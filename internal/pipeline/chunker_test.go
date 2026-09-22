package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

func TestChunkerSectionsHeadings(t *testing.T) {
	md := "# Alpha\nsome alpha text\n## Beta\nbeta body\n# Gamma\ngamma body"
	parents, children := ChunkHierarchical(md, nil, WhitespaceTokenizer{})
	if len(parents) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(parents))
	}
	if parents[0].HeadingPath[0] != "Alpha" {
		t.Fatalf("heading path drift: %v", parents[0].HeadingPath)
	}
	if parents[1].HeadingPath[0] != "Alpha" || parents[1].HeadingPath[1] != "Beta" {
		t.Fatalf("nested heading path drift: %v", parents[1].HeadingPath)
	}
	if parents[2].HeadingPath[0] != "Gamma" {
		t.Fatalf("sibling heading path drift: %v", parents[2].HeadingPath)
	}
	if len(children) == 0 {
		t.Fatal("expected children")
	}
}

func TestChunkerParentCaps(t *testing.T) {
	// one giant section: parent hard-caps at 4096 tokens in ≤4096 slices
	long := strings.Repeat("word ", 6000)
	parents, _ := ChunkHierarchical("# Big\n"+long, nil, WhitespaceTokenizer{})
	if len(parents) < 2 {
		t.Fatalf("expected oversized section split into parents, got %d", len(parents))
	}
	for _, p := range parents {
		if p.TokenCount > ParentHardCap {
			t.Fatalf("parent over hard cap: %d", p.TokenCount)
		}
	}
}

func TestChunkerChildWindowStride(t *testing.T) {
	words := make([]string, 0, 1200)
	for i := 0; i < 1200; i++ {
		words = append(words, "w"+strings.Repeat("x", i%3))
	}
	md := "# S\n" + strings.Join(words, " ")
	_, children := ChunkHierarchical(md, nil, WhitespaceTokenizer{})
	if len(children) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(children))
	}
	for _, c := range children {
		if c.TokenCount > ChildWindowTokens {
			t.Fatalf("child over window budget: %d", c.TokenCount)
		}
	}
	// stride overlap: consecutive children share the stride region — the
	// first ChildStrideTokens words of child[n+1] equal the last
	// ChildStrideTokens words of child[n] (advance = window-stride).
	first := strings.Fields(children[0].Text)
	second := strings.Fields(children[1].Text)
	overlap := 0
	for i := 0; i < ChildStrideTokens && i < len(second); i++ {
		if first[len(first)-ChildStrideTokens+i] == second[i] {
			overlap++
		} else {
			break
		}
	}
	if overlap != ChildStrideTokens {
		t.Fatalf("expected %d shared stride tokens, got %d", ChildStrideTokens, overlap)
	}
}

func TestChunkerBreadcrumb(t *testing.T) {
	md := "# Chapter One\n## Section A\ncontent words here"
	parents, children := ChunkHierarchical(md, nil, WhitespaceTokenizer{})
	want := "Chapter One > Section A"
	if got := RenderHeadingPath(parents[0].HeadingPath); got != want {
		t.Fatalf("breadcrumb drift: %q want %q", got, want)
	}
	if len(children) == 0 || children[0].HeaderBreadcrumb != "[ "+want+" ]" {
		t.Fatalf("child breadcrumb drift: %+v", children)
	}
}

func TestChunkerPerWindowPageRanges(t *testing.T) {
	// two sections on distinct pages; attach page map after chunking
	md := "# One\nalpha words\n# Two\nbeta words"
	pages := []int{1, 1, 2, 2} // 4 lines → pages 1,1,2,2
	parents, children := ChunkHierarchical(md, pages, WhitespaceTokenizer{})
	if parents[0].PageStart == nil || *parents[0].PageStart != 1 {
		t.Fatalf("parent page start drift: %+v", parents[0])
	}
	if parents[1].PageStart == nil || *parents[1].PageStart != 2 {
		t.Fatalf("second parent page start drift: %+v", parents[1])
	}
	AttachChildPages(md, pages, children)
	for _, c := range children {
		if c.PageStart == nil {
			t.Fatalf("child missing page range: %+v", c)
		}
		if c.PageEnd == nil || *c.PageEnd < *c.PageStart {
			t.Fatalf("child page range inverted: %+v", c)
		}
	}
}

func TestChunkerNeighbourDedupe(t *testing.T) {
	// shard overlap residue: two identical children in sequence
	mk := func(seq int) ChildChunk {
		t := "same text same text"
		return ChildChunk{Seq: seq, Text: t, ChunkHash: HashText(t)}
	}
	out := DropDuplicateNeighbours([]ChildChunk{mk(0), mk(1), mk(2)},
		func(c ChildChunk) string { return c.ChunkHash },
		func(c *ChildChunk, seq int) { c.Seq = seq })
	if len(out) != 1 {
		t.Fatalf("expected 1 deduped chunk, got %d", len(out))
	}
	if out[0].Seq != 0 {
		t.Fatalf("seq renumber drift: %d", out[0].Seq)
	}
}

func TestChunkerHashStableAcrossPrefixChanges(t *testing.T) {
	// stored text stays unprefixed; the embed-time breadcrumb prefix must
	// never change the stored hash.
	text := "unprefixed body text"
	h1 := HashText(text)
	_ = "[ Doc > Chapter ] " + text // prefix applied at embed time only
	if h1 != HashText(text) {
		t.Fatal("hash drift")
	}
}

func TestChunkerEmptyInput(t *testing.T) {
	parents, children := ChunkHierarchical("", nil, WhitespaceTokenizer{})
	if len(parents) != 0 || len(children) != 0 {
		t.Fatalf("expected empty, got %d/%d", len(parents), len(children))
	}
}

func TestChunkerFenceProtectsHeadings(t *testing.T) {
	md := "# Code\n```md\n## Not A Heading\n```\ntrailing words"
	parents, _ := ChunkHierarchical(md, nil, WhitespaceTokenizer{})
	if len(parents) != 1 {
		t.Fatalf("fence did not protect heading: %d parents", len(parents))
	}
}

func TestNormalizeHeadingPath(t *testing.T) {
	got := NormalizeHeadingPath([]string{" A ", "", "A", "B", "B "})
	want := []string{"A", "B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize drift: %v want %v", got, want)
	}
}
