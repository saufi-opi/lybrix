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
// binding's job, literally: the pipeline asks the linked binding which
// formats it can convert (Supports / anydoc.FormatFromExtension) and routes
// accordingly — no hand-maintained format table in between.
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
// names. Capability-driven: anything the binding's extension table covers
// is mapped straight through — the routing decision is the binding's
// capability, never a hand-maintained table (the retired switch went stale
// and cost prod a 97s/docling EPUB tail while the binding sat there with
// ANYDOC_FORMAT_EPUB = 7; BACKLOG R-51). The only exceptions are explicit,
// each with a rationale:
//
//   - FmtTXT must never reach anydoc: the ABI has no plain-text format, and
//     the closest extension-table neighbor, CSV, would mis-parse plain text.
//   - FmtMD / FmtHTML have no meaningful ABI mapping.
//
// The two-tier parser already routes TXT/MD through the pure-Go passthrough
// and HTML straight to docling before anydoc is consulted (parser.go), so
// these exceptions are defense in depth, not the routing mechanism.
func anydocFormatFor(f Format) (anydoc.Format, bool) {
	switch f {
	case FmtTXT, FmtMD, FmtHTML:
		return "", false
	}
	return anydoc.FormatFromExtension(f.Ext())
}

// Supports reports whether the linked binding has a fast path for f.
func (AnyDocParser) Supports(f Format) bool {
	_, ok := anydocFormatFor(f)
	return ok
}
