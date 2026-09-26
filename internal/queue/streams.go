// Redis Streams producer/consumer-group primitives (PRD §4.2).
//
// Each pipeline arrow is a Redis Stream with a consumer group. Job state
// of record lives in Postgres, not Redis — if Redis is wiped, the janitor
// re-enqueues everything not in a terminal state (§6.6 Requeue).
package queue

import (
	"context"
	"strings"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stream names, one per pipeline arrow.
const (
	StreamSplit = "doc.split"
	StreamParse = "doc.parse"
	StreamEmbed = "doc.embed"
)

// AllStreams is the canonical stream order (janitor walks it).
var AllStreams = []string{StreamSplit, StreamParse, StreamEmbed}

// ConsumerGroup is the single group name every worker shares.
const ConsumerGroup = "rag-workers"

// NewClient builds the process's Redis client. Socket timeout MUST exceed
// the blocking XREADGROUP block (5s): a deadline at exactly the block
// duration races the server's empty reply and raises a spurious timeout
// every idle poll (1.0 measured this at redis-py; same class of race here).
func NewClient(url string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:    url, // redis://host:port/db parsed by go-redis ParseURL below
		Network: "",
	})
}

// ParseURL builds options from a redis:// URL with sane timeouts.
func ParseURL(url string) (*redis.Options, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opts.ReadTimeout = 15 * time.Second
	return opts, nil
}

// MustRedis parses and connects; a bad URL is a programmer error.
func MustRedis(url string) *redis.Client {
	opts, err := ParseURL(url)
	if err != nil {
		panic(fmt.Sprintf("bad REDIS_URL %q: %v", url, err))
	}
	return redis.NewClient(opts)
}

// EnsureStreams idempotently creates streams + consumer group.
//
// MKSTREAM so a group can exist on an (as-yet) empty stream; the janitor
// relies on groups existing even before the first XADD.
func EnsureStreams(ctx context.Context, r redis.Cmdable, streams ...string) error {
	if len(streams) == 0 {
		streams = AllStreams
	}
	for _, name := range streams {
		err := r.XGroupCreateMkStream(ctx, name, ConsumerGroup, "0").Err()
		if err != nil && !isBusyGroup(err) {
			return err
		}
	}
	return nil
}

func isBusyGroup(err error) bool {
	return err != nil && containsFold(err.Error(), "BUSYGROUP")
}

func containsFold(s, sub string) bool {
	n := len(s)
	m := len(sub)
	if m == 0 {
		return true
	}
	for i := 0; i+m <= n; i++ {
		match := true
		for j := 0; j < m; j++ {
			a, b := s[i+j], sub[j]
			if a >= 'a' && a <= 'z' {
				a -= 32
			}
			if b >= 'a' && b <= 'z' {
				b -= 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// XAddJob XADDs a job payload as a single JSON field — the wire shape is
// {"job": "<json>"} with schema_version inside the JSON.
func XAddJob(ctx context.Context, r redis.Cmdable, stream string, payload any) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return r.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]any{"job": string(b)},
	}).Result()
}

// ReadJobs reads up to count jobs for a consumer; returns (entry_id, job-dict).
//
// An unparseable job field is a dead entry: ACK + XDEL so it never
// resurfaces via reclaim.
func ReadJobs(ctx context.Context, r redis.Cmdable, stream, consumer string, count int, blockMs int64) ([]StreamEntry, error) {
	groups, err := r.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    int64(count),
		Block:    time.Duration(blockMs) * time.Millisecond,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []StreamEntry{}
	for _, g := range groups {
		for _, msg := range g.Messages {
			raw, _ := msg.Values["job"].(string)
			var job map[string]any
			if err := json.Unmarshal([]byte(raw), &job); err != nil {
				slog.Error("unparseable job", "stream", stream, "entry", msg.ID)
				// dead entry: ACK + XDEL so it never resurfaces via reclaim
				_ = r.XAck(ctx, stream, ConsumerGroup, msg.ID).Err()
				if derr := r.XDel(ctx, stream, msg.ID).Err(); derr != nil {
					slog.Warn("read_jobs: XDEL failed", "stream", stream, "entry", msg.ID, "err", derr)
				}
				continue
			}
			out = append(out, StreamEntry{ID: msg.ID, Job: job, RawJob: raw})
		}
	}
	return out, nil
}

// StreamEntry is one delivered stream message with its parsed job payload.
type StreamEntry struct {
	ID     string
	Job    map[string]any
	RawJob string
}

