package chunker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// buildDocWithPages builds a markdown document and a matching per-line page map,
// so page-range behaviour can be tested without a real PDF.
//
// The map follows Stitch's contract: one entry per newline, where the single
// trailing "\n" terminates the last line rather than starting an empty one.
func buildDocWithPages(pagesPerSection int, sections int) (string, []int) {
	var sb strings.Builder
	var pages []int
	line := func(s string, page int) {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(s)
		pages = append(pages, page)
	}
	page := 1
	for s := 0; s < sections; s++ {
		line("## Section "+string(rune('A'+s)), page)
		line("", page)
		for p := 0; p < pagesPerSection; p++ {
			page++
			line("Body text for page "+strings.Repeat("x", 60)+".", page)
			line("", page)
		}
	}
	md := sb.String() + "\n"
	return md, pages
}

// TestPageMapperRanges checks the rune-offset to page lookup directly.
func TestPageMapperRanges(t *testing.T) {
	md := "line one\nline two\nline three\n"
	pages := []int{1, 2, 3}
	m := newPageMapper(md, pages)
	if m == nil {
		t.Fatal("mapper should be built")
	}
	// "line one" is runes [0,8) -> page 1.
	ps, pe := m.rangeFor(0, 8)
	if ps == nil || pe == nil || *ps != 1 || *pe != 1 {
		t.Fatalf("first line range = %v-%v, want 1-1", ps, pe)
	}
	// "line two" is runes [9,17) -> page 2.
	ps, pe = m.rangeFor(9, 17)
	if ps == nil || pe == nil || *ps != 2 || *pe != 2 {
		t.Fatalf("second line range = %v-%v, want 2-2", ps, pe)
	}
	// Spanning lines 1..3 -> pages 1..3.
	ps, pe = m.rangeFor(0, 30)
	if ps == nil || pe == nil || *ps != 1 || *pe != 3 {
		t.Fatalf("spanning range = %v-%v, want 1-3", ps, pe)
	}
}

// TestPageMapperNoMapIsNil pins that a document without page info yields nil
// ranges rather than bogus page 0s.
func TestPageMapperNoMapIsNil(t *testing.T) {
	m := newPageMapper("some text\n", nil)
	ps, pe := m.rangeFor(0, 5)
	if ps != nil || pe != nil {
		t.Fatalf("expected nil page range without a page map, got %v-%v", ps, pe)
	}
}

// TestPageMapperLengthMismatchIsNil guards the degradation path: a page map whose
// length does not match the line count must not mis-attribute pages.
func TestPageMapperLengthMismatchIsNil(t *testing.T) {
	m := newPageMapper("a\nb\nc\n", []int{1})
	ps, _ := m.rangeFor(0, 1)
	if ps != nil {
		t.Fatalf("mismatched page map should yield nil, got %d", *ps)
	}
}

// TestSplitParentChildPageRanges is the regression test for BACKLOG R-27: every
// parent and every child must carry a page range. Previously children were
// always NULL because the only code that set them was never called in
// production.
func TestSplitParentChildPageRanges(t *testing.T) {
	md, pages := buildDocWithPages(3, 4)
	if len(pages) == 0 {
		t.Fatal("fixture produced no page map")
	}

	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 300, 120)

	res := SplitParentChild(md, pages, parentCfg, childCfg)
	if len(res.Children) == 0 {
		t.Fatal("no children produced")
	}
	if len(res.Parents) == 0 {
		t.Fatal("no parents produced")
	}

	for _, p := range res.Parents {
		if p.PageStart == nil || p.PageEnd == nil {
			t.Fatalf("parent seq=%d missing page range: %+v", p.Seq, p)
		}
		if *p.PageEnd < *p.PageStart {
			t.Fatalf("parent seq=%d inverted range %d-%d", p.Seq, *p.PageStart, *p.PageEnd)
		}
	}
	for _, c := range res.Children {
		if c.PageStart == nil || c.PageEnd == nil {
			t.Fatalf("child seq=%d missing page range (R-27): %+v", c.Seq, c)
		}
		if *c.PageEnd < *c.PageStart {
			t.Fatalf("child seq=%d inverted range %d-%d", c.Seq, *c.PageStart, *c.PageEnd)
		}
	}
}

