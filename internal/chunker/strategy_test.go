package chunker

import (
	"strings"
	"testing"
)

// TestProfileHeadingHeavyDoc pins that a real heading backbone is detected, which
// is what selects the heading tier.
func TestProfileHeadingHeavyDoc(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		sb.WriteString("## Section ")
		sb.WriteString(string(rune('A' + i)))
		sb.WriteString("\n\nSome body text for this section.\n\n")
	}
	p := ProfileDocument(sb.String())
	if p.MdHeadingTotal != 6 {
		t.Fatalf("expected 6 headings, got %d", p.MdHeadingTotal)
	}
	if p.DominantHeadingLevel() != 2 {
		t.Fatalf("expected dominant level 2, got %d", p.DominantHeadingLevel())
	}
	chain := SelectStrategy(p)
	if len(chain) == 0 || chain[0] != TierHeading {
		t.Fatalf("expected heading tier first, got %v", chain)
	}
}

// TestProfilePlainTextDoc pins that a document with no markdown headings but
// plenty of structural markers selects the heuristic tier, not heading.
func TestProfilePlainTextDoc(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		sb.WriteString("Chapter ")
		sb.WriteString(string(rune('1' + i)))
		sb.WriteString(": A Title\n\nSome prose follows here.\n\n")
	}
	p := ProfileDocument(sb.String())
	if p.MdHeadingTotal != 0 {
		t.Fatalf("expected no markdown headings, got %d", p.MdHeadingTotal)
	}
	chain := SelectStrategy(p)
	for _, tier := range chain {
		if tier == TierHeading {
			t.Fatalf("plain text doc should not select the heading tier: %v", chain)
		}
	}
	if len(chain) == 0 || chain[0] != TierHeuristic {
		t.Fatalf("expected heuristic tier first, got %v", chain)
	}
}

// TestStrategyAlwaysReturnsChunks pins the safety property: however the tiers
// fare, the caller always receives a usable chunk set, because the chain ends in
// the legacy splitter.
func TestStrategyAlwaysReturnsChunks(t *testing.T) {
	inputs := []string{
		"",
		"a",
		"# H\n\nbody",
		strings.Repeat("plain prose with no structure at all. ", 200),
		"# A\n\n```\ncode only\n```\n",
		strings.Repeat("| a | b |\n| --- | --- |\n| 1 | 2 |\n", 100),
	}
	for i, in := range inputs {
		chunks := Split(in, DefaultConfig())
		if in == "" {
			if len(chunks) != 0 {
				t.Errorf("case %d: empty input should yield nothing", i)
			}
			continue
		}
		if len(chunks) == 0 {
			t.Errorf("case %d: Split returned no chunks for non-empty input", i)
			continue
		}
		assertVerbatim(t, in, chunks)
	}
}

// TestEmptyStrategyMeansAuto guards the footgun WeKnora documents in its own
// code: treating an empty Strategy as legacy silently disables heading
// breadcrumbs for every caller that forgot to set the field. Empty must mean
// auto.
func TestEmptyStrategyMeansAuto(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		sb.WriteString("## Section ")
		sb.WriteString(string(rune('A' + i)))
		sb.WriteString("\n\nBody text.\n\n")
	}
	doc := sb.String()

	chain, profile := resolveChainWithProfile(doc, SplitterConfig{})
	if profile == nil {
		t.Fatal("empty strategy should run the profiler (auto), got nil profile")
	}
	if len(chain) == 0 || chain[0] != TierHeading {
		t.Fatalf("empty strategy should resolve to auto and pick heading first, got %v", chain)
	}
}

