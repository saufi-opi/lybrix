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
	// DocFormat marks which ingestion format family this shard belongs to
	// (office/text shards are single synthetic shards — the whole file is
	// the unit). PDF remains the default zero value.
	DocFormat Format
}

// IsSynthetic reports whether the shard is a non-PDF single synthetic
// shard (EPUB / office / text / HTML): the whole file is the unit and the
// page map treats it as page 1..1.
func (r ParseRequest) IsSynthetic() bool { return r.DocFormat != FmtPDF }

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
	// Supports reports whether this parser has a fast path for f. The
	// routing decision is the implementation's capability, not a
	// caller-maintained format table.
	Supports(f Format) bool
}
