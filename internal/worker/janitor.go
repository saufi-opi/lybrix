package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Janitor — continuous reaper/requeue/reclaim/escalate/stuck/rollup
// (PRD §6.6). Runs every JANITOR_INTERVAL_S. This is the component that
// makes `docker kill` on a parser a non-event: expired leases go back to
// pending within one pass.
type Janitor struct {
	DB       *store.DB
	Redis    redis.Cmdable
	Settings Settings
}

// Settings is the janitor-relevant config subset.
type Settings struct {
	JanitorInterval  time.Duration
	StuckMinutes     int
	MaxShardAttempts int
	ShardLeaseHint   int
}

// scanPage is the XRANGE page size for stream scans (janitor.py SCAN_PAGE).
const scanPage = 500

// stats is one janitor pass's counters (logged + visible in metrics).
type stats struct {
	RequeuedLeases int64
	Escalated      int
	Reenqueued     int
	EmbedSwept     int
	Reclaimed      int
	Quarantined    int
	StuckWarned    int
	Rollup         int
}

// lastRollupBucket persists across passes within one process (1.0 module
// global _last_bucket).
var lastRollupBucket *time.Time

// Pass runs one sweep; returns counters for logging/metrics.
func (j *Janitor) Pass(ctx context.Context) (map[string]int, error) {
	now := time.Now()
	var st stats

	// 1. Reaper: expired running leases → pending (§6.6)
	requeued, err := j.DB.RequeueExpiredLeases(ctx)
	if err != nil {
		return nil, fmt.Errorf("reaper: %w", err)
	}
	st.RequeuedLeases = requeued

	// 2-6. Everything else in one tx (like 1.0: the pass is transactional).
	err = j.DB.Tx(ctx, func(tx pgx.Tx) error {
		var txErr error
		st.Escalated, txErr = j.escalate(ctx, tx, now)
		if txErr != nil {
			return txErr
		}
		docs, txErr := j.DB.NonTerminalDocs(ctx)
		if txErr != nil {
			return txErr
		}
		st.Reenqueued, txErr = j.requeueSweep(ctx, tx, docs)
		if txErr != nil {
			return txErr
		}
		st.Reclaimed, st.Quarantined, txErr = j.reclaim(ctx, tx)
		if txErr != nil {
			return txErr
		}
		st.EmbedSwept, txErr = j.embedSweep(ctx, tx, docs)
		if txErr != nil {
			return txErr
		}
		st.StuckWarned, txErr = j.stuckWarn(ctx, tx, now)
		if txErr != nil {
			return txErr
		}
		st.Rollup, txErr = j.rollup(ctx, tx, now)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"requeued_leases": int(st.RequeuedLeases),
		"escalated":       st.Escalated,
		"reenqueued":      st.Reenqueued,
		"embed_swept":     st.EmbedSwept,
		"reclaimed":       st.Reclaimed,
		"quarantined":     st.Quarantined,
		"stuck_warned":    st.StuckWarned,
		"rollup":          st.Rollup,
	}, nil
}

// escalate: shards with attempts >= max → failed + event (§6.6).
func (j *Janitor) escalate(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	rows, err := j.DB.EscalateStuckShards(ctx, tx, j.Settings.MaxShardAttempts)
	if err != nil {
		return 0, err
	}
	for _, e := range rows {
		_ = store.WriteEvent(ctx, tx, "error", "parse",
			fmt.Sprintf("shard escalated to failed after %d attempts", e.Attempts),
			strPtr(e.DocID), intPtr(e.Idx), strPtr("SHARD_TIMEOUT"), nil, nil)
	}
	return len(rows), nil
}

// requeue: non-terminal docs with no queue entry (Redis loss, §6.6).
// Dedup against undelivered split jobs first (R-6): an UPLOADED doc whose
// split job sits unacked must not be re-XADDed every pass. Scan failure →
// skip the requeue entirely, never blind-add.
func (j *Janitor) requeueSweep(ctx context.Context, tx pgx.Tx, docs []*store.Document) (int, error) {
	queuedSplitIDs, err := PendingJobDocIDs(ctx, j.Redis, queue.StreamSplit)
	if err != nil {
		slog.Warn("doc.split requeue skipped: stream scan failed (no blind re-add)", "err", err)
		return 0, nil
	}
	n := 0
	for _, doc := range docs {
		if doc.State != store.StateUploaded {
			continue
		}
		if queuedSplitIDs[doc.ID] {
			continue // a split job is already waiting — never double-add
		}
		job := queue.SplitJob{SchemaVersion: queue.SchemaVersion, DocID: doc.ID, SourceURI: doc.SourceURI}
		if _, err := queue.XAddJob(ctx, j.Redis, queue.StreamSplit, job); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reclaim: PEL entries stranded by dead consumers. XAUTOCLAIM them, re-add
// as fresh stream entries, ack the old one. PG-lease idempotency makes
// duplicates safe; min_idle must exceed the longest legit job
// (SHARD_LEASE_SECONDS=900) so live parses are never double-claimed.
// MUST run before the settled-book sweep: re-added entries become
// undelivered, so the sweep's dedup scan sees them.
//
// Delivery cap (R-8): entries at/over DELIVERY_CAP are quarantined (DLQ
// events row + XACK/XDEL) instead of re-added. The cap read is fail-open:
// if the XPENDING lookup fails or returns nothing, the entry re-adds as
// before — quarantine only ever fires on a real delivery count.
func (j *Janitor) reclaim(ctx context.Context, tx pgx.Tx) (reclaimed, quarantined int, err error) {
	deliveryCap := j.Settings.MaxShardAttempts + 1
	if deliveryCap < 5 {
		deliveryCap = 5
	}
	for _, stream := range queue.AllStreams {
		entries, err := queue.ClaimStale(ctx, j.Redis, stream, "janitor-reclaim", 20*60*1000, 50)
		if err != nil {
			continue // per-stream tolerance, like 1.0
		}
		for _, entry := range entries {
			job := entry.RawJob
			if job == "" {
				_ = j.Redis.XAck(ctx, stream, queue.ConsumerGroup, entry.ID).Err()
				_ = j.Redis.XDel(ctx, stream, entry.ID).Err() // trim the husk too
				continue
			}
			// delivery count for THIS entry (fail-open: query trouble or an
			// empty answer → re-add, never quarantine on a guess)
			timesDelivered := -1
			pending, perr := j.Redis.XPendingExt(ctx, redisPendingExtArgsPtr(stream, entry.ID)).Result()
			if perr == nil && len(pending) > 0 {
				timesDelivered = int(pending[0].RetryCount)
			} else if perr != nil {
				slog.Warn("reclaim: XPENDING failed — capping disabled for this entry",
					"stream", stream, "entry", entry.ID, "err", perr)
			}
			if timesDelivered >= 0 && timesDelivered >= deliveryCap {
				j.quarantine(ctx, tx, stream, entry.ID, job, int64(timesDelivered))
				quarantined++
				continue
			}
			if _, err := j.Redis.XAdd(ctx, &redis.XAddArgs{
				Stream: stream, Values: map[string]any{"job": job},
			}).Result(); err != nil {
				return reclaimed, quarantined, err
			}
			_ = j.Redis.XAck(ctx, stream, queue.ConsumerGroup, entry.ID).Err()
			_ = j.Redis.XDel(ctx, stream, entry.ID).Err() // old entry trimmed; fresh copy re-added
			reclaimed++
		}
	}
	return reclaimed, quarantined, nil
}

// quarantine writes one DLQ events row, then drops the entry (both Redis
// calls best-effort — dropping the stream entry is the point, the events
// row is the durable record).
func (j *Janitor) quarantine(ctx context.Context, tx pgx.Tx, stream, entryID, rawJob string, timesDelivered int64) {
	var docID *string
	var idx *int
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJob), &payload); err == nil && payload != nil {
		if s, ok := payload["doc_id"].(string); ok && s != "" {
			docID = &s
		}
		if f, ok := payload["idx"].(float64); ok {
			i := int(f)
			idx = &i
		}
	}
	detail := map[string]any{"detail": rawJob}
	_ = store.WriteEvent(ctx, tx, "error", "dlq",
		fmt.Sprintf("job quarantined from %s after %d deliveries", stream, timesDelivered),
		docID, idx, strPtr("DLQ"), nil, detail)
	_ = j.Redis.XAck(ctx, stream, queue.ConsumerGroup, entryID).Err()
	_ = j.Redis.XDel(ctx, stream, entryID).Err()
}

// embedSweep: settled-but-never-enqueued sweep — a parser that dies between
// the PG commit of the last shard and the Redis XADD loses the embed job
// forever (real incident 2026-09-10: 7 books). Re-adding is idempotent
// (chunk ON CONFLICT DO NOTHING), BUT the sweep runs every pass, so it must
// dedup against jobs already waiting on the stream (real incident
// 2026-09-11: no dedup + an unacked job ⇒ 3.2k dupes). Scan failure → skip
// (no blind re-add).
func (j *Janitor) embedSweep(ctx context.Context, tx pgx.Tx, docs []*store.Document) (int, error) {
	queuedIDs, err := PendingJobDocIDs(ctx, j.Redis, queue.StreamEmbed)
	if err != nil {
		slog.Warn("embed sweep skipped: stream scan failed (no blind re-add)", "err", err)
		return 0, nil
	}
	n := 0
	for _, doc := range docs {
		if doc.State != store.StateParsing {
			continue
		}
		if !store.BookSettled(doc) {
			continue
		}
		if queuedIDs[doc.ID] {
			continue // an embed job is already waiting — never double-add
		}
		job := queue.EmbedJob{SchemaVersion: queue.SchemaVersion, DocID: doc.ID}
		if _, err := queue.XAddJob(ctx, j.Redis, queue.StreamEmbed, job); err != nil {
			return n, err
		}
		_ = store.WriteEvent(ctx, tx, "warn", "janitor",
			"settled book found without embed job — re-enqueued",
			strPtr(doc.ID), nil, strPtr("EMBED_RESCUE"), nil, nil)
		n++
	}
	return n, nil
}

// stuckWarn: stuck-document warning (§6.6) — informational only.
func (j *Janitor) stuckWarn(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	cutoff := now.Add(-time.Duration(j.Settings.StuckMinutes) * time.Minute)
	docs, err := j.DB.StuckDocs(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, doc := range docs {
		_ = store.WriteEvent(ctx, tx, "warn", "janitor",
			fmt.Sprintf("document non-terminal for over %d min (state=%s)", j.Settings.StuckMinutes, doc.State),
			strPtr(doc.ID), nil, strPtr("STUCK_DOCUMENT"), nil, nil)
	}
	return len(docs), nil
}

// rollup: metrics rollup (PRD §10.1) — one row per minute bucket from real
// data: per-minute deltas from the previous bucket's snapshot + windowed
// p50/p95 from shards.done_at. Runs inside the pass transaction; a Redis
// read failure is caught inside and never aborts the pass.
func (j *Janitor) rollup(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	bucket := now.Truncate(time.Minute)
	if lastRollupBucket != nil && !bucket.After(*lastRollupBucket) {
		return 0, nil
	}
	windowStart := bucket.Add(-time.Minute)
	rows, err := j.DB.ShardsDoneBetween(ctx, windowStart, bucket)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 && lastRollupBucket != nil && bucket.Equal(*lastRollupBucket) {
		return 0, nil
	}
	durations := make([]int, 0, len(rows))
	rss := make([]int, 0, len(rows))
	pages := 0
	failed := 0
	for _, r := range rows {
		// latency percentiles are a DONE-shard metric — failed shards carry
		// no real duration and would skew p50/p95 toward 0
		if r.State == "done" {
			durations = append(durations, r.DurationMS)
			rss = append(rss, r.PeakRSSMB)
		}
		pages += r.PageEnd - r.PageStart + 1
		if r.State == "failed" {
			failed++
		}
	}
	qd := map[string]any{}
	for _, name := range queue.AllStreams {
		depth, err := queue.QueueDepth(ctx, j.Redis, name)
		if err != nil {
			slog.Warn("rollup: queue_depth read failed", "stream", name, "err", err)
			continue
		}
		qd[name] = depth
	}
	chunksEmbedded := 0
	if n, err := j.DB.EmbeddedChunksSince(ctx, windowStart, bucket); err == nil {
		chunksEmbedded = n
	} else {
		slog.Warn("rollup: chunks_embedded count failed", "err", err)
	}
	done := len(rows)
	p50 := pct(durations, 50)
	p95 := pct(durations, 95)
	rssP95 := pct(rss, 95)
	if err := j.DB.UpsertMetricsRollup(ctx, bucket, &pages, &done, &failed, &chunksEmbedded, p50, p95, rssP95, qd); err != nil {
		return 0, err
	}
	b := bucket
	lastRollupBucket = &b
	return 1, nil
}

// pct is the janitor's pct() from shards.done_at (nearest-rank).
func pct(sorted []int, p int) *int {
	if len(sorted) == 0 {
		return nil
	}
	sort.Ints(sorted)
	i := (len(sorted) - 1) * p / 100
	if i < 0 {
		i = 0
	}
	v := sorted[i]
	return &v
}

// PendingJobDocIDs returns doc_ids holding an UNDELIVERED job on stream.
//
// Precise semantics (stream is never trimmed, so a full XRANGE would see
// every job ever ACKed and wrongly suppress future re-enqueues):
//  1. everything after the group's last-delivered-id (undelivered), plus
//  2. the PEL (delivered-but-unacked) — claimed by a live worker or
//     waiting for the reclaim step, which runs BEFORE the requeue sweeps
//     in every pass.
//
// Returns an error if the scan itself fails — the caller MUST skip the
// sweep rather than blind-add (blind re-adding is exactly how the
// 2026-09-11 doc.embed flood happened: ~3.2k duplicate jobs).
func PendingJobDocIDs(ctx context.Context, r redis.Cmdable, stream string) (map[string]bool, error) {
	groups, err := r.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return nil, err
	}
	var lastDelivered string
	found := false
	for _, g := range groups {
		if g.Name == queue.ConsumerGroup {
			lastDelivered = g.LastDeliveredID
			found = true
			break
		}
	}
	ids := map[string]bool{}
	if !found {
		return ids, nil
	}
	if lastDelivered == "" {
		lastDelivered = "0-0"
	}
	start := "(" + lastDelivered // exclusive: entries AT it are delivered
	for {
		page, err := r.XRange(ctx, stream, start, "+").Result()
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, msg := range page {
			if docID := jobDocID(msg.Values); docID != "" {
				ids[docID] = true
			}
		}
		if len(page) < scanPage {
			break
		}
		start = "(" + page[len(page)-1].ID
	}
	// PEL: delivered but unacked (live claim or awaiting reclaim)
	pel, err := r.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream, Group: queue.ConsumerGroup, Start: "-", End: "+", Count: scanPage,
	}).Result()
	if err != nil {
		return nil, err
	}
	for _, p := range pel {
		msgs, err := r.XRange(ctx, stream, p.ID, p.ID).Result()
		if err != nil {
			continue
		}
		for _, msg := range msgs {
			if docID := jobDocID(msg.Values); docID != "" {
				ids[docID] = true
			}
		}
	}
	return ids, nil
}

func jobDocID(values map[string]any) string {
	raw, _ := values["job"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	s, _ := payload["doc_id"].(string)
	return s
}

// Run loops the janitor pass at JANITOR_INTERVAL_S.
func (j *Janitor) Run(ctx context.Context) error {
	slog.Info("janitor starting", "interval", j.Settings.JanitorInterval)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		st, err := j.Pass(ctx)
		if err != nil {
			slog.Error("janitor pass failed", "err", err)
		} else if anyTrue(st) {
			slog.Info("janitor pass", "stats", st)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(j.Settings.JanitorInterval):
		}
	}
}

func anyTrue(m map[string]int) bool {
	for _, v := range m {
		if v > 0 {
			return true
		}
	}
	return false
}

var _ = errors.NewPlatformError