// TestValidatorRejectsBrokenOutput covers each rejection rule, since a tier that
// is not rejected means the chain never advances.
func TestValidatorRejectsBrokenOutput(t *testing.T) {
	cases := []struct {
		name    string
		chunks  []Chunk
		total   int
		size    int
		wantOK  bool
		reasonS string
	}{
		{name: "no chunks", chunks: nil, total: 1000, size: 100, wantOK: false},
		{
			name:   "single chunk for large doc",
			chunks: []Chunk{{Content: strings.Repeat("x", 900), Start: 0, End: 900}},
			total:  900, size: 100, wantOK: false,
		},
		{
			name: "healthy split",
			chunks: []Chunk{
				{Content: strings.Repeat("x", 100), Start: 0, End: 100},
				{Content: strings.Repeat("x", 100), Start: 100, End: 200},
			},
			total: 200, size: 100, wantOK: true,
		},
		{
			name: "chunk exceeds 2x target",
			chunks: []Chunk{
				{Content: strings.Repeat("x", 300), Start: 0, End: 300},
				{Content: strings.Repeat("x", 100), Start: 300, End: 400},
			},
			total: 400, size: 100, wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateChunks(tc.chunks, tc.total, tc.size)
			if got.OK != tc.wantOK {
				t.Fatalf("ValidateChunks.OK=%v want %v (reason %q)", got.OK, tc.wantOK, got.Reason)
			}
			if !tc.wantOK && got.Reason == "" {
				t.Fatal("a rejection must carry a reason")
			}
		})
	}
}

// TestDiagnosticsRecordsRejections pins that a rejected tier is recorded with its
// reason, which is what makes the tier chain debuggable.
func TestDiagnosticsRecordsRejections(t *testing.T) {
	doc := strings.Repeat("plain unstructured prose sentence. ", 100)
	_, diag := SplitWithDiagnostics(doc, SplitterConfig{ChunkSize: 200, ChunkOverlap: 20, Separators: DefaultSeparators})
	if diag == nil {
		t.Fatal("diagnostics should never be nil")
	}
	if diag.SelectedTier == "" {
		t.Fatal("a selected tier must be recorded")
	}
	for _, r := range diag.Rejected {
		if r.Reason == "" {
			t.Fatalf("rejected tier %s has no reason", r.Tier)
		}
	}
}

// TestHeuristicDropsBoundariesInsideProtected pins that the heuristic tier does
// not cut through a table even when a structural marker sits inside it.
func TestHeuristicDropsBoundariesInsideProtected(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Intro prose.\n\n| Col A | Col B |\n| --- | --- |\n")
	// Numbered-looking rows inside the table would be boundaries if not protected.
	for i := 0; i < 40; i++ {
		sb.WriteString("| 1. looks numbered | value |\n")
	}
	doc := sb.String()

	spans := protectedSpansRune(doc, protectedSpans(doc))
	if len(spans) == 0 {
		t.Fatal("table should be protected")
	}
	bounds := findHeuristicBoundaries(doc, nil)
	filtered := dropBoundsInsideSpans(bounds, spans)
	for _, b := range filtered {
		if insideSpan(spans, b.runeStart) {
			t.Fatalf("boundary at %d survived inside a protected span", b.runeStart)
		}
	}
}

// TestHashStabilityIsWordLevel pins what the hash does and does not guarantee.
//
// It IS stable across pure reformatting: a span whose word sequence is unchanged
// hashes the same whether verbatim or whitespace-flattened. It is NOT stable
// across the chunker rewrite for a heading-bearing section, because the old
// chunker prepended the heading text without its "#" while the new Content
// includes the heading line verbatim. Migration therefore must delete rather
// than upsert — this test exists so nobody "restores" the stronger claim.
func TestHashStabilityIsWordLevel(t *testing.T) {
	// Stable: identical words, different whitespace.
	verbatim := "| Model | Score |\n| --- | ---: |\n| anydoc | 0.91 |"
	flat := strings.Join(strings.Fields(verbatim), " ")
	if HashText(verbatim) != HashText(flat) {
		t.Fatal("HashText must ignore whitespace")
	}

	// Not stable: the heading marker is part of the new text.
	newText := "# Results\n\nWe will use RRF to do a fusion of the results."
	oldText := "Results\n\nWe will use RRF to do a fusion of the results."
	if HashText(newText) == HashText(oldText) {
		t.Fatal("heading-bearing text must hash differently from the old heading-tail form")
	}

	// Distinct words must not collide.
	if HashText("alpha beta") == HashText("alpha gamma") {
		t.Fatal("distinct text must not collide")
	}
}

