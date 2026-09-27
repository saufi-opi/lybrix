package service

import (
	"testing"

	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// TestLadderAction is the ladder decision's first table test (R-36 task 3 —
// LadderAction had zero coverage before). The case matrix comes from the
// two call sites in parser.go (HandleParse's `shard.Attempts >= 2` branch
// and its resplit escalation): hold (attempt 1 never laddered — the caller
// checks Attempts >= 2 first), resplit at attempts 2-3 when the span is
// worth splitting, and text_only otherwise. attempt >= 4 is always
// text_only (the terminal attempt — attempts are not incremented past the
// claim, so attempt 4 IS the text-only attempt).
func TestLadderAction(t *testing.T) {
	cases := []struct {
		name       string
		pageStart  int
		pageEnd    int
		attempts   int
		shardPages int
		want       string
	}{
		// span < 2 → text_only regardless of attempts (single-page shards
		// and EPUB synthetic shards are never split).
		{name: "single page, attempt 2", pageStart: 5, pageEnd: 5, attempts: 2,
			shardPages: 20, want: "text_only"},
		{name: "single page, attempt 4", pageStart: 5, pageEnd: 5, attempts: 4, shardPages: 20, want: "text_only"},
		{name: "epub synthetic shard (span 1)", pageStart: 1, pageEnd: 1, attempts: 2,
			shardPages: 20, want: "text_only"},
		// attempt 1: the caller never ladders (HandleParse checks
		// Attempts >= 2 first) — the function still answers text_only.
		{name: "attempt 1 is not laddered", pageStart: 1, pageEnd: 400, attempts: 1, shardPages: 20, want: "text_only"},
		// attempt 2: quarter-split only when the span is worth it. The
		// condition is span >= 4*(max(1, shardPages/4))/2 — for the 20-page
		// shard size that is span >= 10.
		{name: "attempt 2, span 400 (>= threshold)", pageStart: 1, pageEnd: 400, attempts: 2, shardPages: 20, want: "split"},
		{name: "attempt 2, span 10 (== threshold)", pageStart: 1, pageEnd: 10, attempts: 2, shardPages: 20, want: "split"},
		{name: "attempt 2, span 9 (< threshold)", pageStart: 1, pageEnd: 9, attempts: 2, shardPages: 20, want: "text_only"},
		{name: "attempt 2, shardPages 4 → threshold span >= 2", pageStart: 1, pageEnd: 2, attempts: 2, shardPages: 4, want: "split"},
		{name: "attempt 2, tiny shardPages 4, span 1 (single page)", pageStart: 1, pageEnd: 1, attempts: 2, shardPages: 4, want: "text_only"},
		// attempt 3: any span >= 2 splits to single pages.
		{name: "attempt 3, span 2", pageStart: 10, pageEnd: 11, attempts: 3, shardPages: 20, want: "split"},
		{name: "attempt 3, span 100", pageStart: 1, pageEnd: 100, attempts: 3, shardPages: 20, want: "split"},
		// attempt >= 4: terminal — text-only conversion, never split.
		{name: "attempt 4 terminal", pageStart: 1, pageEnd: 400, attempts: 4, shardPages: 20, want: "text_only"},
		{name: "attempt 5 terminal", pageStart: 1, pageEnd: 400, attempts: 5, shardPages: 20, want: "text_only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shard := &store.Shard{
				PageStart: tc.pageStart,
				PageEnd:   tc.pageEnd,
				Attempts:  tc.attempts,
			}
			if got := LadderAction(shard, tc.shardPages); got != tc.want {
				t.Fatalf("LadderAction(span %d, attempts %d, shardPages %d) = %q, want %q",
					tc.pageEnd-tc.pageStart+1, tc.attempts, tc.shardPages, got, tc.want)
			}
		})
	}
}

// TestSettledAfterDone pins finishParse's settle decision over the doc
// counter shapes the splitter/fixtures produce (mirrors splitter_test.go's
// fixture style): only total>0 with done+failed >= total settles.
func TestSettledAfterDone(t *testing.T) {
	intPtr := func(i int) *int { return &i }
	cases := []struct {
		name string
		doc  *store.Document
		want bool
	}{
		{name: "nil doc", doc: nil, want: false},
		{name: "zero shards (total_shards NULL)", doc: &store.Document{TotalShards: nil, ShardsDone: 0, ShardsFailed: 0}, want: false},
		{name: "total 0", doc: &store.Document{TotalShards: intPtr(0), ShardsDone: 0, ShardsFailed: 0}, want: false},
		{name: "mid-parse (done < total)", doc: &store.Document{
			TotalShards: intPtr(20), ShardsDone: 19, ShardsFailed: 0}, want: false},
		{name: "exactly settled (done == total)", doc: &store.Document{
			TotalShards: intPtr(20), ShardsDone: 20, ShardsFailed: 0}, want: true},
		{name: "settled with failures (done+failed >= total)", doc: &store.Document{
			TotalShards: intPtr(20), ShardsDone: 18, ShardsFailed: 2}, want: true},
		{name: "all failed", doc: &store.Document{
			TotalShards: intPtr(3), ShardsDone: 0, ShardsFailed: 3}, want: true},
		{name: "overshoot (sub-shard resplit over-count)", doc: &store.Document{
			TotalShards: intPtr(10), ShardsDone: 12, ShardsFailed: 0}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settledAfterDone(tc.doc); got != tc.want {
				t.Fatalf("settledAfterDone(%+v) = %v, want %v", tc.doc, got, tc.want)
			}
		})
	}
}

// TestSubShardJobs pins the resplitShard enqueue-payload seam: one ParseJob
// per bound, idx offset by startIdx (sub-shards continue the parent's idx
// sequence — R-11 ladder), pages taken from the bounds verbatim, and the
// doc's source URI threaded through.
func TestSubShardJobs(t *testing.T) {
	docID := "44444444-4444-4444-4444-444444444444"
	doc := &store.Document{ID: docID, SourceURI: "s3://raw/book.pdf"}
	shard := &store.Shard{Idx: 3, PageStart: 81, PageEnd: 100}

	cases := []struct {
		name     string
		bounds   [][2]int
		startIdx int
		want     []queue.ParseJob
	}{
		{
			name:     "quarter-split of a 20-page shard → 4 sub-shards at startIdx",
			bounds:   [][2]int{{81, 84}, {85, 88}, {89, 92}, {93, 96}},
			startIdx: 28,
			want: []queue.ParseJob{
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 28, PageStart: 81, PageEnd: 84, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 29, PageStart: 85, PageEnd: 88, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 30, PageStart: 89, PageEnd: 92, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 31, PageStart: 93, PageEnd: 96, SourceURI: "s3://raw/book.pdf"},
			},
		},
		{
			name:     "single-page split (attempt 3) at idx 0",
			bounds:   [][2]int{{81, 81}, {82, 82}},
			startIdx: 0,
			want: []queue.ParseJob{
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 0, PageStart: 81, PageEnd: 81, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 1, PageStart: 82, PageEnd: 82, SourceURI: "s3://raw/book.pdf"},
			},
		},
		{
			name:     "empty bounds → empty slice",
			bounds:   [][2]int{},
			startIdx: 5,
			want:     []queue.ParseJob{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := subShardJobs(doc, shard, tc.bounds, tc.startIdx)
			if len(got) != len(tc.want) {
				t.Fatalf("count drift: got %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("job %d drift:\n got %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
