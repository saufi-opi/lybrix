package pipeline

import "context"

// ParseRequest is one shard-level parse unit (blueprint §4.2: a whole
// 16–24 page shard is passed in memory — never per-page requests).
type ParseRequest struct {
	// PDFPath is the local PDF file (cache or temp download).
	PDFPath string
	// PDFBytes, when non-empty, is parsed straight from memory.
	PDFBytes   []byte
	PageStart  int
	PageEnd    int
	Attempt    int
	ShardPages int
	// TextOnly (retry ladder ≥4): docling with table structure off.
	TextOnly bool
	// NeedOCR (gate verdict): skip the born-digital fast path.
	NeedOCR bool
	// SkipAnyDoc forces the docling tier (EPUB: anydoc cannot open a zip
	// container; docling parses EPUB natively).
	SkipAnyDoc bool
	// IsEpub marks the shard as an EPUB-derived single synthetic shard:
	// docling returns the whole book as one markdown unit and the page
	// map treats it as page 1..1.
	IsEpub bool
}

// ParseResult is one parsed shard.
type ParseResult struct {
	Markdown         string
	NeedsOCR         bool
	MeanCharsPerPage float64
	DurationMS       int64
	PeakRSSMB        int
	Engine           string // "anydoc" | "docling"
}

// Parser is the parse-stage interface both engines satisfy.
type Parser interface {
	Parse(ctx context.Context, req ParseRequest) (ParseResult, error)
	Available() bool
}
