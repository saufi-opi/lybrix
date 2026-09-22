package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
