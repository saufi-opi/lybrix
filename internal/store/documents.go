package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// --- documents -----------------------------------------------------------

const docCols = `id, collection_id, title, author, filename, byte_size, source_uri,
	content_sha256, page_count, mime_type, state, error_code, error_detail,
	total_shards, shards_done, shards_failed, chunk_count, completeness,
	metadata, uploaded_by, created_at, updated_at, ready_at`

func scanDoc(row pgx.Row) (*Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.CollectionID, &d.Title, &d.Author, &d.Filename,
		&d.ByteSize, &d.SourceURI, &d.ContentSHA256, &d.PageCount, &d.MimeType,
		&d.State, &d.ErrorCode, &d.ErrorDetail, &d.TotalShards, &d.ShardsDone,
		&d.ShardsFailed, &d.ChunkCount, &d.Completeness, &d.Metadata,
		&d.UploadedBy, &d.CreatedAt, &d.UpdatedAt, &d.ReadyAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDocument fetches one document by id.
func (d *DB) GetDocument(ctx context.Context, docID string) (*Document, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+docCols+` FROM documents WHERE id = $1`, docID)
	doc, err := scanDoc(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// GetDocumentTx is GetDocument inside an existing transaction. The parser's
// post-MarkShardDone settled check must read shards_done through the same tx
// — a pool read cannot see the uncommitted increment, so BookSettled would
// stay false until the janitor's next pass.
func (d *DB) GetDocumentTx(ctx context.Context, tx pgx.Tx, docID string) (*Document, error) {
	row := tx.QueryRow(ctx, `SELECT `+docCols+` FROM documents WHERE id = $1`, docID)
	doc, err := scanDoc(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// ResetDocForFullRetry tears a doc back down to UPLOADED for a scope="full"
// retry: its chunks and shards rows are deleted and every derived counter
// and marker is cleared. In-flight parse jobs for the old shards hit
// ClaimShard, find no row, and return nil — ACKed safely (no retry storm).
func (d *DB) ResetDocForFullRetry(ctx context.Context, tx pgx.Tx, docID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM shards WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE documents SET state = 'uploaded',
		total_shards = NULL, shards_done = 0, shards_failed = 0,
		chunk_count = NULL, completeness = NULL, error_code = NULL,
		error_detail = NULL, ready_at = NULL, updated_at = NOW()
		WHERE id = $1`, docID)
	return err
}

// FindDuplicate is upload dedupe: content_sha256 unique per collection (PRD §5).
func (d *DB) FindDuplicate(ctx context.Context, collectionID, contentSha256 string) (*Document, error) {
	row := d.Pool.QueryRow(ctx,
		`SELECT `+docCols+` FROM documents WHERE collection_id = $1 AND content_sha256 = $2`,
		collectionID, contentSha256)
	doc, err := scanDoc(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// SetDocState writes a state transition (and optionally the error columns).
func (d *DB) SetDocState(ctx context.Context, tx pgx.Tx, docID string, state DocState, errorCode, errorDetail *string) error {
	_, err := tx.Exec(ctx,
		`UPDATE documents SET state = $2, error_code = $3, error_detail = $4, updated_at = NOW() WHERE id = $1`,
		docID, state, errorCode, errorDetail)
	return err
}

// SetDocStatePool is SetDocState on the pool (no ambient tx).
func (d *DB) SetDocStatePool(ctx context.Context, docID string, state DocState, errorCode, errorDetail *string) error {
	_, err := d.Pool.Exec(ctx,
		`UPDATE documents SET state = $2, error_code = $3, error_detail = $4, updated_at = NOW() WHERE id = $1`,
		docID, state, errorCode, errorDetail)
	return err
}

// InsertDocument registers a committed upload. MimeType rides along: the
// splitter routes on it (non-PDF → single synthetic shard), so a dropped
// mime_type would default every doc to application/pdf and send EPUBs into
// pdfcpu.
func (d *DB) InsertDocument(ctx context.Context, doc *Document) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO documents
		(id, collection_id, title, author, source_uri, content_sha256, byte_size, state, mime_type, metadata, uploaded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		doc.ID, doc.CollectionID, doc.Title, doc.Author, doc.SourceURI,
		doc.ContentSHA256, doc.ByteSize, doc.State, doc.MimeType, doc.Metadata, doc.UploadedBy)
	return err
}

// ListDocuments ordered by updated_at desc with the OpenAPI filters.
func (d *DB) ListDocuments(ctx context.Context, state *DocState, collection, q *string, limit, offset int) ([]*Document, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	qry := `SELECT ` + docCols + ` FROM documents`
	args := []any{}
	where := []string{}
	if state != nil {
		args = append(args, *state)
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if collection != nil && *collection != "" {
		args = append(args, *collection)
		where = append(where, fmt.Sprintf("collection_id = $%d", len(args)))
	}
	if q != nil && *q != "" {
		args = append(args, "%"+*q+"%")
		where = append(where, fmt.Sprintf("title ILIKE $%d", len(args)))
	}
	if len(where) > 0 {
		qry += " WHERE " + joinAnd(where)
	}
	args = append(args, limit, offset)
	qry += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := d.Pool.Query(ctx, qry, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		doc, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

func joinAnd(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += " AND "
		}
		s += p
	}
	return s
}

// --- shards ---------------------------------------------------------------

const shardCols = `doc_id, idx, page_start, page_end, state, attempts, needs_ocr,
	mean_chars_per_page, parsed_uri, worker_id, lease_until, duration_ms,
	peak_rss_mb, done_at, error_code, error_detail, created_at`

func scanShard(row pgx.Row) (*Shard, error) {
	var s Shard
	err := row.Scan(&s.DocID, &s.Idx, &s.PageStart, &s.PageEnd, &s.State,
		&s.Attempts, &s.NeedsOCR, &s.MeanCharsPerPage, &s.ParsedURI, &s.WorkerID,
		&s.LeaseUntil, &s.DurationMS, &s.PeakRSSMB, &s.DoneAt, &s.ErrorCode,
		&s.ErrorDetail, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetShard fetches one shard row.
func (d *DB) GetShard(ctx context.Context, docID string, idx int) (*Shard, error) {
	row := d.Pool.QueryRow(ctx,
		`SELECT `+shardCols+` FROM shards WHERE doc_id = $1 AND idx = $2`, docID, idx)
	s, err := scanShard(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// GetShards lists a document's shard rows ordered by idx (shard grid).
func (d *DB) GetShards(ctx context.Context, docID string) ([]*Shard, error) {
	rows, err := d.Pool.Query(ctx,
		`SELECT `+shardCols+` FROM shards WHERE doc_id = $1 ORDER BY idx`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Shard
	for rows.Next() {
		s, err := scanShard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// NextShardIdx is max idx per doc + 1 — where retry-ladder sub-shards continue.
func (d *DB) NextShardIdx(ctx context.Context, tx pgx.Tx, docID string) (int, error) {
	var maxIdx *int
	if err := tx.QueryRow(ctx,
		`SELECT max(idx) FROM shards WHERE doc_id = $1`, docID).Scan(&maxIdx); err != nil {
		return 0, err
	}
	if maxIdx == nil {
		return 0, nil
	}
	return *maxIdx + 1, nil
}

// InsertShards inserts one row per shard bound; idx runs from startIdx
// (retry-ladder sub-shards continue after the parent's idx so idx stays
// unique per doc). Returns the count inserted.
func (d *DB) InsertShards(ctx context.Context, tx pgx.Tx, docID string, bounds [][2]int, startIdx int) (int, error) {
	for i, b := range bounds {
		if _, err := tx.Exec(ctx,
			`INSERT INTO shards (doc_id, idx, page_start, page_end) VALUES ($1,$2,$3,$4)`,
			docID, startIdx+i, b[0], b[1]); err != nil {
			return i, err
		}
	}
	return len(bounds), nil
}

// SkipShard marks a parent shard SKIPPED — replaced by retry-ladder
// sub-shards; the embedder selects state=='done' only, so skipped parents
// never embed.
func (d *DB) SkipShard(ctx context.Context, tx pgx.Tx, docID string, idx int) error {
	tag, err := tx.Exec(ctx,
		`UPDATE shards SET state = 'skipped' WHERE doc_id = $1 AND idx = $2`, docID, idx)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("shard %s/%d not found", docID, idx)
	}
	return nil
}

// ClaimShard is the atomic claim (PRD §6.3 step 1): UPDATE ... WHERE state
// IN (pending, failed) RETURNING — zero rows means someone else got it.
func (d *DB) ClaimShard(ctx context.Context, tx pgx.Tx, docID string, idx int, workerID string, leaseSeconds int) (*Shard, error) {
	leaseUntil := time.Now().Add(time.Duration(leaseSeconds) * time.Second)
	rows, err := tx.Query(ctx, `UPDATE shards
		SET state = 'running', attempts = attempts + 1, worker_id = $3, lease_until = $4
		WHERE doc_id = $1 AND idx = $2 AND state IN ('pending','failed')
		RETURNING `+shardCols,
		docID, idx, workerID, leaseUntil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanShard(rows)
}

// MarkShardDone marks done and atomically bumps shards_done (PRD §6.3
// step 6). needsOCR persists the OCR-gate verdict so per-shard OCR
// decisions stay queryable after the fact.
func (d *DB) MarkShardDone(ctx context.Context, tx pgx.Tx, docID string, idx, durationMS, peakRSSMB int, parsedURI string, needsOCR bool, meanChars *float64) error {
	if _, err := tx.Exec(ctx, `UPDATE shards SET state = 'done', done_at = NOW(),
		needs_ocr = $3, duration_ms = $4, peak_rss_mb = $5, parsed_uri = $6,
		lease_until = NULL, mean_chars_per_page = $7,
		error_code = NULL, error_detail = NULL
		WHERE doc_id = $1 AND idx = $2`,
		docID, idx, needsOCR, durationMS, peakRSSMB, parsedURI, meanChars); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE documents SET shards_done = shards_done + 1, updated_at = NOW() WHERE id = $1`, docID)
	return err
}

// MarkShardFailed marks a shard failed and bumps shards_failed.
func (d *DB) MarkShardFailed(ctx context.Context, tx pgx.Tx, docID string, idx int, errorCode, errorDetail string) error {
	if _, err := tx.Exec(ctx, `UPDATE shards SET state = 'failed',
		error_code = $3, error_detail = $4, lease_until = NULL
		WHERE doc_id = $1 AND idx = $2`,
		docID, idx, errorCode, errorDetail); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE documents SET shards_failed = shards_failed + 1, updated_at = NOW() WHERE id = $1`, docID)
	return err
}

// BookSettled reports whether every shard is done or failed → time to
// enqueue embed (PRD §6.3 step 7).
func BookSettled(doc *Document) bool {
	total := 0
	if doc.TotalShards != nil {
		total = *doc.TotalShards
	}
	return total > 0 && (doc.ShardsDone+doc.ShardsFailed) >= total
}

// RequeueExpiredLeases is the janitor Reaper (PRD §6.6): running shards
// whose lease expired go back to pending. This is how a docker kill on a
// parser recovers. Returns rows requeued.
func (d *DB) RequeueExpiredLeases(ctx context.Context) (int64, error) {
	tag, err := d.Pool.Exec(ctx, `UPDATE shards SET state = 'pending', worker_id = NULL, lease_until = NULL
		WHERE state = 'running' AND lease_until < NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteDocument removes a doc row; chunks/shards cascade.
func (d *DB) DeleteDocument(ctx context.Context, docID string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docID)
	return err
}

// DeleteDocumentsBatch removes several doc rows in one statement — the batch
// delete path. chunks/shards cascade per row exactly like DeleteDocument;
// unknown ids are simply not present, so the caller counts rows affected to
// report per-doc outcomes.
func (d *DB) DeleteDocumentsBatch(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := d.Pool.Exec(ctx, `DELETE FROM documents WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// --- chunk helpers used by documents.go callers ---------------------------

// CountDocChunks counts chunks for one doc.
func (d *DB) CountDocChunks(ctx context.Context, docID string) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE doc_id = $1`, docID).Scan(&n)
	return n, err
}

// UpdateEmbedding writes one chunk's vector (batched upserts go through
// chunks.go CopyEmbeddings; this is the single-row helper for tests/tools).
func (d *DB) UpdateEmbedding(ctx context.Context, chunkID string, vec []float32) error {
	_, err := d.Pool.Exec(ctx, `UPDATE chunks SET embedding = $2, embedded_at = NOW() WHERE id = $1`,
		chunkID, pgvector.NewVector(vec))
	return err
}
