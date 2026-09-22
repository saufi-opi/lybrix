package pipeline

import "context"

// ctxAlias keeps the build-tag twins symmetric without duplicating imports.
type ctxAlias = context.Context

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
