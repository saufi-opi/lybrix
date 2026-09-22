package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// EscalateStuckShards marks pending shards at/over max attempts as failed
// with SHARD_TIMEOUT and bumps shards_failed per doc. Returns the number
// escalated plus per-doc counters for the events rows the janitor writes.
func (d *DB) EscalateStuckShards(ctx context.Context, tx pgx.Tx, maxAttempts int) ([]EscalatedShard, error) {
	rows, err := tx.Query(ctx, `UPDATE shards SET state = 'failed',
			error_code = 'SHARD_TIMEOUT', error_detail = 'escalated after ' || attempts || ' attempts'
		WHERE state = 'pending' AND attempts >= $1
		RETURNING doc_id, idx, attempts`, maxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EscalatedShard
	for rows.Next() {
		var e EscalatedShard
		if err := rows.Scan(&e.DocID, &e.Idx, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, e := range out {
		if _, err := tx.Exec(ctx,
			`UPDATE documents SET shards_failed = shards_failed + 1, updated_at = NOW() WHERE id = $1`,
			e.DocID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EscalatedShard is one shard the janitor escalated to failed.
type EscalatedShard struct {
	DocID    string
	Idx      int
	Attempts int
}

// NonTerminalDocs lists docs not in a terminal state (requeue sweeps).
func (d *DB) NonTerminalDocs(ctx context.Context) ([]*Document, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+docCols+` FROM documents
		WHERE state NOT IN ('ready','failed','archived','partial')`)
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

// SettledParsingDocs lists docs in state=parsing whose shards all settled
// (done+failed >= total) — the embed-rescue sweep input.
func (d *DB) SettledParsingDocs(ctx context.Context) ([]*Document, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+docCols+` FROM documents
		WHERE state = 'parsing' AND total_shards IS NOT NULL
		  AND shards_done + shards_failed >= total_shards`)
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

// StuckDocs returns non-terminal docs untouched for over the stuck window —
// the informational STUCK_DOCUMENT warning input.
func (d *DB) StuckDocs(ctx context.Context, cutoff time.Time) ([]*Document, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+docCols+` FROM documents
		WHERE state NOT IN ('ready','failed','archived','partial') AND updated_at < $1`, cutoff)
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

// SettledShards lists a doc's done shards in page_start order — the
// embedder's stitch input. Sub-shards (retry ladder) continue idx after the
// parent's; page_start is the true document order (R-11 ladder, PRD §6.3).
func (d *DB) SettledShards(ctx context.Context, docID string) ([]*Shard, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+shardCols+` FROM shards
		WHERE doc_id = $1 AND state = 'done' ORDER BY page_start`, docID)
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

// FailedShards lists a doc's failed shards ordered by idx (retry scope=shards).
func (d *DB) FailedShards(ctx context.Context, docID string) ([]*Shard, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+shardCols+` FROM shards
		WHERE doc_id = $1 AND state = 'failed' ORDER BY idx`, docID)
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

// RequeueFailedShards resets failed shards to pending, clears their error
// columns, and unwinds shards_failed by the number requeued (counter
// hygiene from documents.py retry — without this the embedder would compute
// a wrong completeness and book_settled could double-count, R-11).
func (d *DB) RequeueFailedShards(ctx context.Context, tx pgx.Tx, docID string) (int, error) {
	tag, err := tx.Exec(ctx, `UPDATE shards SET state = 'pending', error_code = NULL, error_detail = NULL
		WHERE doc_id = $1 AND state = 'failed'`, docID)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE documents SET shards_failed = shards_failed - $2, updated_at = NOW() WHERE id = $1`,
			docID, n); err != nil {
			return n, err
		}
	}
	return n, nil
}

// SetChunkCountAndCompleteness finalizes the doc after embedding: ready or
// partial depending on failed shards, plus chunk_count and completeness.
func (d *DB) SetChunkCountAndCompleteness(ctx context.Context, tx pgx.Tx, docID string, chunkCount int) error {
	if _, err := tx.Exec(ctx, `UPDATE documents SET chunk_count = $2,
			completeness = CASE WHEN total_shards IS NULL THEN NULL
				ELSE round((total_shards - shards_failed)::numeric / total_shards, 4) END
			WHERE id = $1`, docID, chunkCount); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE documents SET state = CASE WHEN shards_failed > 0
			THEN 'partial' ELSE 'ready' END, ready_at = NOW(), updated_at = NOW()
		WHERE id = $1`, docID)
	return err
}

// MetricsSnapshotRow aggregates the /pipeline counters in one round trip.
type MetricsSnapshotRow struct {
	DocsReady, DocsParsing, DocsFailed, DocsTotal          int64
	DocsAwaitingEmbed, DocsParsingActive                   int64
	ShardsDone, ShardsPending, ShardsFailed, ShardsRunning int64
	ShardsTotal                                            int64
	States                                                 map[string]int64
}

// MetricsSnapshot reads the document/shard counters for /pipeline.
func (d *DB) MetricsSnapshot(ctx context.Context) (*MetricsSnapshotRow, error) {
	m := &MetricsSnapshotRow{States: map[string]int64{}}
	err := d.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE state='ready'),
		count(*) FILTER (WHERE state='parsing'),
		count(*) FILTER (WHERE state='failed'),
		count(*) FROM documents`).Scan(
		&m.DocsReady, &m.DocsParsing, &m.DocsFailed, &m.DocsTotal)
	if err != nil {
		return nil, err
	}
	err = d.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE total_shards IS NOT NULL AND shards_done >= total_shards),
		count(*) FILTER (WHERE total_shards IS NULL OR shards_done < total_shards)
		FROM documents WHERE state='parsing'`).Scan(&m.DocsAwaitingEmbed, &m.DocsParsingActive)
	if err != nil {
		return nil, err
	}
	err = d.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE state='done'),
		count(*) FILTER (WHERE state='pending'),
		count(*) FILTER (WHERE state='failed'),
		count(*) FILTER (WHERE state='running'),
		count(*) FROM shards`).Scan(
		&m.ShardsDone, &m.ShardsPending, &m.ShardsFailed, &m.ShardsRunning, &m.ShardsTotal)
	if err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT state, count(*) FROM documents GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		m.States[state] = n
	}
	return m, rows.Err()
}

// InFlightParseRow is one running shard with its doc title — the
// /pipeline in_flight_parse detail.
type InFlightParseRow struct {
	Title      string
	Idx        int
	PageStart  int
	PageEnd    int
	WorkerID   *string
	LeaseUntil *time.Time
}

// InFlightParse lists shards with a live lease (LIMIT 12 like 1.0).
func (d *DB) InFlightParse(ctx context.Context) ([]InFlightParseRow, error) {
	rows, err := d.Pool.Query(ctx, `SELECT d.title, s.idx, s.page_start, s.page_end,
		s.worker_id, s.lease_until
		FROM shards s JOIN documents d ON d.id = s.doc_id
		WHERE s.state='running' LIMIT 12`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InFlightParseRow
	for rows.Next() {
		var r InFlightParseRow
		if err := rows.Scan(&r.Title, &r.Idx, &r.PageStart, &r.PageEnd, &r.WorkerID, &r.LeaseUntil); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ShardRollupRow is one settled shard in the minute rollup window.
type ShardRollupRow struct {
	PageStart  int
	PageEnd    int
	DurationMS int
	PeakRSSMB  int
	State      string
}

// ShardsDoneBetween lists shards settled in [start, end) — the rollup window.
func (d *DB) ShardsDoneBetween(ctx context.Context, start, end time.Time) ([]ShardRollupRow, error) {
	rows, err := d.Pool.Query(ctx, `SELECT page_start, page_end, COALESCE(duration_ms,0),
		COALESCE(peak_rss_mb,0), state FROM shards
		WHERE done_at IS NOT NULL AND done_at >= $1 AND done_at < $2`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShardRollupRow
	for rows.Next() {
		var r ShardRollupRow
		if err := rows.Scan(&r.PageStart, &r.PageEnd, &r.DurationMS, &r.PeakRSSMB, &r.State); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertMetricsRollup writes one minute-bucket row (idempotent on the PK).
func (d *DB) UpsertMetricsRollup(ctx context.Context, bucket time.Time, pages, done, failed, embedded, p50, p95, rssP95 *int, queueDepth map[string]any) error {
	qd, err := json.Marshal(queueDepth)
	if err != nil {
		return err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO metrics_rollup
		(bucket, pages_parsed, shards_done, shards_failed, chunks_embedded,
		 parse_p50_ms, parse_p95_ms, peak_rss_p95_mb, queue_depth)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (bucket) DO UPDATE SET
		  pages_parsed = EXCLUDED.pages_parsed,
		  shards_done = EXCLUDED.shards_done,
		  shards_failed = EXCLUDED.shards_failed,
		  chunks_embedded = EXCLUDED.chunks_embedded,
		  parse_p50_ms = EXCLUDED.parse_p50_ms,
		  parse_p95_ms = EXCLUDED.parse_p95_ms,
		  peak_rss_p95_mb = EXCLUDED.peak_rss_p95_mb,
		  queue_depth = EXCLUDED.queue_depth`,
		bucket, pages, done, failed, embedded, p50, p95, rssP95, qd)
	return err
}

// RollupRow is one recent metrics_rollup row for /metrics.
type RollupRow struct {
	Bucket         time.Time
	PagesParsed    *int
	ShardsDone     *int
	ShardsFailed   *int
	ChunksEmbedded *int
	ParseP50MS     *int
	ParseP95MS     *int
	PeakRSSP95MB   *int
	QueueDepth     map[string]any
}

// LatestRollups returns the n most recent rollup rows.
func (d *DB) LatestRollups(ctx context.Context, n int) ([]RollupRow, error) {
	rows, err := d.Pool.Query(ctx, `SELECT bucket, pages_parsed, shards_done, shards_failed,
		chunks_embedded, parse_p50_ms, parse_p95_ms, peak_rss_p95_mb, queue_depth
		FROM metrics_rollup ORDER BY bucket DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RollupRow
	for rows.Next() {
		var r RollupRow
		var qd []byte
		if err := rows.Scan(&r.Bucket, &r.PagesParsed, &r.ShardsDone, &r.ShardsFailed,
			&r.ChunksEmbedded, &r.ParseP50MS, &r.ParseP95MS, &r.PeakRSSP95MB, &qd); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(qd, &r.QueueDepth)
		out = append(out, r)
	}
	return out, rows.Err()
}
