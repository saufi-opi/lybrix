// Package chunker turns parsed markdown into retrieval chunks.
//
// The central invariant is that Chunk.Content is a VERBATIM rune-slice of the
// source markdown: End-Start == utf8.RuneCountInString(Content) always holds.
// Nothing in this package re-joins words or otherwise rewrites the parser's
// output, so tables, fenced code, list nesting and paragraph breaks survive
// into the index intact (BACKLOG R-26). Section context — a heading breadcrumb
// or a table's column header — is carried OUT OF BAND (ContextHeader, or the
// zero-width synthetic units in header_tracker.go) and prepended only at embed
// time, which is what lets the position invariant hold.
//
// Splitting is adaptive: a document profiler picks an ordered chain of
// strategies (heading → heuristic → legacy) and a validator rejects an
// obviously broken result so the next tier runs.
//
// Ported from Tencent WeKnora (MIT) internal/infrastructure/chunker, with the
// docreader coupling and preview-endpoint plumbing removed.
package chunker

import (
	"strings"
	"unicode/utf8"
)

// Chunk is one piece of split text with its source position.
//
// Content holds exactly the text from the original document between Start and
// End (rune offsets), so End-Start == utf8.RuneCountInString(Content). Code
// that reconstructs a document from its chunks, or that maps a chunk back to a
// source page, relies on that invariant.
//
// ContextHeader is a separately-tracked context string (a markdown heading
// breadcrumb, or a table's column header) that is prepended at embed time but
// is NOT part of Content.
type Chunk struct {
	Content       string
	ContextHeader string
	Seq           int
	Start         int // rune offset into the document
	End           int // rune offset into the document

	// PageStart/PageEnd are the 1-based inclusive source pages this chunk
	// covers, derived from the stitch page map. Nil when no page map exists.
	PageStart *int
	PageEnd   *int

	// SyntheticPrefixRunes counts leading runes of Content that are generated
	// rather than copied from the source — currently only a table header
	// re-injected across a chunk boundary.
	//
	// For a chunk without an injected header this is 0 and Content is exactly
	// the source text between Start and End. Otherwise the invariant is:
	//
	//	End-Start == runeLen(Content) - SyntheticPrefixRunes
	//	Content[SyntheticPrefixRunes:] == source[Start:End]
	//
	// Positions still describe the SOURCE portion, so page attribution stays
	// correct (an injected header is repeated from an earlier page).
	SyntheticPrefixRunes int
}

// EmbeddingContent returns the text to feed the embedding model: the
// ContextHeader prepended (when set) plus the chunk content, with the body's
// surrounding whitespace trimmed so boundary newlines don't dilute the vector.
// Inner whitespace is preserved.
func (c Chunk) EmbeddingContent() string {
	body := strings.TrimSpace(c.Content)
	if c.ContextHeader == "" {
		return body
	}
	return c.ContextHeader + "\n\n" + body
}

// ChunkHash is the content hash used for dedupe and as the idempotency key.
// Whitespace-normalized, so it is stable across the verbatim-text change for a
// chunk covering the same words — see HashText.
func (c Chunk) ChunkHash() string { return HashText(c.Content) }

// ChildChunk is a Chunk that knows its parent.
type ChildChunk struct {
	Chunk
	// ParentIndex is an index into ParentChildResult.Parents, or -1 when the
	// child has no parent row (a parent that produced exactly one identical
	// child is not materialised).
	ParentIndex int
	// ParentHash is the parent's chunk hash, for resolving parent_id after
	// dedupe renumbers the parent set.
	ParentHash string
}

// ParentChildResult is the two-level chunking output. Parents carry the wide
// context window (no embedding); children are the embedded retrieval units.
type ParentChildResult struct {
	Parents  []Chunk
	Children []ChildChunk
}

// runeLen returns the number of runes in s.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// splitRunes splits text into its runes. One allocation, shared by the tiers.
func splitRunes(text string) []rune { return []rune(text) }