// Ack ACKs a processed job and XDELs it from the stream (trim-on-ACK).
//
// Streams are never trimmed automatically, so every processed job would
// otherwise live in the stream forever — 2026-09-12: doc.parse reached
// 25,073 entries for 15,994 real shards (36% stale duplicates) because the
// janitor reclaim re-adds ACKed PEL entries, growing the stream in a loop.
// XDEL is best-effort: a failed delete must never fail a job that just
// completed.
func Ack(ctx context.Context, r redis.Cmdable, stream, entryID string) {
	_ = r.XAck(ctx, stream, ConsumerGroup, entryID).Err()
	if err := r.XDel(ctx, stream, entryID).Err(); err != nil {
		slog.Warn("ack: XDEL failed (best-effort)", "stream", stream, "entry", entryID, "err", err)
	}
}

// ClaimStale XAUTOCLAIMs entries idle beyond minIdleMS — the Redis-side half
// of crash recovery; the Postgres lease (shards.lease_until) is the other.
func ClaimStale(ctx context.Context, r redis.Cmdable, stream, consumer string, minIdleMS int64, count int64) ([]StreamEntry, error) {
	res, _, err := r.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    ConsumerGroup,
		Consumer: consumer,
		MinIdle:  time.Duration(minIdleMS) * time.Millisecond,
		Start:    "0-0",
		Count:    count,
	}).Result()
	if err != nil {
		return nil, err
	}
	out := []StreamEntry{}
	for _, msg := range res {
		raw, _ := msg.Values["job"].(string)
		var job map[string]any
		if err := json.Unmarshal([]byte(raw), &job); err != nil {
			_ = r.XAck(ctx, stream, ConsumerGroup, msg.ID).Err()
			continue
		}
		out = append(out, StreamEntry{ID: msg.ID, Job: job, RawJob: raw})
	}
	return out, nil
}

// UndeliveredCount returns entries the group has never seen (Redis "lag")
// with a NULL fallback.
//
// XINFO GROUPS lag becomes nil once the group's bookkeeping is
// untrustworthy — most commonly after XDEL/XTRIM removed entries that the
// counters already accounted (2026-09-12: cleaning duplicate embed jobs
// left doc.embed lag=nil and the dashboard showed waiting=1 for a 73-job
// backlog). Fast path returns the int directly; on nil we count the
// undelivered tail with an exclusive XRANGE scan after the group's
// last-delivered-id (pages of 500, same cursor semantics as the janitor
// dedup scan). No group at all → 0.
func UndeliveredCount(ctx context.Context, r redis.Cmdable, stream string) (int64, error) {
	groups, err := r.XInfoGroups(ctx, stream).Result()
	if err != nil {
		if strings.Contains(err.Error(), "no such key") {
			return 0, nil
		}
		return 0, err
	}
	for _, g := range groups {
		if g.Name != ConsumerGroup {
			continue
		}
		if g.Lag >= 0 {
			return g.Lag, nil
		}
		cursor := g.LastDeliveredID
		if cursor == "" {
			cursor = "0-0"
		}
		return scanUndeliveredTail(ctx, r, stream, cursor)
	}
	return 0, nil // no group → nothing is undelivered for this group
}

// QueueDepth returns undelivered + pending entries — the number backpressure
// cares about.
//
// XLEN counts EVERY entry ever written to the stream (nothing trims it), so
// on a long-lived stream it only grows and eventually trips the backpressure
// cap even when the queue is actually empty. The real backlog is undelivered
// entries (group lag, with a live-count fallback when Redis reports nil) plus
// pending (delivered but unacked / PEL).
func QueueDepth(ctx context.Context, r redis.Cmdable, stream string) (int64, error) {
	pending, err := r.XPending(ctx, stream, ConsumerGroup).Result()
	if err != nil {
		pending = nil // a stream without a group reads as zero pending
	}
	var pcount int64
	if pending != nil {
		pcount = pending.Count
	}
	undelivered, err := UndeliveredCount(ctx, r, stream)
	if err != nil {
		return 0, err
	}
	return undelivered + pcount, nil
}

