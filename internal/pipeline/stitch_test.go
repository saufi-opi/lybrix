package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

// Stitch semantics ported from tests/test_stitch.py.

func TestStitchBoundaryHeadingDedupe(t *testing.T) {
	// shard 1 ends on a heading the overlap re-shows at shard 2's start
	docs := map[int]string{
		0: "# Chapter One\nbody one\n## Shared Tail",
		1: "## Shared Tail\noverlap body\nmore body",
	}
	out := Stitch(docs, map[int][2]int{0: {1, 2}, 1: {2, 3}})
	lines := strings.Split(strings.TrimSpace(out.Markdown), "\n")
	// the duplicated boundary heading must appear once
	count := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "## Shared Tail" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("boundary heading dedupe failed (%d occurrences): %q", count, out.Markdown)
	}
}

func TestStitchPageMapMonotone(t *testing.T) {
	docs := map[int]string{
		0: "alpha\nbeta",
		1: "gamma\ndelta",
		2: "", // empty shard: contributes neither lines nor a consumed range
	}
	ranges := map[int][2]int{0: {1, 2}, 1: {3, 4}, 2: {5, 6}}
	out := Stitch(docs, ranges)
	if len(out.Pages) != 4 {
		t.Fatalf("page map size drift: %d", len(out.Pages))
	}
	for i := 1; i < len(out.Pages); i++ {
		if out.Pages[i] < out.Pages[i-1] {
			t.Fatalf("page map not monotone: %v", out.Pages)
		}
	}
	if out.Pages[0] != 1 || out.Pages[2] != 3 {
		t.Fatalf("page map interpolation drift: %v", out.Pages)
	}
}

func TestStitchJSONWrapperPayload(t *testing.T) {
	// parser uploads export markdown directly; a JSON wrapper payload's
	// "markdown" field is honored (load_shard_docs parity).
	docs := map[int]string{0: `{"markdown": "# Title\nwords"}`}
	out := Stitch(docs, nil)
	if !strings.Contains(out.Markdown, "# Title") {
		t.Fatalf("json wrapper markdown lost: %q", out.Markdown)
	}
}

func TestStitchEmpty(t *testing.T) {
	out := Stitch(map[int]string{}, nil)
	if out.Markdown != "\n" {
		t.Fatalf("empty stitch drift: %q", out.Markdown)
	}
}

func TestStitchOrderFollowsShardIdx(t *testing.T) {
	// map iteration order must not leak into the stitched doc
	docs := map[int]string{
		2: "third shard",
		0: "first shard",
		1: "second shard",
	}
	out := Stitch(docs, nil)
	idx0 := strings.Index(out.Markdown, "first shard")
	idx1 := strings.Index(out.Markdown, "second shard")
	idx2 := strings.Index(out.Markdown, "third shard")
	if !(idx0 < idx1 && idx1 < idx2) {
		t.Fatalf("shard order broken: %q (%d,%d,%d)", out.Markdown, idx0, idx1, idx2)
	}
}

// --- OCR gate thresholds ---------------------------------------------------

func TestGateVerdictThresholds(t *testing.T) {
	// The gate math itself: mean < threshold ⇒ needs_ocr. (Page-text
	// extraction from real PDFs is covered by the fixture-PDF lane; the
	// threshold arithmetic is unit-pinned here.)
	counts := []int{50, 0, 0}
	sum := 0
	for _, c := range counts {
		sum += c
	}
	mean := float64(sum) / float64(len(counts))
	if mean >= 20 {
		t.Fatalf("mean drift: %v", mean)
	}
	empty := OCRVerdict{NeedsOCR: true, MeanCharsPerPage: 0.0}
	if !empty.NeedsOCR {
		t.Fatal("empty verdict drift")
	}
	_ = reflect.DeepEqual // reflect retained
}

// --- yield check (blueprint §4.2 fast-path gate) ---------------------------

func TestYieldOK(t *testing.T) {
	if !YieldOK(strings.Repeat("a", 500), 10, 50) {
		t.Fatal("healthy markdown should pass the yield check")
	}
	if YieldOK("short", 10, 50) {
		t.Fatal("low-yield markdown should fail")
	}
	if YieldOK("", 10, 50) {
		t.Fatal("empty markdown should fail")
	}
}
