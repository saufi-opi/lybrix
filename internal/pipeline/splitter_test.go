package pipeline

import (
	"reflect"
	"testing"
)

// Splitter bound arithmetic (PRD §6.2): a book is never a unit of work.
// Cases ported verbatim from tests/test_splitter.py.

type pair struct{ s, e int }

func boundsPairs(bounds []ShardBound) []pair {
	out := make([]pair, len(bounds))
	for i, b := range bounds {
		out[i] = pair{b.PageStart, b.PageEnd}
	}
	return out
}

func TestFixedBoundsExactDivision(t *testing.T) {
	bounds, err := FixedBounds(40, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []pair{{1, 20}, {21, 40}}
	if !reflect.DeepEqual(boundsPairs(bounds), want) {
		t.Fatalf("got %v want %v", boundsPairs(bounds), want)
	}
	if bounds[0].Idx != 0 || bounds[1].Idx != 1 {
		t.Fatalf("idx drift: %v", bounds)
	}
}

func TestFixedBoundsRemainder(t *testing.T) {
	bounds, err := FixedBounds(45, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := bounds[len(bounds)-1]
	if last.PageEnd != 45 || last.PageStart != 41 {
		t.Fatalf("last bound drift: %+v", last)
	}
	for _, b := range bounds {
		if b.PageEnd < b.PageStart {
			t.Fatalf("inverted bound: %+v", b)
		}
	}
}

func TestFixedBoundsOverlapNeverRepeatsAPageTwiceFar(t *testing.T) {
	// With 1-page overlap, consecutive shards share exactly one page and
	// coverage stays total.
	bounds, err := FixedBounds(100, 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bounds[0].PageStart != 1 || bounds[0].PageEnd != 20 {
		t.Fatalf("first bound drift: %+v", bounds[0])
	}
	if bounds[1].PageStart != 20 { // overlap page
		t.Fatalf("second bound start drift: %+v", bounds[1])
	}
	if bounds[len(bounds)-1].PageEnd != 100 {
		t.Fatalf("last bound end drift: %+v", bounds[len(bounds)-1])
	}
	// every page covered
	covered := map[int]bool{}
	for _, b := range bounds {
		for p := b.PageStart; p <= b.PageEnd; p++ {
			covered[p] = true
		}
	}
	if len(covered) != 100 {
		t.Fatalf("coverage incomplete: %d pages", len(covered))
	}
}

func TestFixedBoundsSinglePageBook(t *testing.T) {
	bounds, err := FixedBounds(1, 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []pair{{1, 1}}
	if !reflect.DeepEqual(boundsPairs(bounds), want) {
		t.Fatalf("got %v want %v", boundsPairs(bounds), want)
	}
}

func TestFixedBoundsRejectsZeroPages(t *testing.T) {
	if _, err := FixedBounds(0, 20, 1); err == nil {
		t.Fatal("expected error for 0 pages")
	}
}

// The anti-loop guard: shard_pages=1 must not spin forever.
func TestFixedBoundsShardPagesOneAntiLoop(t *testing.T) {
	bounds, err := FixedBounds(5, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []pair{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}}
	if !reflect.DeepEqual(boundsPairs(bounds), want) {
		t.Fatalf("shard_pages=1 anti-loop broken: %v", boundsPairs(bounds))
	}
}

func TestChapterAlignedSnapsToBookmarks(t *testing.T) {
	outline := []Bookmark{{1, "Intro"}, {21, "Ch 1"}, {61, "Ch 2"}}
	bounds, err := ChapterAlignedBounds(outline, 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	// a boundary must exist exactly at each chapter start
	starts := map[int]bool{}
	for _, b := range bounds {
		starts[b.PageStart] = true
	}
	if !starts[21] || !starts[61] {
		t.Fatalf("chapter starts missing: %v", starts)
	}
	// coverage complete
	covered := map[int]bool{}
	for _, b := range bounds {
		for p := b.PageStart; p <= b.PageEnd; p++ {
			covered[p] = true
		}
	}
	if len(covered) != 80 {
		t.Fatalf("coverage incomplete: %d", len(covered))
	}
}

func TestChapterAlignedNoShardCrossesChapterEdge(t *testing.T) {
	outline := []Bookmark{{1, "A"}, {10, "B"}}
	bounds, err := ChapterAlignedBounds(outline, 25, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bounds {
		// shard either ends before chapter B starts, or starts at/after it
		if !(b.PageEnd < 10 || b.PageStart >= 10) {
			t.Fatalf("shard crosses chapter edge: %+v", b)
		}
	}
}

func TestChapterAlignedDegenerateOutlineFallsBack(t *testing.T) {
	outline := []Bookmark{{5, "x"}, {5, "x"}} // duplicate/degenerate
	bounds, err := ChapterAlignedBounds(outline, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounds) == 0 {
		t.Fatal("expected fallback bounds")
	}
	for _, b := range bounds {
		if b.PageEnd < b.PageStart {
			t.Fatalf("inverted bound after fallback: %+v", b)
		}
	}
}