// Quarantine quarantines a poison job: one DLQ events row, then drop the entry.
//
// The terminal half of the janitor reclaim path (R-8). A job whose handler
// always raises (e.g. a doc deleted mid-parse) is otherwise redelivered
// forever: deliver → raise → unacked in the PEL → XAUTOCLAIM → re-add →
// deliver … The reclaim re-add also resets the entry, so nothing bounds the
// loop. Once the caller decides an entry is over the delivery cap, this
// writes one events row (level=error, stage=dlq, code=DLQ) carrying the raw
// job JSON, then ACKs and deletes the entry — both Redis calls best-effort,
// never raising. docID/shardIdx are parsed from the raw job when possible;
// unparseable jobs are still quarantined.
func Quarantine(ctx context.Context, r redis.Cmdable, stream, entryID, rawJob string, timesDelivered int64, lastError string, writeEvent func(docID, shardIdx any, detail map[string]any)) {
	var docID, shardIdx any
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJob), &payload); err == nil && payload != nil {
		if v, ok := payload["doc_id"].(string); ok && v != "" {
			docID = v
		}
		if f, ok := payload["idx"].(float64); ok {
			shardIdx = int(f)
		}
	}
	detail := map[string]any{"detail": rawJob}
	if lastError != "" {
		detail["last_error"] = lastError
	}
	writeEvent(docID, shardIdx, detail)
	_ = r.XAck(ctx, stream, ConsumerGroup, entryID).Err()
	_ = r.XDel(ctx, stream, entryID).Err()
}

// NewConsumerName returns "<prefix>-<8 random hex>" — consumer identity per
// process start, so recreated containers never inherit a stale PEL view.
func NewConsumerName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, randomHex8())
}

// scanUndeliveredTail counts the undelivered tail with an exclusive XRANGE
// scan after the group's last-delivered-id (pages of 500, same cursor
// semantics as the janitor dedup scan) — the lag=None fallback body.
func scanUndeliveredTail(ctx context.Context, r redis.Cmdable, stream, lastDeliveredID string) (int64, error) {
	start := "(" + lastDeliveredID // exclusive: entries AT the cursor are delivered
	var total int64
	for {
		page, err := r.XRange(ctx, stream, start, "+").Result()
		if err != nil {
			return total, err
		}
		total += int64(len(page))
		if len(page) < 500 {
			return total, nil
		}
		start = "(" + page[len(page)-1].ID
	}
}

// PendingJobKeys returns the set of dedup keys holding an UNDELIVERED job
// on stream — the caller-supplied-keyExtractor variant of the janitor
// dedup scan.
//
// Precise semantics (the stream is never trimmed, so a full XRANGE would
// see every job ever ACKed and wrongly suppress future re-enqueues):
//  1. everything after the group's last-delivered-id (undelivered), plus
//  2. the PEL (delivered-but-unacked) — claimed by a live worker or
//     waiting for the reclaim step.
//
// keyOf is applied to each parsed job payload; an unparseable payload or a
// keyOf returning false contributes nothing. Returns an error if the scan
// itself fails — the caller MUST skip the sweep rather than blind-add
// (blind re-adding is exactly how the 2026-09-11 doc.embed flood happened:
// ~3.2k duplicate jobs).
func PendingJobKeys(ctx context.Context, r redis.Cmdable, stream string, keyOf func(payload map[string]any) (string, bool)) (map[string]bool, error) {
	groups, err := r.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return nil, err
	}
	var lastDelivered string
	found := false
	for _, g := range groups {
		if g.Name == ConsumerGroup {
			lastDelivered = g.LastDeliveredID
			found = true
			break
		}
	}
	keys := map[string]bool{}
	if !found {
		return keys, nil
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
			if key, ok := payloadKeyOf(msg.Values, keyOf); ok {
				keys[key] = true
			}
		}
		if len(page) < scanPageKeys {
			break
		}
		start = "(" + page[len(page)-1].ID
	}
	// PEL: delivered but unacked (live claim or awaiting reclaim)
	pel, err := r.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream, Group: ConsumerGroup, Start: "-", End: "+", Count: scanPageKeys,
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
			if key, ok := payloadKeyOf(msg.Values, keyOf); ok {
				keys[key] = true
			}
		}
	}
	return keys, nil
}

// payloadKeyOf unwraps the stream's {"job": "<json>"} envelope and applies
// keyOf to the parsed payload.
func payloadKeyOf(values map[string]any, keyOf func(payload map[string]any) (string, bool)) (string, bool) {
	raw, _ := values["job"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload == nil {
		return "", false
	}
	return keyOf(payload)
}

// scanPageKeys is the XRANGE page size for the dedup scan. Kept in the
// queue package so the scan machinery here stays self-contained (the
// janitor's scanPage constant is the same value).
const scanPageKeys = 500
