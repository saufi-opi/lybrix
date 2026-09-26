package chunker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// assertVerbatim checks the package's central invariant: a chunk's Content is the
// source text between Start and End, so tables, code fences and lists survive to
// the index (BACKLOG R-26) and page-range lookup is possible (R-27).
//
// A chunk carrying a re-injected table header has that header prepended as
// generated text, so the source portion is Content[SyntheticPrefixRunes:] and
// End-Start equals its rune count.
func assertVerbatim(t *testing.T, src string, chunks []Chunk) {
	t.Helper()
	runes := []rune(src)
	for _, c := range chunks {
		if c.End < c.Start || c.End > len(runes) {
			t.Fatalf("chunk %d has bad range [%d,%d) for %d runes", c.Seq, c.Start, c.End, len(runes))
		}
		if c.SyntheticPrefixRunes < 0 || c.SyntheticPrefixRunes > utf8.RuneCountInString(c.Content) {
			t.Fatalf("chunk %d has bad synthetic prefix %d", c.Seq, c.SyntheticPrefixRunes)
		}
		body := string([]rune(c.Content)[c.SyntheticPrefixRunes:])
		if got := utf8.RuneCountInString(body); got != c.End-c.Start {
			t.Fatalf("chunk %d: runeLen(body)=%d but End-Start=%d\ncontent=%q",
				c.Seq, got, c.End-c.Start, c.Content)
		}
		// The strongest form: the body is the literal slice at those offsets.
		if want := string(runes[c.Start:c.End]); body != want {
			t.Fatalf("chunk %d is not the source slice\n got=%q\nwant=%q", c.Seq, body, want)
		}
	}
}

const tableAndCodeDoc = `# Results

Intro paragraph before the table.

| Model | Score |
| --- | ---: |
| anydoc | 0.91 |
| docling | 0.88 |

Some prose between blocks.

` + "```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```" + `

- first item
- second item
`

// TestChunkerPreservesMarkdown is the headline regression test for R-26: a table,
// a fenced code block and a list must all survive chunking intact. The old
// chunker flattened these into a single whitespace-joined word stream.
func TestChunkerPreservesMarkdown(t *testing.T) {
	chunks := SplitText(tableAndCodeDoc, DefaultConfig())
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	assertVerbatim(t, tableAndCodeDoc, chunks)

	all := joinContents(chunks)
	for _, want := range []string{
		"| Model | Score |",
		"| --- | ---: |",
		"| anydoc | 0.91 |",
		"| docling | 0.88 |",
		"```go\nfunc main() {",
		"- first item",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("markdown not preserved, missing %q in:\n%s", want, all)
		}
	}
}

// TestChunkerNoBoundaryInsideTable guards the protected-span logic: no chunk may
// end or begin in the middle of a table, because a row without its header (or a
// header without its rows) is not interpretable.
func TestChunkerNoBoundaryInsideTable(t *testing.T) {
	// A table far larger than the budget forces the splitter to actually split
	// inside it — the interesting case.
	var sb strings.Builder
	sb.WriteString("# Big Table\n\n| Col A | Col B |\n| --- | --- |\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("| value ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteString(" | other |\n")
	}
	doc := sb.String()

	cfg := SplitterConfig{ChunkSize: 400, ChunkOverlap: 40, Separators: DefaultSeparators}
	chunks := SplitText(doc, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected the table to force multiple chunks, got %d", len(chunks))
	}
	assertVerbatim(t, doc, chunks)

	for _, c := range chunks {
		body := strings.TrimSpace(c.Content)
		if body == "" {
			continue
		}
		// Every non-empty line of a chunk that touches the table must be a
		// complete row: a line starting with "|" must also end with "|".
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || !strings.HasPrefix(trimmed, "|") {
				continue
			}
			if !strings.HasSuffix(trimmed, "|") {
				t.Fatalf("chunk %d cut a table row in half: %q", c.Seq, trimmed)
			}
		}
	}
}

