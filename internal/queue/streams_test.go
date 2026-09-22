package queue

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (redis.Cmdable, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return r, mr
}

func TestEnsureStreamsIdempotent(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	if err := EnsureStreams(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStreams(ctx, r); err != nil {
		t.Fatalf("second ensure must be a no-op: %v", err)
	}
}

func TestXAddReadAckXdel(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	if err := EnsureStreams(ctx, r, StreamParse); err != nil {
		t.Fatal(err)
	}
	job := ParseJob{SchemaVersion: 1, DocID: "d1", Idx: 0, PageStart: 1, PageEnd: 20, SourceURI: "s3://raw/d1.pdf"}
	if _, err := XAddJob(ctx, r, StreamParse, job); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadJobs(ctx, r, StreamParse, "c1", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Job["doc_id"] != "d1" {
		t.Fatalf("read drift: %+v", entries)
	}
	// ack must ACK + XDEL (trim-on-ack)
	Ack(ctx, r, StreamParse, entries[0].ID)
	if n := r.XLen(ctx, StreamParse).Val(); n != 0 {
		t.Fatalf("XDEL-on-ack broken: XLen=%d", n)
	}
}

func TestUnparseableJobACKedAndDeleted(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	r.XAdd(ctx, &redis.XAddArgs{Stream: StreamParse, Values: map[string]any{"job": "{not json"}})
	entries, err := ReadJobs(ctx, r, StreamParse, "c1", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dead entry must not surface: %+v", entries)
	}
	if r.XLen(ctx, StreamParse).Val() != 0 {
		t.Fatal("dead entry must be XDELed")
	}
}

func TestUndeliveredCountFastPath(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	for i := 0; i < 600; i++ { // > scan page 500 to exercise pagination
		if _, err := XAddJob(ctx, r, StreamParse, ParseJob{SchemaVersion: 1, DocID: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := UndeliveredCount(ctx, r, StreamParse)
	if err != nil {
		t.Fatal(err)
	}
	// miniredis reports lag as total stream entries (never -1), so this
	// exercises the fast path; the exclusive-from-last-delivered fallback
	// arithmetic is pinned by scanUndeliveredTail below.
	if n != 600 {
		t.Fatalf("lag fast path drift: %d", n)
	}
}

// scanUndeliveredTail is the fallback body (exclusive XRANGE after the
// group's last-delivered-id, pages of 500) — pinned directly so the
// lag=None arithmetic is tested without depending on miniredis's lag
// fidelity.
func TestScanUndeliveredTail(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := XAddJob(ctx, r, StreamParse, ParseJob{SchemaVersion: 1, DocID: "d"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// deliver ids[0] to a consumer: the undelivered tail is ids[1:]
	if _, err := ReadJobs(ctx, r, StreamParse, "c1", 1, 100); err != nil {
		t.Fatal(err)
	}
	groups, err := r.XInfoGroups(ctx, StreamParse).Result()
	if err != nil {
		t.Fatal(err)
	}
	last := groups[0].LastDeliveredID
	n, err := scanUndeliveredTail(ctx, r, StreamParse, last)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tail scan drift: %d (want 2)", n)
	}
	// entries AT the cursor are delivered (exclusive): scanning from ids[0]
	// itself must still yield the two later entries.
	n, err = scanUndeliveredTail(ctx, r, StreamParse, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("exclusive-cursor drift: %d (want 2)", n)
	}
}

func TestQueueDepthUndeliveredPlusPending(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	for i := 0; i < 3; i++ {
		if _, err := XAddJob(ctx, r, StreamParse, ParseJob{SchemaVersion: 1, DocID: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	// deliver 1 (lands in the PEL unacked)
	if _, err := ReadJobs(ctx, r, StreamParse, "c1", 1, 100); err != nil {
		t.Fatal(err)
	}
	depth, err := QueueDepth(ctx, r, StreamParse)
	if err != nil {
		t.Fatal(err)
	}
	// Real Redis: 2 undelivered + 1 pending = 3. miniredis approximates lag
	// with total stream entries (delivered included), so the sum here is 4;
	// the depth = undelivered + pending arithmetic itself is pinned by the
	// components (TestUndeliveredCountFastPath / XPENDING count) and the
	// exclusive-cursor fallback test.
	if depth != 2+1 && depth != 3+1 {
		t.Fatalf("queue depth drift: %d", depth)
	}
}

func TestQuarantineWritesDLQEvent(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	raw := `{"doc_id":"11111111-1111-1111-1111-111111111111","idx":3}`
	id, err := r.XAdd(ctx, &redis.XAddArgs{Stream: StreamParse, Values: map[string]any{"job": raw}}).Result()
	if err != nil {
		t.Fatal(err)
	}
	var gotDocID any
	var gotIdx any
	var gotDetail map[string]any
	Quarantine(ctx, r, StreamParse, id, raw, 6, "boom",
		func(docID, shardIdx any, detail map[string]any) {
			gotDocID, gotIdx, gotDetail = docID, shardIdx, detail
		})
	if gotDocID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("quarantine doc_id drift: %v", gotDocID)
	}
	if gotIdx != 3 {
		t.Fatalf("quarantine idx drift: %v", gotIdx)
	}
	if gotDetail["detail"] != raw || gotDetail["last_error"] != "boom" {
		t.Fatalf("quarantine detail drift: %v", gotDetail)
	}
	// entry is gone from the stream
	if r.XLen(ctx, StreamParse).Val() != 0 {
		t.Fatal("quarantined entry must be dropped")
	}
}

func TestQuarantineUnparseableJobStillQuarantined(t *testing.T) {
	r, _ := newTestRedis(t)
	ctx := context.Background()
	EnsureStreams(ctx, r, StreamParse)
	raw := "total garbage"
	id, err := r.XAdd(ctx, &redis.XAddArgs{Stream: StreamParse, Values: map[string]any{"job": raw}}).Result()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	Quarantine(ctx, r, StreamParse, id, raw, 6, "",
		func(docID, shardIdx any, detail map[string]any) {
			called = true
			if docID != nil || shardIdx != nil {
				t.Fatal("unparseable job must quarantine without links")
			}
		})
	if !called {
		t.Fatal("quarantine must always write the DLQ event")
	}
}

func TestNewConsumerName(t *testing.T) {
	n := NewConsumerName("parser")
	if !strings.HasPrefix(n, "parser-") || len(n) != len("parser-")+8 {
		t.Fatalf("consumer name drift: %s", n)
	}
}