// TestSplitParentChildSeqIsMonotone pins that parents and children share one
// monotone sequence, so neighbour queries and prev/next links cannot confuse the
// two kinds (BACKLOG R-28).
func TestSplitParentChildSeqIsMonotone(t *testing.T) {
	md, pages := buildDocWithPages(2, 3)
	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 300, 120)
	res := SplitParentChild(md, pages, parentCfg, childCfg)

	type seqKind struct {
		seq      int
		isParent bool
	}
	var all []seqKind
	for _, p := range res.Parents {
		all = append(all, seqKind{p.Seq, true})
	}
	for _, c := range res.Children {
		all = append(all, seqKind{c.Seq, false})
	}
	seen := map[int]bool{}
	for _, s := range all {
		if seen[s.seq] {
			t.Fatalf("seq %d assigned to more than one chunk (R-28)", s.seq)
		}
		seen[s.seq] = true
	}
	// Every seq from 0..n-1 must be present exactly once.
	for i := 0; i < len(all); i++ {
		if !seen[i] {
			t.Fatalf("seq %d missing from the sequence (gaps break neighbour queries)", i)
		}
	}
}

// TestSplitParentChildChildrenAreInsideParents pins the containment property:
// every child's rune span lies within its parent's, so a child never cites
// content outside the parent it links to.
func TestSplitParentChildChildrenAreInsideParents(t *testing.T) {
	md, pages := buildDocWithPages(3, 3)
	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 400, 120)
	res := SplitParentChild(md, pages, parentCfg, childCfg)

	for _, c := range res.Children {
		if c.ParentIndex < 0 {
			continue // standalone child: no parent row
		}
		p := res.Parents[c.ParentIndex]
		if c.Start < p.Start || c.End > p.End {
			t.Fatalf("child seq=%d [%d,%d) escapes parent seq=%d [%d,%d)",
				c.Seq, c.Start, c.End, p.Seq, p.Start, p.End)
		}
	}
}

// TestSplitParentChildParentHashMatches verifies a child carries the hash of the
// parent it points at, which is what lets the service resolve parent_id after
// dedupe renumbers the parent set.
func TestSplitParentChildParentHashMatches(t *testing.T) {
	md, pages := buildDocWithPages(3, 2)
	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 400, 120)
	res := SplitParentChild(md, pages, parentCfg, childCfg)

	for _, c := range res.Children {
		if c.ParentIndex < 0 {
			continue
		}
		p := res.Parents[c.ParentIndex]
		if c.ParentHash != p.ChunkHash() {
			t.Fatalf("child seq=%d parent hash %q != parent's %q", c.Seq, c.ParentHash, p.ChunkHash())
		}
	}
}

// TestSplitParentChildVerbatim pins that the hierarchical path preserves the
// verbatim invariant too — children are slices of the parent, which is a slice
// of the document.
func TestSplitParentChildVerbatim(t *testing.T) {
	md, pages := buildDocWithPages(2, 3)
	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 300, 120)
	res := SplitParentChild(md, pages, parentCfg, childCfg)

	runes := []rune(md)
	check := func(seq, start, end, synthetic int, content string) {
		body := string([]rune(content)[synthetic:])
		if want := string(runes[start:end]); body != want {
			t.Fatalf("seq=%d not the source slice\n got=%q\nwant=%q", seq, body, want)
		}
	}
	for _, p := range res.Parents {
		check(p.Seq, p.Start, p.End, p.SyntheticPrefixRunes, p.Content)
	}
	for _, c := range res.Children {
		check(c.Seq, c.Start, c.End, c.SyntheticPrefixRunes, c.Content)
	}
}

// TestSplitParentChildEmptyInput covers the degenerate case.
func TestSplitParentChildEmptyInput(t *testing.T) {
	res := SplitParentChild("", nil, DefaultConfig(), DefaultConfig())
	if len(res.Parents) != 0 || len(res.Children) != 0 {
		t.Fatalf("empty input should produce nothing, got %d parents / %d children",
			len(res.Parents), len(res.Children))
	}
}