// TestChunkerReinjectsTableHeader verifies that when a table spans chunks, every
// following chunk carries the header row, so a retrieved chunk is
// self-describing about what its columns mean.
func TestChunkerReinjectsTableHeader(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# Report\n\n| Metric | Value |\n| --- | --- |\n")
	for i := 0; i < 120; i++ {
		sb.WriteString("| metric_name_")
		sb.WriteString(strings.Repeat("z", 15))
		sb.WriteString(" | 42 |\n")
	}
	doc := sb.String()

	cfg := SplitterConfig{ChunkSize: 400, ChunkOverlap: 0, Separators: DefaultSeparators}
	chunks := SplitText(doc, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// Every chunk that contains table rows must contain the header.
	for _, c := range chunks {
		if !strings.Contains(c.Content, "| metric_name_") {
			continue
		}
		if !strings.Contains(c.Content, "| Metric | Value |") {
			t.Errorf("chunk %d carries table rows but no header:\n%s", c.Seq, c.Content)
		}
	}
}

// TestChunkerFenceProtectsHeadings ensures a heading-looking line inside a code
// fence is not treated as a section boundary.
func TestChunkerFenceProtectsHeadings(t *testing.T) {
	doc := "# Real Heading\n\n```\n## Not A Heading\n```\n\ntail text\n"
	profile := ProfileDocument(doc)
	bounds := findHeadingBoundaries(doc, 1)
	if len(bounds) != 1 {
		t.Fatalf("expected 1 heading boundary, got %d", len(bounds))
	}
	if profile.MdHeadingTotal != 1 {
		t.Fatalf("expected 1 markdown heading counted, got %d", profile.MdHeadingTotal)
	}
}

// TestProtectedSpansOCRStrayBracket is the regression test for the bug that
// motivated the bounded link/image patterns: an unbounded pattern let a single
// stray "[" swallow the rest of the document as one "protected" atomic span,
// defeating chunking entirely.
func TestProtectedSpansOCRStrayBracket(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Some OCR text with a stray bracket [ here.\n\n")
	for i := 0; i < 60; i++ {
		sb.WriteString("This is a paragraph of ordinary prose that should be split normally.\n\n")
	}
	doc := sb.String()

	spans := protectedSpans(doc)
	total := 0
	for _, s := range spans {
		total += s.end - s.start
	}
	if total > len(doc)/2 {
		t.Fatalf("a stray bracket swallowed %d of %d bytes as protected", total, len(doc))
	}

	cfg := SplitterConfig{ChunkSize: 300, ChunkOverlap: 30, Separators: DefaultSeparators}
	chunks := SplitText(doc, cfg)
	if len(chunks) < 2 {
		t.Fatalf("stray bracket defeated chunking: got %d chunks", len(chunks))
	}
	assertVerbatim(t, doc, chunks)
}

// TestProtectedSpansMarkdownStillProtected is the counterweight to the stray
// bracket test: real links and images must still be protected.
func TestProtectedSpansMarkdownStillProtected(t *testing.T) {
	doc := "See [the docs](https://example.com/a) and ![a picture](img.png) here."
	spans := protectedSpans(doc)
	if len(spans) != 2 {
		t.Fatalf("expected 2 protected spans (link + image), got %d: %+v", len(spans), spans)
	}
}

// TestProtectedSpansMathAndFences covers the remaining protected kinds: block
// math, inline code and a fenced block. The document contains exactly three
// protected regions, and each must be covered by a span.
func TestProtectedSpansMathAndFences(t *testing.T) {
	doc := "before\n\n$$E = mc^2$$\n\nafter `inline code` and\n\n```py\nx = 1\n```\n"
	spans := protectedSpans(doc)
	if len(spans) != 3 {
		t.Fatalf("expected 3 protected spans (math, inline code, fence), got %d: %+v", len(spans), spans)
	}
	for _, want := range []string{"$$E = mc^2$$", "`inline code`", "```py\nx = 1\n```"} {
		found := false
		for _, s := range spans {
			if doc[s.start:s.end] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q to be a protected span, got %+v", want, spans)
		}
	}
}

// TestChunkerLineEndingsNormalized pins that CRLF and LF inputs produce the same
// chunking, since browsers normalize pasted text but uploads do not.
func TestChunkerLineEndingsNormalized(t *testing.T) {
	lf := "# H\n\npara one\n\npara two\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	if NormalizeLineEndings(crlf) != lf {
		t.Fatalf("CRLF not normalized:\n%q", NormalizeLineEndings(crlf))
	}
}

// TestChunkerEmptyAndWhitespace covers the degenerate inputs.
func TestChunkerEmptyAndWhitespace(t *testing.T) {
	if got := SplitText("", DefaultConfig()); got != nil {
		t.Fatalf("empty input should produce nil, got %d chunks", len(got))
	}
	// Whitespace-only input must not panic and must not yield blank chunks.
	for _, c := range SplitText("   \n\n   \t  \n", DefaultConfig()) {
		if strings.TrimSpace(c.Content) == "" {
			t.Fatalf("emitted a whitespace-only chunk: %q", c.Content)
		}
	}
}

func joinContents(chunks []Chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c.Content)
	}
	return sb.String()
}

