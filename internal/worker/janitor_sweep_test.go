package worker

import (
	"encoding/json"
	"testing"

	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// pendingShardSweep's pure seams (R-32): the shared key derivation and the
// rescue decision. The DB-backed sweep itself runs in the testcontainers
// lane; these tests pin the decision logic without docker.

func TestParseJobKey(t *testing.T) {
	docID := "aaaaaaaa-0000-0000-0000-000000000000"
	// payload shape: what queue.XAddJob actually writes — a queue.ParseJob
	// JSON round-trip, so the test pins the real Redis payload, not a
	// hand-built map.
	job := queue.ParseJob{SchemaVersion: queue.SchemaVersion, DocID: docID, Idx: 3,
		PageStart: 40, PageEnd: 59, SourceURI: "s3://raw/x.pdf"}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	// idx must survive as float64 (encoding/json decodes numbers into
	// interface{} maps as float64) — the extractor's job is to convert it.
	if _, ok := payload["idx"].(float64); !ok {
		t.Fatalf("fixture drift: idx is %T, want float64", payload["idx"])
	}

	cases := []struct {
		name    string
		payload map[string]any
		wantKey string
		wantOK  bool
	}{
		{name: "doc_id+idx extracted, float64 idx from JSON",
			payload: payload, wantKey: parseJobKeyOf(docID, 3), wantOK: true},
		{name: "unparseable payload shape: missing doc_id",
			payload: map[string]any{"idx": float64(0)}, wantKey: "", wantOK: false},
		{name: "unparseable payload shape: missing idx",
			payload: map[string]any{"doc_id": docID}, wantKey: "", wantOK: false},
		{name: "empty doc_id",
			payload: map[string]any{"doc_id": "", "idx": float64(0)}, wantKey: "", wantOK: false},
		{name: "idx zero is a real key (synthetic shard)",
			payload: map[string]any{"doc_id": docID, "idx": float64(0)},
			wantKey: parseJobKeyOf(docID, 0), wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := parseJobKey(tc.payload)
			if ok != tc.wantOK || key != tc.wantKey {
				t.Fatalf("parseJobKey drift: got (%q, %v), want (%q, %v)", key, ok, tc.wantKey, tc.wantOK)
			}
		})
	}

	// Byte-identity of the two derivation paths (the R-1-class regression
	// the shared helper exists to prevent): Redis-payload path and DB-row
	// path must produce the same bytes for the same (doc_id, idx).
	shard := store.StalePendingShard{DocID: docID, Idx: 3}
	if got, want := parseJobKeyOf(shard.DocID, shard.Idx), parseJobKeyOf(docID, 3); got != want {
		t.Fatalf("derivation drift: DB-row %q != source %q", got, want)
	}
	payloadKey, ok := parseJobKey(payload)
	if !ok || payloadKey != parseJobKeyOf(docID, 3) {
		t.Fatalf("payload path drift: (%q, %v)", payloadKey, ok)
	}
}

func TestMissingParseJobs(t *testing.T) {
	docID := "bbbbbbbb-0000-0000-0000-000000000000"
	shards := []store.StalePendingShard{
		{DocID: docID, Idx: 0, PageStart: 1, PageEnd: 20, SourceURI: "s3://raw/a.pdf"},
		{DocID: docID, Idx: 1, PageStart: 21, PageEnd: 40, SourceURI: "s3://raw/a.pdf"},
	}
	// queued is built via parseJobKeyOf itself, never a literal: the test
	// pins the shared helper rather than a hardcoded string.
	keyOf := func(s store.StalePendingShard) string { return parseJobKeyOf(s.DocID, s.Idx) }

	cases := []struct {
		name    string
		shards  []store.StalePendingShard
		queued  map[string]bool
		wantIdx []int
	}{
		{
			name:    "all keys present in queued → empty",
			shards:  shards,
			queued:  map[string]bool{keyOf(shards[0]): true, keyOf(shards[1]): true},
			wantIdx: []int{},
		},
		{
			name:    "some missing → exactly the missing ParseJobs",
			shards:  shards,
			queued:  map[string]bool{keyOf(shards[0]): true},
			wantIdx: []int{1},
		},
		{
			name:    "empty queued → all shards",
			shards:  shards,
			queued:  map[string]bool{},
			wantIdx: []int{0, 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := missingParseJobs(tc.shards, tc.queued)
			if len(got) != len(tc.wantIdx) {
				t.Fatalf("count drift: got %d jobs, want %d (%+v)", len(got), len(tc.wantIdx), got)
			}
			for i, wantIdx := range tc.wantIdx {
				want := queue.ParseJob{
					SchemaVersion: queue.SchemaVersion,
					DocID:         tc.shards[wantIdx].DocID,
					Idx:           tc.shards[wantIdx].Idx,
					PageStart:     tc.shards[wantIdx].PageStart,
					PageEnd:       tc.shards[wantIdx].PageEnd,
					SourceURI:     tc.shards[wantIdx].SourceURI,
				}
				if got[i] != want {
					t.Fatalf("job %d drift:\n got %+v\nwant %+v", i, got[i], want)
				}
			}
		})
	}
}