// TestRenumberGlobalIsCollisionFree is the regression test for BACKLOG R-28.
// Parents and children are deduped independently, so each ends up numbered
// 0..N-1 and the sequences collide; RenumberGlobal must give one monotone range.
func TestRenumberGlobalIsCollisionFree(t *testing.T) {
	parents := []Chunk{{Content: "p0"}, {Content: "p1"}, {Content: "p2"}}
	children := []ChildChunk{
		{Chunk: Chunk{Content: "c0"}},
		{Chunk: Chunk{Content: "c1"}},
		{Chunk: Chunk{Content: "c2"}},
		{Chunk: Chunk{Content: "c3"}},
	}
	total := RenumberGlobal(parents, children)
	if total != 7 {
		t.Fatalf("expected 7 numbered chunks, got %d", total)
	}
	seen := map[int]string{}
	for _, p := range parents {
		if prev, ok := seen[p.Seq]; ok {
			t.Fatalf("seq %d collides: %q and %q", p.Seq, prev, p.Content)
		}
		seen[p.Seq] = p.Content
	}
	for _, c := range children {
		if prev, ok := seen[c.Seq]; ok {
			t.Fatalf("seq %d collides: %q and %q", c.Seq, prev, c.Content)
		}
		seen[c.Seq] = c.Content
	}
	for i := 0; i < total; i++ {
		if _, ok := seen[i]; !ok {
			t.Fatalf("seq %d missing — gaps break neighbour queries", i)
		}
	}
}

// TestSplitParentChildSeqIsUniqueAfterDedupe pins the composition that the
// service performs: dedupe each set, then renumber globally.
func TestSplitParentChildSeqIsUniqueAfterDedupe(t *testing.T) {
	md, pages := buildDocWithPages(2, 4)
	base := SplitterConfig{ChunkSize: 200, ChunkOverlap: 40, Separators: DefaultSeparators}
	parentCfg, childCfg := DeriveParentChildConfigs(base, 300, 120)
	res := SplitParentChild(md, pages, parentCfg, childCfg)

	parents := DropDuplicateChunks(res.Parents,
		func(p Chunk) string { return p.ChunkHash() },
		func(p *Chunk, seq int) { p.Seq = seq })
	children := DropDuplicateChunks(res.Children,
		func(c ChildChunk) string { return c.ChunkHash() },
		func(c *ChildChunk, seq int) { c.Seq = seq })
	RenumberGlobal(parents, children)

	seen := map[int]bool{}
	for _, p := range parents {
		if seen[p.Seq] {
			t.Fatalf("duplicate seq %d after dedupe+renumber", p.Seq)
		}
		seen[p.Seq] = true
	}
	for _, c := range children {
		if seen[c.Seq] {
			t.Fatalf("duplicate seq %d after dedupe+renumber", c.Seq)
		}
		seen[c.Seq] = true
	}
}

// TestDropDuplicateChunks covers the document-wide dedupe that prevents a
// duplicate hash reaching COPY and aborting the transaction (BACKLOG R-24).
func TestDropDuplicateChunks(t *testing.T) {
	mk := func(seq int, text string) ChildChunk {
		return ChildChunk{Chunk: Chunk{Seq: seq, Content: text}, ParentHash: ""}
	}
	in := []ChildChunk{mk(0, "a"), mk(1, "b"), mk(2, "a"), mk(3, "c")}
	out := DropDuplicateChunks(in,
		func(c ChildChunk) string { return HashText(c.Content) },
		func(c *ChildChunk, seq int) { c.Seq = seq })

	if len(out) != 3 {
		t.Fatalf("expected 3 chunks after dedupe, got %d", len(out))
	}
	for i, c := range out {
		if c.Seq != i {
			t.Fatalf("seq not densely renumbered: %v", out)
		}
	}
	// Order is preserved: a, b, c.
	want := []string{"a", "b", "c"}
	for i, c := range out {
		if c.Content != want[i] {
			t.Fatalf("order not preserved: got %q want %q", c.Content, want[i])
		}
	}
}