// TestChunkerRepairsEmptyTableHeader covers the MarkItDown/OCR table shape where
// the column-name row is empty:
//
//	||
//	| --- | --- |
//	| real column A | real column B |
//
// The tracker promotes the first data row into real column names so the header
// re-injected into later chunks is meaningful. Without this, every chunk after
// the first would carry a header of bare pipes.
func TestChunkerRepairsEmptyTableHeader(t *testing.T) {
	ht := newHeaderTracker()
	ht.update("||\n| --- | --- |\n")
	if !ht.pendingExtend[markdownTableHookPriority] {
		t.Fatal("an empty column-name row must be flagged for extension")
	}

	// The first data row supplies the real column names.
	ht.update("| real col A | real col B |\n")
	got := ht.getHeaders()
	if !strings.Contains(got, "real col A") || !strings.Contains(got, "real col B") {
		t.Fatalf("header not repaired from the first data row: %q", got)
	}
	if !strings.Contains(got, "---") {
		t.Fatalf("repaired header must keep its separator row: %q", got)
	}
	if isEmptyTableHeaderRow(got) {
		t.Fatalf("repaired header still looks empty: %q", got)
	}
}

// TestChunkerHeaderEndsOnProseAndColumnMismatch pins the two guards that stop a
// table header bleeding into unrelated content.
func TestChunkerHeaderEndsOnProseAndColumnMismatch(t *testing.T) {
	// Prose after the table ends the header.
	ht := newHeaderTracker()
	ht.update("| A | B |\n| --- | --- |\n")
	if ht.getHeaders() == "" {
		t.Fatal("header should be active inside the table")
	}
	ht.update("Some ordinary prose resumed here.\n")
	if ht.getHeaders() != "" {
		t.Fatalf("prose must end the table header, got %q", ht.getHeaders())
	}

	// A different-width table must not inherit the previous header.
	ht2 := newHeaderTracker()
	ht2.update("| A | B |\n| --- | --- |\n")
	if !headerColumnMismatch(ht2.getHeaders(), "| only one column |\n") {
		t.Fatal("a 1-column row must not inherit a 2-column header")
	}
	if headerColumnMismatch(ht2.getHeaders(), "| x | y |\n") {
		t.Fatal("a same-width row must inherit the header")
	}
}

// TestChunkerMultibyteVerbatim covers a CJK document, where every character is
// three bytes. All chunker offsets are rune-based, so a byte/rune confusion
// would corrupt Content or land a boundary mid-character — and the page mapper
// indexes the same rune space.
func TestChunkerMultibyteVerbatim(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# 第一章 绪论\n\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("这是一段用于测试的中文文本，包含表格与代码块。\n\n")
	}
	sb.WriteString("| 模型 | 分数 |\n| --- | --- |\n| anydoc | 0.91 |\n\n")
	sb.WriteString("```go\nfunc main() { println(\"你好\") }\n```\n")
	doc := sb.String()

	cfg := SplitterConfig{ChunkSize: 300, ChunkOverlap: 40, Separators: DefaultSeparators}
	chunks := SplitText(doc, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a %d-rune doc, got %d", len([]rune(doc)), len(chunks))
	}
	assertVerbatim(t, doc, chunks)

	all := joinContents(chunks)
	if !strings.Contains(all, "| 模型 | 分数 |") {
		t.Error("CJK table lost")
	}
	if !strings.Contains(all, "你好") {
		t.Error("CJK code block content lost")
	}

	// The page map must resolve for every chunk.
	lines := strings.Count(doc, "\n")
	pages := make([]int, lines)
	for i := range pages {
		pages[i] = 1 + i/10
	}
	res := SplitParentChild(doc, pages, cfg, SplitterConfig{ChunkSize: 150, ChunkOverlap: 30, Separators: DefaultSeparators})
	for _, c := range res.Children {
		if c.PageStart == nil || c.PageEnd == nil {
			t.Fatalf("CJK child seq=%d has no page range", c.Seq)
		}
	}
}
