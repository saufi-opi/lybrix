package store

import (
	"context"
	"fmt"

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

// HybridSearch runs the blueprint §5 RRF SQL over pgvector cosine + pg_search
// BM25, with the key-scope collection filter pushed into BOTH CTEs — never
// post-filtered after top_k truncation (R-14).
//
// Parameters: $1 query vector, $2 collection filter (empty → all), $3
// collection scope array (empty → all), $4 bm25 query string, $5 fetch limit.
func (d *DB) HybridSearch(ctx context.Context, queryVec []float32, collection string, collectionScope []string, bm25Query string, limit int) ([]*SearchHit, error) {
	rows, err := d.Pool.Query(ctx, `
WITH dense_matches AS (
    SELECT id, parent_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY embedding <=> $1) AS dense_rank
    FROM chunks
    WHERE is_parent = FALSE
      AND ($2 = '' OR collection_id = $2)
      AND ($6 = '{}'::text[] OR collection_id = ANY($6))
    ORDER BY embedding <=> $1
    LIMIT $5
),
bm25_matches AS (
    SELECT id, parent_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY paradedb.score(id) DESC) AS bm25_rank
    FROM chunks
    WHERE id @@@ paradedb.parse($4) AND is_parent = FALSE
      AND ($2 = '' OR collection_id = $2)
      AND ($6 = '{}'::text[] OR collection_id = ANY($6))
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
    (COALESCE(1.0 / (60 + d.dense_rank), 0.0) * 1.0 +
     COALESCE(1.0 / (60 + b.bm25_rank), 0.0) * 0.3) AS rrf_score,
    doc.id AS doc_id, doc.title AS doc_title, doc.completeness AS completeness
FROM dense_matches d
FULL OUTER JOIN bm25_matches b ON d.id = b.id
LEFT JOIN chunks p ON p.id = COALESCE(d.parent_id, b.parent_id)
JOIN documents doc ON doc.id = COALESCE(d.doc_id, b.doc_id)
ORDER BY rrf_score DESC
LIMIT $5`,
		pgvector.NewVector(queryVec), collection, 0, bm25Query, limit, pqTextArray(collectionScope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SearchHit
	for rows.Next() {
		var h SearchHit
		var parentID *string
		if err := rows.Scan(&h.ChunkID, &parentID, &h.Text, &h.ParentText,
			&h.HeadingPath, &h.PageStart, &h.PageEnd, &h.Score,
			&h.DocID, &h.DocTitle, &h.Completeness); err != nil {
			return nil, err
		}
		if h.Completeness != nil && *h.Completeness < 1.0 {
			h.Partial = true
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// pqTextArray renders an empty-vs-populated text[] literal for the scope
// filter sentinel ($6 = '{}'::text[]). pgx would encode an empty []string
// as '{}' anyway, but keeping the explicit render makes the sentinel test
// obvious.
func pqTextArray(xs []string) []string {
	if xs == nil {
		xs = []string{}
	}
	return xs
}

var _ = fmt.Sprintf
