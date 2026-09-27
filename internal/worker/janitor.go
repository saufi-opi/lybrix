package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
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

// stats is one janitor pass's counters (logged + visible in metrics).
type stats struct {
	RequeuedLeases int64
	Escalated      int
	Reenqueued     int
	EmbedSwept     int
	Reclaimed      int
	Quarantined    int
	ParseRescued   int
	StuckWarned    int
	Rollup         int
}

// lastRollupBucket persists across passes within one process (1.0 module
// global _last_bucket).
var lastRollupBucket *time.Time

// Pass runs one sweep; returns counters for logging/metrics.
//
// R-36 structure: one SHORT TxWithRetry per step, and every piece of Redis
// I/O runs OUTSIDE any tx. The 1.0 "the pass is transactional" shape — one
// doc-wide tx holding shard/doc row locks across dozens of Redis round-
// trips — was the ideal deadlock counterparty for the parsers' standing
// shard locks (the 2026-09-27 storm). Step order is preserved exactly:
// reclaim BEFORE the sweeps that dedup against re-added entries
// (janitor.go comment below, R-1/R-6 semantics).
func (j *Janitor) Pass(ctx context.Context) (map[string]int, error) {
	now := time.Now()
	var st stats

	// 1. Reaper: expired running leases → pending (§6.6) — pool
	// autocommit, as today.
	requeued, err := j.DB.RequeueExpiredLeases(ctx)
	if err != nil {
		return nil, fmt.Errorf("reaper: %w", err)
	}
	st.RequeuedLeases = requeued

	// 2. escalate — one short tx: shard UPDATEs + batched counter bump +
	// event rows.
	err = j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		var txErr error
		st.Escalated, txErr = j.escalate(ctx, tx, now)
		return txErr
	})
	if err != nil {
		return nil, err
	}

	// 3. requeueSweep — reads the docs it needs first (pool), then does the
	// Redis scan + XADDs with NO tx open. Scan failure → skip, never
	// blind-add (R-1/R-6).
	docs, err := j.DB.NonTerminalDocs(ctx)
	if err != nil {
		return nil, err
	}
	st.Reenqueued, err = j.requeueSweep(ctx, docs)
	if err != nil {
		return nil, err
	}

	// 4. reclaim — the Redis loop (XAUTOCLAIM/XPENDING/XACK/XDEL/XADD) runs
	// with NO tx; quarantines are COLLECTED as descriptors and one short tx
	// afterwards writes all DLQ event rows. MUST run before the sweeps
	// below: re-added entries become undelivered, so the sweeps' dedup
	// scans see them.
	var quarantines []quarantineEvent
	st.Reclaimed, quarantines, err = j.reclaim(ctx)
	if err != nil {
		return nil, err
	}
	st.Quarantined = len(quarantines)
	if len(quarantines) > 0 {
		err = j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
			for _, q := range quarantines {
				if err := store.WriteEvent(ctx, tx, q.Level, q.Stage, q.Message,
					q.DocID, q.ShardIdx, q.Code, q.WorkerID, q.Context); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// 5. embedSweep — pool reads + Redis scan + XADDs outside any tx; the
	// rescue events land in one short tx. Scan failure → skip (R-1).
	st.EmbedSwept, err = j.embedSweep(ctx, docs)
	if err != nil {
		return nil, err
	}

	// 6. pendingShardSweep — same shape (R-32 rescue; runs after reclaim so
	// re-added PEL entries are visible to the dedup scan).
	st.ParseRescued, err = j.pendingShardSweep(ctx, now)
	if err != nil {
		return nil, err
	}

	// 7. stuckWarn — pool read + one short tx for the events.
	st.StuckWarned, err = j.stuckWarn(ctx, now)
	if err != nil {
		return nil, err
	}

	// 8. rollup — pool reads + queue-depth (Redis) outside any tx; the
	// upsert lands in a short tx.
	st.Rollup, err = j.rollup(ctx, now)
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
		"parse_rescued":   st.ParseRescued,
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
// skip the requeue entirely, never blind-add. R-36: no tx parameter — the
// XADDs always ran tx-side work only; the sweep never wrote PG.
func (j *Janitor) requeueSweep(ctx context.Context, docs []*store.Document) (int, error) {
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
//
// R-36: the whole loop runs with NO tx open. Quarantined entries are
// collected as quarantineEvent descriptors; the caller writes all DLQ
// event rows in one short tx afterwards. The XACK/XDEL (the point of the
// operation — dropping the entry from the PEL) still fires immediately, so
// the entry cannot be re-claimed while its event row waits.
func (j *Janitor) reclaim(ctx context.Context) (reclaimed int, quarantines []quarantineEvent, err error) {
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
			decided, q, err := reclaimDecide(ctx, j.Redis, stream, entry.ID, entry.RawJob, deliveryCap)
			if err != nil {
				return reclaimed, quarantines, err
			}
			switch decided {
			case reclaimQuarantined:
				quarantines = append(quarantines, q)
			case reclaimReadded:
				reclaimed++
			}
		}
	}
	return reclaimed, quarantines, nil
}

type reclaimDecision int

const (
	reclaimKept        reclaimDecision = iota // husk entry: acked+deleted only
	reclaimReadded                            // under cap: fresh copy re-added
	reclaimQuarantined                        // at/over cap: descriptor collected
)

// reclaimDecide is the per-entry reclaim decision — the seam the
// miniredis lane can drive directly (miniredis does not implement
// XAUTOCLAIM, so the loop's XAUTOCLAIM half is exercised in the
// testcontainers soak lane instead). Semantics unchanged from 1.0/R-8:
// empty job → XACK+XDEL the husk; delivery count at/over cap → quarantine
// descriptor (XACK+XDEL fires here); otherwise re-add fresh + XACK+XDEL.
// Delivery-count lookup is fail-open: a failed/empty XPENDING re-adds.
func reclaimDecide(ctx context.Context, r redis.Cmdable, stream, entryID, rawJob string,
	deliveryCap int) (reclaimDecision, quarantineEvent, error) {
	if rawJob == "" {
		_ = r.XAck(ctx, stream, queue.ConsumerGroup, entryID).Err()
		_ = r.XDel(ctx, stream, entryID).Err() // trim the husk too
		return reclaimKept, quarantineEvent{}, nil
	}
	// delivery count for THIS entry (fail-open: query trouble or an
	// empty answer → re-add, never quarantine on a guess)
	timesDelivered := -1
	pending, perr := r.XPendingExt(ctx, redisPendingExtArgsPtr(stream, entryID)).Result()
	if perr == nil && len(pending) > 0 {
		timesDelivered = int(pending[0].RetryCount)
	} else if perr != nil {
		slog.Warn("reclaim: XPENDING failed — capping disabled for this entry",
			"stream", stream, "entry", entryID, "err", perr)
	}
	if timesDelivered >= 0 && timesDelivered >= deliveryCap {
		q := quarantineEventOf(stream, entryID, rawJob, int64(timesDelivered))
		// XACK/XDEL fires with the collection — the entry cannot be
		// re-claimed while its event row waits.
		_ = r.XAck(ctx, stream, queue.ConsumerGroup, entryID).Err()
		_ = r.XDel(ctx, stream, entryID).Err()
		return reclaimQuarantined, q, nil
	}
	if _, err := r.XAdd(ctx, &redis.XAddArgs{
		Stream: stream, Values: map[string]any{"job": rawJob},
	}).Result(); err != nil {
		return reclaimKept, quarantineEvent{}, err
	}
	_ = r.XAck(ctx, stream, queue.ConsumerGroup, entryID).Err()
	_ = r.XDel(ctx, stream, entryID).Err() // old entry trimmed; fresh copy re-added
	return reclaimReadded, quarantineEvent{}, nil
}

// quarantineEvent is one DLQ record: the events-row fields for a quarantined
// stream entry, collected by reclaim (outside any tx) and written by the
// caller in one short tx.
type quarantineEvent struct {
	Level    string
	Stage    string
	Message  string
	DocID    *string
	ShardIdx *int
	Code     *string
	WorkerID *string
	Context  map[string]any
}

// quarantineEventOf builds the DLQ descriptor for one quarantined entry —
// the extraction half of the old quarantine (pure, testable).
func quarantineEventOf(stream, entryID, rawJob string, timesDelivered int64) quarantineEvent {
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
	return quarantineEvent{
		Level:    "error",
		Stage:    "dlq",
		Message:  fmt.Sprintf("job quarantined from %s after %d deliveries", stream, timesDelivered),
		DocID:    docID,
		ShardIdx: idx,
		Code:     strPtr("DLQ"),
		WorkerID: nil,
		Context:  detail,
	}
}

// embedSweep: settled-but-never-enqueued sweep — a parser that dies between
// the PG commit of the last shard and the Redis XADD loses the embed job
// forever (real incident 2026-09-10: 7 books). Re-adding is idempotent
// (chunk ON CONFLICT DO NOTHING), BUT the sweep runs every pass, so it must
// dedup against jobs already waiting on the stream (real incident
// 2026-09-11: no dedup + an unacked job ⇒ 3.2k dupes). Scan failure → skip
// (no blind re-add). R-36: Redis I/O outside any tx; rescue events land in
// one short tx after the XADDs.
func (j *Janitor) embedSweep(ctx context.Context, docs []*store.Document) (int, error) {
	queuedIDs, err := PendingJobDocIDs(ctx, j.Redis, queue.StreamEmbed)
	if err != nil {
		slog.Warn("embed sweep skipped: stream scan failed (no blind re-add)", "err", err)
		return 0, nil
	}
	n := 0
	var events []quarantineEvent
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
		events = append(events, quarantineEvent{
			Level:   "warn",
			Stage:   "janitor",
			Message: "settled book found without embed job — re-enqueued",
			DocID:   strPtr(doc.ID),
			Code:    strPtr("EMBED_RESCUE"),
			Context: map[string]any{},
		})
		n++
	}
	if len(events) > 0 {
		err = j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
			for _, ev := range events {
				if err := store.WriteEvent(ctx, tx, ev.Level, ev.Stage, ev.Message,
					ev.DocID, ev.ShardIdx, ev.Code, ev.WorkerID, ev.Context); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// shardSweepGrace is how long a doc's updated_at must be in the past before
// pendingShardSweep considers its shards. Not a correctness need — the
// per-shard dedup makes the sweep idempotent, and a duplicate that slips
// through races benignly (second ClaimShard matches 0 rows and ACKs) — but
// a noise filter: it keeps the sweep out of the split's commit→enqueue
// window and bounds events-row chatter.
const shardSweepGrace = 2 * time.Minute

// parseJobKeyOf is THE single parse-job dedup key derivation — both the
// Redis-payload side (parseJobKey) and the DB-row side (the sweep, from
// store.StalePendingShard) call this one helper, so a format change cannot
// desynchronize the two (an R-1-class blind-add hazard).
func parseJobKeyOf(docID string, idx int) string {
	return docID + "|" + strconv.Itoa(idx)
}

// parseJobKey extracts the dedup key from a parsed stream payload — the
// Redis-payload side of the sweep's key derivation. Unparseable payload or
// a missing field ⇒ ("", false) (same spirit as the old jobDocID).
func parseJobKey(payload map[string]any) (string, bool) {
	docID, ok := payload["doc_id"].(string)
	if !ok || docID == "" {
		return "", false
	}
	f, ok := payload["idx"].(float64) // JSON numbers decode as float64
	if !ok {
		return "", false
	}
	return parseJobKeyOf(docID, int(f)), true
}

// missingParseJobs is the rescue decision, pure: for each never-attempted
// shard, emit a ParseJob unless its dedup key already sits on doc.parse
// (undelivered or in the PEL). The sweep body is a thin wrapper around
// this — it is the only DB-free coverage of the decision.
func missingParseJobs(shards []store.StalePendingShard, queued map[string]bool) []queue.ParseJob {
	var out []queue.ParseJob
	for _, s := range shards {
		if queued[parseJobKeyOf(s.DocID, s.Idx)] {
			continue // a parse job is already waiting — never double-add
		}
		out = append(out, queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         s.DocID,
			Idx:           s.Idx,
			PageStart:     s.PageStart,
			PageEnd:       s.PageEnd,
			SourceURI:     s.SourceURI,
		})
	}
	return out
}

// pendingShardSweep is the 8th janitor step (R-32): shards stranded
// state='pending' attempts=0 under non-terminal docs — a parser that
// destroyed the front of doc.parse before the split tx committed (the R-32
// race), a crash between the split commit and the XADDs, or the parser
// ladder's resplit variant — get their ParseJobs re-enqueued. Deduped per
// (doc_id, idx) against undelivered + PEL entries; a Redis scan failure
// skips the sweep entirely (fail-open, never blind re-add — the
// 2026-09-11 doc.embed flood is the regression this forbids). Runs after
// reclaim, so re-added PEL entries are visible to the dedup scan.
func (j *Janitor) pendingShardSweep(ctx context.Context, now time.Time) (int, error) {
	shards, err := j.DB.NeverAttemptedShards(ctx, now.Add(-shardSweepGrace))
	if err != nil {
		return 0, fmt.Errorf("never-attempted shard query failed: %w", err)
	}
	if len(shards) == 0 {
		return 0, nil
	}
	queued, err := queue.PendingJobKeys(ctx, j.Redis, queue.StreamParse, parseJobKey)
	if err != nil {
		slog.Warn("pendingShardSweep skipped: doc.parse scan failed (no blind re-add)", "err", err)
		return 0, nil
	}
	jobs := missingParseJobs(shards, queued)
	if len(jobs) == 0 {
		return 0, nil
	}
	rescuedByDoc := map[string]bool{}
	for _, job := range jobs {
		if _, err := queue.XAddJob(ctx, j.Redis, queue.StreamParse, job); err != nil {
			return 0, err
		}
		rescuedByDoc[job.DocID] = true
	}
	// Rescue events in one short tx, after the XADDs (R-36: no Redis I/O
	// inside a tx).
	err = j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		for docID := range rescuedByDoc {
			if err := store.WriteEvent(ctx, tx, "warn", "janitor",
				fmt.Sprintf("%d never-attempted shard(s) re-enqueued — R-32 rescue", countJobs(jobs, docID)),
				strPtr(docID), nil, strPtr("PARSE_RESCUE"), nil, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return len(jobs), err
	}
	return len(jobs), nil
}

// countJobs counts the seam's jobs belonging to one doc (events rows are
// per affected doc, not per shard).
func countJobs(jobs []queue.ParseJob, docID string) int {
	n := 0
	for _, j := range jobs {
		if j.DocID == docID {
			n++
		}
	}
	return n
}

// stuckWarn: stuck-document warning (§6.6) — informational only. R-36:
// pool read + one short tx for the events, no Redis I/O at all.
func (j *Janitor) stuckWarn(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-time.Duration(j.Settings.StuckMinutes) * time.Minute)
	docs, err := j.DB.StuckDocs(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		return 0, nil
	}
	err = j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		for _, doc := range docs {
			if err := store.WriteEvent(ctx, tx, "warn", "janitor",
				fmt.Sprintf("document non-terminal for over %d min (state=%s)", j.Settings.StuckMinutes, doc.State),
				strPtr(doc.ID), nil, strPtr("STUCK_DOCUMENT"), nil, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return len(docs), err
	}
	return len(docs), nil
}

// rollup: metrics rollup (PRD §10.1) — one row per minute bucket from real
// data: per-minute deltas from the previous bucket's snapshot + windowed
// p50/p95 from shards.done_at. R-36: pool reads + queue-depth (Redis)
// outside any tx; UpsertMetricsRollup lands in a short tx. A Redis read
// failure is caught inside and never aborts the pass.
func (j *Janitor) rollup(ctx context.Context, now time.Time) (int, error) {
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
	if err := j.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		return j.DB.UpsertMetricsRollup(ctx, bucket, &pages, &done, &failed, &chunksEmbedded, p50, p95, rssP95, qd)
	}); err != nil {
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

// PendingJobDocIDs returns doc_ids holding an UNDELIVERED job on stream —
// a thin wrapper over queue.PendingJobKeys with the doc_id extractor, so
// there is exactly one scan implementation (R-1/R-6 semantics preserved
// verbatim, including "error ⇒ caller MUST skip, never blind-add").
//
// Ordering note: queue.PendingJobKeys sees the PEL, which includes entries
// re-added by the janitor's own reclaim step. Pass's real order is
// requeueSweep BEFORE reclaim — so the split requeue dedup scan does NOT
// include entries re-added by this pass's reclaim (they land after it);
// only the sweeps that run after reclaim (embedSweep, pendingShardSweep)
// see those re-added entries in their dedup sets.
func PendingJobDocIDs(ctx context.Context, r redis.Cmdable, stream string) (map[string]bool, error) {
	return queue.PendingJobKeys(ctx, r, stream, func(payload map[string]any) (string, bool) {
		s, ok := payload["doc_id"].(string)
		if !ok || s == "" {
			return "", false
		}
		return s, true
	})
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
