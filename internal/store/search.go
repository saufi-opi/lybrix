package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// SearchHit is one fused retrieval result — the SearchRequest response item
// and the MCP search tool item share this shape.
type SearchHit struct {
	ChunkID      string
	DocID        string
	DocTitle     *string
	PageStart    *int
	PageEnd      *int
	HeadingPath  []string
	ParentText   string
	Text         string
	Score        float64
	Partial      bool
	Completeness *float64
}

// Dense-weight 1.0 / BM25 0.3 — blueprint §5's weighted RRF. 1.0 measured
// 0.15 against Qdrant on its corpus; 2.0 starts at the blueprint's 0.3 and
// A/Bs down via the eval harness if golden-set eval regresses.
const (
	RRFK        = 60.0
	DenseWeight = 1.0
	BM25Weight  = 0.3
)

// ErrDimMismatch is the typed backstop for a Postgres dimension-mismatch
// error — the API maps it to 503 naming the model, never a 500.
var ErrDimMismatch = errors.New("vector dimension mismatch")

// HybridSearch runs the blueprint §5 RRF SQL over pgvector cosine + pg_search
// BM25, with the key-scope collection filter pushed into BOTH CTEs — never
// post-filtered after top_k truncation (R-14).
//
// dim is the query model's declared dimension: the dense CTE filters
// vector_dims(embedding) = <dim> (literal, matching the per-dim HNSW index
// predicate) and orders by a literal-cast `embedding::vector(<dim>) <=> $1`
// (matching the index expression). Other-dimension rows are excluded, never
// errors — a rebind leaves stale-dim vectors in the table and dense search
// must simply ignore them until a re-embed lands.
//
// Parameters: $1 query vector, $2 collection filter (empty → all), $3
// collection scope array (empty → all), $4 bm25 query string, $5 fetch limit,
// $6 dim (interpolated as a SQL literal — an int from the registry row).
func (d *DB) HybridSearch(ctx context.Context, queryVec []float32, dim int, collection string, collectionScope []string, bm25Query string, limit int) ([]*SearchHit, error) {
	if dim < 1 || dim > 2000 {
		return nil, fmt.Errorf("query dim %d out of range 1..2000", dim)
	}
	rows, err := d.Pool.Query(ctx, fmt.Sprintf(`
WITH dense_matches AS (
    SELECT id, parent_id, doc_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY embedding::vector(%[1]d) <=> $1) AS dense_rank
    FROM chunks
    WHERE is_parent = FALSE
      AND vector_dims(embedding) = %[1]d
      AND ($2 = '' OR collection_id = $2)
      AND ($3 = '{}'::text[] OR collection_id = ANY($3))
    ORDER BY embedding::vector(%[1]d) <=> $1
    LIMIT $5
),
bm25_matches AS (
    SELECT id, parent_id, doc_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY paradedb.score(id) DESC) AS bm25_rank
    FROM chunks
    WHERE id @@@ paradedb.parse($4) AND is_parent = FALSE
      AND ($2 = '' OR collection_id = $2)
      AND ($3 = '{}'::text[] OR collection_id = ANY($3))
    LIMIT $5
)
SELECT
    COALESCE(d.id, b.id) AS chunk_id,
    COALESCE(d.parent_id, b.parent_id) AS parent_id,
    COALESCE(d.text, b.text) AS child_text,
    p.text AS parent_text,
    COALESCE(d.heading_path, b.heading_path) AS heading_path,
    COALESCE(d.page_start, b.page_start) AS page_start,
    COALESCE(d.page_end, b.page_end) AS page_end,
    (COALESCE(1.0 / (%[2]f + d.dense_rank), 0.0) * 1.0 +
     COALESCE(1.0 / (%[2]f + b.bm25_rank), 0.0) * 0.3) AS rrf_score,
    doc.id AS doc_id, doc.title AS doc_title, doc.completeness AS completeness
FROM dense_matches d
FULL OUTER JOIN bm25_matches b ON d.id = b.id
LEFT JOIN chunks p ON p.id = COALESCE(d.parent_id, b.parent_id)
JOIN documents doc ON doc.id = COALESCE(d.doc_id, b.doc_id)
ORDER BY rrf_score DESC
LIMIT $5`,
		dim, float64(RRFK)),
		pgvector.NewVector(queryVec), collection, pqTextArray(collectionScope), bm25Query, limit)
	if err != nil {
		return nil, mapDimErr(err)
	}
	defer rows.Close()
	var out []*SearchHit
	for rows.Next() {
		var h SearchHit
		var parentID *string
		if err := rows.Scan(&h.ChunkID, &parentID, &h.Text, &h.ParentText,
			&h.HeadingPath, &h.PageStart, &h.PageEnd, &h.Score,
			&h.DocID, &h.DocTitle, &h.Completeness); err != nil {
			return nil, mapDimErr(err)
		}
		if h.Completeness != nil && *h.Completeness < 1.0 {
			h.Partial = true
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// mapDimErr converts a Postgres dimension-mismatch failure into the typed
// ErrDimMismatch backstop. Defense in depth: the vector_dims filter should
// make this unreachable, but a mismatched query vector reaching Postgres
// must surface as a typed error (→ 503), not a raw 500.
func mapDimErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "different vector dimensions") ||
		strings.Contains(err.Error(), "does not have dimensions") {
		return fmt.Errorf("%w: %s", ErrDimMismatch, err.Error())
	}
	return err
}

// MultiHybridSearch is the WeKnora multi-knowledge-base search (2.0.2): the
// target collections are grouped by their bound embedding model, the query
// is embedded once per unique model (embedderFn(model) → vector), each group
// contributes a dense leg at its own dimension, one BM25 leg spans all
// target collections, and every leg fuses with weighted RRF into a single
// unified top-K (dense weight 1.0, BM25 weight 0.3, k=60 — same constants
// as HybridSearch).
//
// collections == empty → all collections bind a model; scope is the key's
// collection allowlist pushed into every leg's SQL filter (R-14 — never
// post-filtered). embedderFn is called once per unique model, never per
// collection.
func (d *DB) MultiHybridSearch(ctx context.Context, query string, collections []string, scope []string, topK int, embedderFn func(m *EmbeddingModel) ([]float32, error)) ([]*SearchHit, error) {
	// Resolve the target set: explicit ids, else every collection. The key
	// scope is an allowlist — intersect BEFORE grouping so out-of-scope
	// models are never embedded (a down backend the key can't see must not
	// fail the search) and never reach a dense leg.
	targets := collections
	if len(targets) == 0 {
		rows, err := d.Pool.Query(ctx, `SELECT id FROM collections ORDER BY created_at`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		targets = []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			targets = append(targets, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(scope) > 0 {
		allowed := make(map[string]bool, len(scope))
		for _, s := range scope {
			allowed[s] = true
		}
		filtered := make([]string, 0, len(targets))
		for _, t := range targets {
			if allowed[t] {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	}
	modelsByCollection, err := d.GetModelsByCollectionIDs(ctx, targets)
	if err != nil {
		return nil, err
	}

	// Group target collections by model id, embedding the query once per
	// unique model.
	type group struct {
		model *EmbeddingModel
		cols  []string
		vec   []float32
	}
	groups := map[string]*group{}
	var order []string // deterministic fusion order
	for _, cid := range targets {
		m := modelsByCollection[cid]
		if m == nil {
			continue // unbound collection: BM25-only participation below
		}
		g, ok := groups[m.ID]
		if !ok {
			vec, err := embedderFn(m)
			if err != nil {
				return nil, err
			}
			g = &group{model: m, vec: vec}
			groups[m.ID] = g
			order = append(order, m.ID)
		}
		g.cols = append(g.cols, cid)
	}

	// One BM25 leg across ALL target collections (sparse needs no model).
	bm25Hits, err := d.HybridBMSearch(ctx, targets, scope, query, topK)
	if err != nil {
		return nil, err
	}

	// Accumulate RRF: score = Σ weight / (k + rank), rank 1-based per leg.
	scores := map[string]float64{}
	best := map[string]*SearchHit{}
	addLeg := func(hits []*SearchHit, weight float64) {
		for i, h := range hits {
			scores[h.ChunkID] += weight / (RRFK + float64(i+1))
			if prev, ok := best[h.ChunkID]; !ok || len(h.Text) > len(prev.Text) {
				best[h.ChunkID] = h
			}
		}
	}
	addLeg(bm25Hits, BM25Weight)
	for _, id := range order {
		g := groups[id]
		denseHits, err := d.HybridDenseSearch(ctx, g.vec, g.model.VectorDim, g.cols, scope, topK)
		if err != nil {
			return nil, err
		}
		addLeg(denseHits, DenseWeight)
	}

	// Rank the fused scores, fill any missing metadata from another leg's
	// copy of the hit, and truncate to topK.
	type fused struct {
		hit   *SearchHit
		score float64
	}
	fusedList := make([]fused, 0, len(scores))
	for cid, score := range scores {
		fusedList = append(fusedList, fused{best[cid], score})
	}
	sort.Slice(fusedList, func(i, j int) bool { return fusedList[i].score > fusedList[j].score })
	if len(fusedList) > topK {
		fusedList = fusedList[:topK]
	}
	out := make([]*SearchHit, 0, len(fusedList))
	for _, f := range fusedList {
		h := f.hit
		h.Score = f.score
		if h.HeadingPath == nil {
			h.HeadingPath = []string{}
		}
		out = append(out, h)
	}
	return out, nil
}

// HybridDenseSearch runs one model group's dense leg: cosine top-K over the
// group's dimension's partial HNSW, filtered to the group's collections and
// the key scope. Only ids + ranking data are needed — fusion refetches
// nothing (each hit carries its full row from the SQL select).
func (d *DB) HybridDenseSearch(ctx context.Context, queryVec []float32, dim int, collections, scope []string, limit int) ([]*SearchHit, error) {
	if dim < 1 || dim > 2000 {
		return nil, fmt.Errorf("query dim %d out of range 1..2000", dim)
	}
	rows, err := d.Pool.Query(ctx, fmt.Sprintf(`
SELECT c.id, c.doc_id, doc.title, c.page_start, c.page_end, c.heading_path,
       p.text, c.text, doc.completeness
FROM (SELECT id, parent_id, doc_id, text, heading_path, page_start, page_end,
             ROW_NUMBER() OVER (ORDER BY embedding::vector(%[1]d) <=> $3) AS dense_rank
      FROM chunks
      WHERE is_parent = FALSE
        AND vector_dims(embedding) = %[1]d
        AND collection_id = ANY($1)
        AND ($2 = '{}'::text[] OR collection_id = ANY($2))
      ORDER BY embedding::vector(%[1]d) <=> $3
      LIMIT $4) c
LEFT JOIN chunks p ON p.id = c.parent_id
JOIN documents doc ON doc.id = c.doc_id
ORDER BY c.dense_rank`,
		dim), pqTextArray(collections), pqTextArray(scope), pgvector.NewVector(queryVec), limit)
	if err != nil {
		return nil, mapDimErr(err)
	}
	defer rows.Close()
	return scanHits(rows)
}

// HybridBMSearch runs the sparse leg across all target collections: BM25
// top-K over pg_search, filtered by collection set and key scope.
func (d *DB) HybridBMSearch(ctx context.Context, collections, scope []string, bm25Query string, limit int) ([]*SearchHit, error) {
	rows, err := d.Pool.Query(ctx, `
SELECT c.id, c.doc_id, doc.title, c.page_start, c.page_end, c.heading_path,
       p.text, c.text, doc.completeness
FROM (SELECT id, parent_id, doc_id, text, heading_path, page_start, page_end,
             ROW_NUMBER() OVER (ORDER BY paradedb.score(id) DESC) AS bm25_rank
      FROM chunks
      WHERE id @@@ paradedb.parse($1) AND is_parent = FALSE
        AND ($2 = '{}'::text[] OR collection_id = ANY($2))
        AND ($3 = '{}'::text[] OR collection_id = ANY($3))
      ORDER BY paradedb.score(id) DESC
      LIMIT $4) c
LEFT JOIN chunks p ON p.id = c.parent_id
JOIN documents doc ON doc.id = c.doc_id
ORDER BY c.bm25_rank`,
		bm25Query, pqTextArray(collections), pqTextArray(scope), limit)
	if err != nil {
		return nil, mapDimErr(err)
	}
	defer rows.Close()
	return scanHits(rows)
}

// scanHits reads the shared (chunk, parent-text, doc) projection both legs
// return.
func scanHits(rows pgx.Rows) ([]*SearchHit, error) {
	var out []*SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.DocTitle, &h.PageStart,
			&h.PageEnd, &h.HeadingPath, &h.ParentText, &h.Text,
			&h.Completeness); err != nil {
			return nil, mapDimErr(err)
		}
		if h.Completeness != nil && *h.Completeness < 1.0 {
			h.Partial = true
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// pqTextArray renders an empty-vs-populated text[] literal for the scope
// filter sentinel ($3 = '{}'::text[]). pgx would encode an empty []string
// as '{}' anyway, but keeping the explicit render makes the sentinel test
// obvious.
func pqTextArray(xs []string) []string {
	if xs == nil {
		xs = []string{}
	}
	return xs
}