// bigTableDoc builds a table far larger than any chunk budget, so the chunker
// must split inside it and re-inject the header — which makes some chunks carry
// a synthetic prefix.
func bigTableDoc(rows int) string {
	var sb strings.Builder
	sb.WriteString("# Big Table\n\n| Col A | Col B |\n| --- | --- |\n")
	for i := 0; i < rows; i++ {
		sb.WriteString("| value ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteString(" | other |\n")
	}
	return sb.String()
}

// TestSplitParentChildVerbatimWithInjectedHeader is the regression test for the
// child-offset bug.
//
// A parent that carries a re-injected table header has Content of the shape
//
//	[generated header][source slice]
//
// so a child re-split from it indexes a buffer that begins with generated text.
// The naive `sub.Start += parent.Start` shift ignored that and produced offsets
// that were wrong by the header length — 202 of 211 children in this fixture
// violated the invariant before the fix. The earlier tests missed it because
// their fixtures never produced a synthetic prefix.
func TestSplitParentChildVerbatimWithInjectedHeader(t *testing.T) {
	doc := bigTableDoc(400)
	runes := []rune(doc)

	base := SplitterConfig{ChunkSize: 400, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 800, 200)
	res := SplitParentChild(doc, nil, parentCfg, childCfg)

	if len(res.Parents) == 0 || len(res.Children) == 0 {
		t.Fatalf("fixture produced no chunks: %d parents, %d children", len(res.Parents), len(res.Children))
	}
	// The fixture must actually exercise the synthetic-prefix path, or this test
	// silently stops guarding anything.
	sawSynthetic := false
	for _, p := range res.Parents {
		if p.SyntheticPrefixRunes > 0 {
			sawSynthetic = true
			break
		}
	}
	if !sawSynthetic {
		t.Fatal("fixture produced no injected-header parent — the guard would be vacuous")
	}

	check := func(kind string, seq, start, end, synthetic int, content string) {
		if end > len(runes) || start > end {
			t.Errorf("%s seq=%d has out-of-range span [%d,%d) for %d runes", kind, seq, start, end, len(runes))
			return
		}
		body := string([]rune(content)[synthetic:])
		if end-start != utf8.RuneCountInString(body) {
			t.Errorf("%s seq=%d: body runes=%d but End-Start=%d (synthetic=%d)",
				kind, seq, utf8.RuneCountInString(body), end-start, synthetic)
			return
		}
		if want := string(runes[start:end]); body != want {
			t.Errorf("%s seq=%d is not the source slice at [%d,%d)", kind, seq, start, end)
		}
	}
	for _, p := range res.Parents {
		check("parent", p.Seq, p.Start, p.End, p.SyntheticPrefixRunes, p.Content)
	}
	for _, c := range res.Children {
		check("child", c.Seq, c.Start, c.End, c.SyntheticPrefixRunes, c.Content)
	}
}

// TestSplitParentChildChildrenInsideParentsWithInjectedHeader extends the
// containment property to the synthetic-prefix case: a child must still sit
// within its parent's source span.
func TestSplitParentChildChildrenInsideParentsWithInjectedHeader(t *testing.T) {
	doc := bigTableDoc(200)
	base := SplitterConfig{ChunkSize: 400, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 800, 200)
	res := SplitParentChild(doc, nil, parentCfg, childCfg)

	for _, c := range res.Children {
		if c.ParentIndex < 0 {
			continue
		}
		p := res.Parents[c.ParentIndex]
		if c.Start < p.Start || c.End > p.End {
			t.Fatalf("child seq=%d [%d,%d) escapes parent seq=%d [%d,%d)",
				c.Seq, c.Start, c.End, p.Seq, p.Start, p.End)
		}
	}
}

// TestReadPageRangeNeverEmptyForChunkedDoc is the regression test for the
// read_pages emptiness bug.
//
// The chunker does not materialise a parent for a section that yields one
// identical child, so a document made of short sections has children and no
// parents. A parents-only read_pages query returned nothing for such a doc,
// which is worse than the duplication the parents-only rule was fixing. The
// store's fallback is verified against real SQL in readpages_test.go; this pins
// the chunker side of the contract: such a doc has chunks, and every one of them
// carries a page range so the range filter can match it.
func TestReadPageRangeNeverEmptyForChunkedDoc(t *testing.T) {
	docs := map[string]string{
		"one short section": "# Title\n\nA short body.\n",
		"tiny plus big":     "# A\n\nshort\n\n# B\n\n" + strings.Repeat("long prose sentence here. ", 400) + "\n",
		"headings only":     "# A\n\n## B\n\n### C\n",
		"many short": func() string {
			var sb strings.Builder
			for i := 0; i < 20; i++ {
				sb.WriteString("# S")
				sb.WriteRune(rune('A' + i))
				sb.WriteString("\n\nshort body\n\n")
			}
			return sb.String()
		}(),
	}
	base := SplitterConfig{ChunkSize: 512, ChunkOverlap: 80, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 2048, 384)

	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			// A page map covering the whole document, so every chunk should
			// resolve a range.
			lines := strings.Count(doc, "\n")
			if !strings.HasSuffix(doc, "\n") {
				lines++
			}
			pages := make([]int, lines)
			for i := range pages {
				pages[i] = 1 + i/3
			}

			res := SplitParentChild(doc, pages, parentCfg, childCfg)
			if len(res.Parents)+len(res.Children) == 0 {
				t.Fatal("document produced no chunks — read_pages would return nothing")
			}

			// Every row must carry a page range; a nil range fails the SQL
			// comparison and would be invisible to read_pages.
			for _, p := range res.Parents {
				if p.PageStart == nil || p.PageEnd == nil {
					t.Fatalf("parent seq=%d has no page range", p.Seq)
				}
			}
			for _, c := range res.Children {
				if c.PageStart == nil || c.PageEnd == nil {
					t.Fatalf("child seq=%d has no page range", c.Seq)
				}
			}
		})
	}
}
