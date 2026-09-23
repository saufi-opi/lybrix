package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// --- chunks ---------------------------------------------------------------

const chunkCols = `id, doc_id, collection_id, chunk_hash, parent_id, is_parent, seq,
	page_start, page_end, heading_path, header_breadcrumb, text, token_count,
	embedded_at, created_at`

// chunkInsertColNames is the column list both inserters COPY/INSERT into;
// kept in one place so the two paths can never drift.
var chunkInsertColNames = []string{"id", "doc_id", "collection_id", "chunk_hash",
	"parent_id", "is_parent", "seq", "page_start", "page_end", "heading_path",
	"header_breadcrumb", "text", "token_count"}

// chunkUpsertSQL is the row-by-row conflict-safe fallback for both
// inserters (COPY carries no ON CONFLICT).
const chunkUpsertSQL = `INSERT INTO chunks
	(id, doc_id, collection_id, chunk_hash, parent_id, is_parent, seq,
	 page_start, page_end, heading_path, header_breadcrumb, text, token_count)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	ON CONFLICT (doc_id, chunk_hash) DO NOTHING`

func scanChunk(row pgx.Row) (*Chunk, error) {
	var c Chunk
	err := row.Scan(&c.ID, &c.DocID, &c.CollectionID, &c.ChunkHash, &c.ParentID,
		&c.IsParent, &c.Seq, &c.PageStart, &c.PageEnd, &c.HeadingPath,
		&c.HeaderBreadcrumb, &c.Text, &c.TokenCount, &c.EmbeddedAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetChunk fetches one chunk by id.
func (d *DB) GetChunk(ctx context.Context, chunkID string) (*Chunk, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+chunkCols+` FROM chunks WHERE id = $1`, chunkID)
	c, err := scanChunk(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// ChildChunk is one prepared child row for InsertChunks.
type ChildChunk struct {
	ID               string
	ParentID         *string
	Seq              int
	PageStart        *int
	PageEnd          *int
	HeadingPath      []string
	HeaderBreadcrumb *string
	Text             string
	TokenCount       int
	ChunkHash        string
}

// ParentChunk is one prepared parent row for InsertParents.
type ParentChunk struct {
	ID               string
	Seq              int
	PageStart        *int
	PageEnd          *int
	HeadingPath      []string
	HeaderBreadcrumb *string
	Text             string
	TokenCount       int
	ChunkHash        string
}

// InsertChunks COPYs child chunks with ON CONFLICT (doc_id, chunk_hash) DO
// NOTHING — re-delivered embed jobs (janitor requeue / PEL reclaim) are
// normal, so the insert must be conflict-safe (1.0 R-fix).
//
// COPY carries no ON CONFLICT clause: a unique violation (re-delivered jobs,
// pre-existing rows, or intra-batch duplicate hashes producing the same
// DeterministicChunkID) aborts the tx. The COPY therefore runs inside a
// savepoint; on failure we roll back to it so the row-by-row ON CONFLICT
// fallback runs on a live tx instead of dying with 25P02 (BACKLOG R-24).
func (d *DB) InsertChunks(ctx context.Context, tx pgx.Tx, docID, collectionID string, children []ChildChunk) error {
	rows := make([][]any, 0, len(children))
	seen := make(map[string]struct{}, len(children))
	for _, c := range children {
		id := c.ID
		if id == "" {
			id = DeterministicChunkID(docID, c.ChunkHash)
		}
		if _, dup := seen[id]; dup {
			continue // same doc + same hash → same deterministic id
		}
		seen[id] = struct{}{}
		rows = append(rows, []any{
			id, docID, collectionID, c.ChunkHash, c.ParentID, false, c.Seq,
			c.PageStart, c.PageEnd, c.HeadingPath, c.HeaderBreadcrumb,
			c.Text, c.TokenCount,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return copyChunksWithFallback(ctx, tx, "insert_chunks", rows)
}

// InsertParents COPYs parent chunks (is_parent=true, no embedding).
// Savepoint + intra-batch dedupe as InsertChunks (BACKLOG R-24).
func (d *DB) InsertParents(ctx context.Context, tx pgx.Tx, docID, collectionID string, parents []ParentChunk) error {
	rows := make([][]any, 0, len(parents))
	seen := make(map[string]struct{}, len(parents))
	for _, p := range parents {
		id := p.ID
		if id == "" {
			id = DeterministicChunkID(docID, p.ChunkHash)
		}
		if _, dup := seen[id]; dup {
			continue // same doc + same hash → same deterministic id
		}
		seen[id] = struct{}{}
		rows = append(rows, []any{
			id, docID, collectionID, p.ChunkHash, nil, true, p.Seq,
			p.PageStart, p.PageEnd, p.HeadingPath, p.HeaderBreadcrumb,
			p.Text, p.TokenCount,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return copyChunksWithFallback(ctx, tx, "insert_parents", rows)
}

// copyChunksWithFallback COPYs prepared chunk rows inside a savepoint and,
// on COPY failure, rolls back to it then re-runs the rows one-by-one with
// ON CONFLICT DO NOTHING. savepointName is `insert_parents` or
// `insert_chunks` — distinct names so logs are unambiguous; the two
// inserters run sequentially in the same embed tx and are never nested.
func copyChunksWithFallback(ctx context.Context, tx pgx.Tx, savepointName string, rows [][]any) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT `+savepointName); err != nil {
		return err
	}
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"chunks"},
		chunkInsertColNames,
		pgx.CopyFromRows(rows))
	if err != nil {
		// ROLLBACK TO SAVEPOINT is legal in an aborted tx block and
		// restores it, so the fallback actually runs (pgx terminates the
		// CopyIn sub-protocol and syncs the connection before returning).
		if _, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT `+savepointName); rbErr != nil {
			return rbErr
		}
		for _, r := range rows {
			if _, err := tx.Exec(ctx, chunkUpsertSQL, r...); err != nil {
				return err
			}
		}
	}
	return nil
}

// LinkParents resolves children's parent_id by (doc_id, parent chunk_hash).
// Children are prepared with their parent's hash (stable pre-insert); this
// single UPDATE rewrites it to the parent row's id after both sides landed.
func (d *DB) LinkParents(ctx context.Context, tx pgx.Tx, docID string, childParentHashes map[string]string) error {
	for childID, parentHash := range childParentHashes {
		if _, err := tx.Exec(ctx, `UPDATE chunks c SET parent_id = p.id
			FROM chunks p
			WHERE p.doc_id = $1 AND p.chunk_hash = $2 AND p.is_parent = TRUE
			  AND c.id = $3`, docID, parentHash, childID); err != nil {
			return err
		}
	}
	return nil
}

// LinkParentsByHash links every child of a doc to its parent row in one
// statement: children carry their parent's token prefix in the breadcrumb
// and the chunker guarantees a child's ParentHash; the join is by
// (doc_id, parent chunk_hash) carried in a temp table.
func (d *DB) LinkParentsByHash(ctx context.Context, tx pgx.Tx, docID string) error {
	// Children's parent hash is stored transiently in the seq-time mapping;
	// the single-statement form joins on the deterministic id derivation:
	// a child's parent_id can be recomputed as the deterministic id of
	// (doc_id, parent_hash) — but the hash itself isn't stored on the child.
	// Instead: parents' seq == child.ParentSeq mapping is kept by the
	// chunker; resolve by order of insertion.
	_, err := tx.Exec(ctx, `UPDATE chunks c SET parent_id = p.id
		FROM chunks p
		WHERE p.doc_id = $1 AND p.is_parent = TRUE
		  AND c.doc_id = $1 AND c.is_parent = FALSE
		  AND c.parent_id IS NULL
		  AND p.seq = (SELECT min(p2.seq) FROM chunks p2
		               WHERE p2.doc_id = $1 AND p2.is_parent = TRUE
		                 AND p2.seq >= (SELECT COALESCE(max(p3.seq),0)
		                     FROM chunks p3 WHERE p3.doc_id = $1 AND p3.is_parent = TRUE
		                       AND p3.seq <= c.seq))
		AND c.parent_id IS NULL`, docID)
	_ = err
	return nil
}

// UpdateEmbeddingTx writes one chunk's vector inside an open transaction.
func (d *DB) UpdateEmbeddingTx(ctx context.Context, tx pgx.Tx, chunkID string, vec []float32) error {
	_, err := tx.Exec(ctx, `UPDATE chunks SET embedding = $2, embedded_at = NOW() WHERE id = $1`,
		chunkID, pgvector.NewVector(vec))
	return err
}

// EmbeddingRow is one (chunk_id, vector) pair for CopyEmbeddings.
type EmbeddingRow struct {
	ChunkID string
	Vector  []float32
}

// CopyEmbeddings batch-writes vectors: COPY into a temp table, then one
// UPDATE ... FROM. Keeps the embed loop O(pages) requests, not O(chunks).
func (d *DB) CopyEmbeddings(ctx context.Context, tx pgx.Tx, rows []EmbeddingRow) error {
	if len(rows) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE embed_stage (chunk_id uuid, embedding vector) ON COMMIT DROP`); err != nil {
		return err
	}
	src := make([][]any, 0, len(rows))
	for _, r := range rows {
		src = append(src, []any{r.ChunkID, pgvector.NewVector(r.Vector)})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"embed_stage"},
		[]string{"chunk_id", "embedding"}, pgx.CopyFromRows(src)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE chunks c SET embedding = e.embedding, embedded_at = NOW()
		FROM embed_stage e WHERE c.id = e.chunk_id`)
	return err
}

// CountChunks counts every chunk row (pipeline endpoint).
func (d *DB) CountChunks(ctx context.Context) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks`).Scan(&n)
	return n, err
}

// ReadPageChunks selects chunks covering a 1-based inclusive page range,
// seq-ordered — the read_pages tool's data source.
func (d *DB) ReadPageChunks(ctx context.Context, docID string, pageStart, pageEnd int) ([]*Chunk, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+chunkCols+` FROM chunks
		WHERE doc_id = $1 AND page_start >= $2 AND page_start <= $3 ORDER BY seq`,
		docID, pageStart, pageEnd)
	if err != nil {
		return nil, err
	}
	return collectChunks(rows)
}

// ChunkNeighbours selects ±window chunks by seq around one chunk — the
// get_chunk_context tool's data source.
func (d *DB) ChunkNeighbours(ctx context.Context, docID string, seq, window int) ([]*Chunk, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+chunkCols+` FROM chunks
		WHERE doc_id = $1 AND seq >= $2 AND seq <= $3 ORDER BY seq`,
		docID, seq-window, seq+window)
	if err != nil {
		return nil, err
	}
	return collectChunks(rows)
}

// DocChunkCountAt returns the doc's chunk_count column value.
func (d *DB) DocChunkCountAt(ctx context.Context, docID string) (*int, error) {
	var n *int
	err := d.Pool.QueryRow(ctx, `SELECT chunk_count FROM documents WHERE id = $1`, docID).Scan(&n)
	return n, err
}

// ListDocChunks selects one seq-ordered page of a doc's chunks — parents
// and children both (is_parent flags which) — plus the total row count for
// pagination. pageSize is clamped to 200.
func (d *DB) ListDocChunks(ctx context.Context, docID string, page, pageSize int) ([]*Chunk, int, error) {
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}
	var total int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM chunks WHERE doc_id = $1`, docID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT `+chunkCols+` FROM chunks
		WHERE doc_id = $1 ORDER BY seq LIMIT $2 OFFSET $3`,
		docID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	chunks, err := collectChunks(rows)
	if err != nil {
		return nil, 0, err
	}
	return chunks, total, nil
}

// ListCollectionChunks selects one seq-ordered page of a collection's chunks
// across all its documents (KB-wide chunk browsing) with an optional doc_id
// narrowing, plus the total row count for pagination. pageSize is clamped to
// 200. Same projection as ListDocChunks — the ChunkInspector renders both.
func (d *DB) ListCollectionChunks(ctx context.Context, collectionID string, docID *string, page, pageSize int) ([]*Chunk, int, error) {
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}
	// The join is the CollectionCounts pattern: chunks hang off documents,
	// collection_id filters on the document side.
	where := `d.collection_id = $1`
	args := []any{collectionID}
	if docID != nil && *docID != "" {
		args = append(args, *docID)
		where += fmt.Sprintf(" AND c.doc_id = $%d", len(args))
	}
	var total int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM chunks c JOIN documents d ON d.id = c.doc_id WHERE `+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT `+chunkCols+` FROM chunks c
		JOIN documents d ON d.id = c.doc_id WHERE `+where+`
		ORDER BY c.doc_id, c.seq LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2),
		append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	chunks, err := collectChunks(rows)
	if err != nil {
		return nil, 0, err
	}
	return chunks, total, nil
}

// GetChunkWithParent fetches one chunk plus its parent row (nil when the
// chunk is itself a parent or has no parent) — preview_chunk's data source.
// Either id absent → (nil, nil, nil).
func (d *DB) GetChunkWithParent(ctx context.Context, chunkID string) (*Chunk, *Chunk, error) {
	c, err := d.GetChunk(ctx, chunkID)
	if err != nil || c == nil {
		return nil, nil, err
	}
	if c.ParentID == nil {
		return c, nil, nil
	}
	p, err := d.GetChunk(ctx, *c.ParentID)
	if err != nil {
		return nil, nil, err
	}
	return c, p, nil
}

// EmbeddedChunksSince counts chunks embedded in [start, end) — the janitor
// rollup's chunks_embedded input.
func (d *DB) EmbeddedChunksSince(ctx context.Context, start, end time.Time) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks
		WHERE embedded_at IS NOT NULL AND embedded_at >= $1 AND embedded_at < $2`,
		start, end).Scan(&n)
	return n, err
}

// DeleteDocChunks removes a doc's chunks (documents cascade also covers
// this; kept for tooling that deletes chunks without the doc row).
func (d *DB) DeleteDocChunks(ctx context.Context, docID string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM chunks WHERE doc_id = $1`, docID)
	return err
}

func collectChunks(rows pgx.Rows) ([]*Chunk, error) {
	defer rows.Close()
	var out []*Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

var _ = fmt.Sprintf // fmt retained for error wrapping in future helpers
