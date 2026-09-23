//go:build anydoc

// The real CGO fast path: a thin adapter over third_party/anydoc-go (the
// Firecrawl anydoc binding). Linked only with `-tags anydoc`; the default
// build compiles anydoc_stub.go so CI never needs the Rust archive.
//
// Thread safety: the binding pins every CGO call to its OS thread with
// runtime.LockOSThread (anydoc.call) — the Rust side keeps thread-local
// error registers — so the adapter does not pin again.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"time"

	anydoc "github.com/firecrawl/anydoc/go"
)

// AnyDocParser is the in-process conversion engine.
type AnyDocParser struct{}

// Available reports whether the fast path is linked in.
func (AnyDocParser) Available() bool { return true }

// Parse converts one shard to GFM markdown in-process. Sub-100ms per shard
// is the expected steady state (blueprint §4.2). Format mapping is the
// binding's job: pipeline.Format → anydoc.Format → the ABI's C tags
// (anydoc.h ANYDOC_FORMAT_*); pdf arrives as ANYDOC_FORMAT_PDF (3), not the
// 0 the retired fileformat.go table claimed.
func (AnyDocParser) Parse(_ context.Context, req ParseRequest) (ParseResult, error) {
	started := time.Now()

	src := req.PDFBytes
	if len(src) == 0 {
		b, err := os.ReadFile(req.PDFPath)
		if err != nil {
			return ParseResult{}, err
		}
		src = b
	}
	format, ok := anydocFormatFor(req.DocFormat)
	if !ok {
		return ParseResult{}, fmt.Errorf("anydoc: format %d has no fast path", int(req.DocFormat))
	}
	md, err := anydoc.ToMarkdownBytes(src, &format)
	if err != nil {
		// *anydoc.ConvertError: typed taxonomy; never the unavailable
		// sentinel, so TwoTierParser falls through to docling on failure.
		return ParseResult{}, err
	}
	return ParseResult{
		Markdown:   md,
		Engine:     "anydoc",
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

// anydocFormatFor maps the pipeline format table onto the binding's Format
// names. Single source of truth for the crossover; anything unmapped here
// (EPUB, HTML, MD) has no fast path by design (parser.go routing).
func anydocFormatFor(f Format) (anydoc.Format, bool) {
	switch f {
	case FmtPDF:
		return anydoc.FormatPdf, true
	case FmtDOCX:
		return anydoc.FormatDocx, true
	case FmtPPTX:
		return anydoc.FormatPptx, true
	case FmtXLSX:
		return anydoc.FormatXlsx, true
	}
	// FmtTXT (and EPUB/HTML/MD) deliberately unmapped: the ABI has no plain
	// text format (the closest, CSV, would mis-parse text), and the two-tier
	// parser routes TXT/MD through the pure-Go passthrough before anydoc is
	// consulted.
	return "", false
}
