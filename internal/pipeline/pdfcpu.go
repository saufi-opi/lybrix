// Package pipeline: the ingest pipeline (splitter, OCR gate, parsers,
// chunker, embedder) — Go ports of libs/parsing, libs/chunking, and the
// worker stages.
package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// PageCount opens a PDF (bytes or file) and returns its page count.
// PDF_ENCRYPTED / PDF_CORRUPT classification lives with the caller via
// ClassifyPDFError.
func PageCount(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return PageCountBytes(b)
}

// PageCountBytes is PageCount over in-memory bytes (commit verify path).
func PageCountBytes(b []byte) (int, error) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := api.ReadAndValidate(bytes.NewReader(b), conf)
	if err == nil && ctx.PageCount > 0 {
		return ctx.PageCount, nil
	}
	// Fallback for real-world PDFs with damaged/shifted xref tables:
	// Count /Type /Page (excluding /Pages) objects in PDF byte stream.
	count := countPDFPageObjects(b)
	if count > 0 {
		return count, nil
	}
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func countPDFPageObjects(b []byte) int {
	// Search for `/Type /Page` or `/Type/Page`
	n := 0
	l := len(b)
	for i := 0; i < l-10; i++ {
		if b[i] == '/' && b[i+1] == 'T' && b[i+2] == 'y' && b[i+3] == 'p' && b[i+4] == 'e' {
			j := i + 5
			for j < l && (b[j] == ' ' || b[j] == '	' || b[j] == '' || b[j] == '
') {
				j++
			}
			if j+5 < l && b[j] == '/' && b[j+1] == 'P' && b[j+2] == 'a' && b[j+3] == 'g' && b[j+4] == 'e' {
				// Ensure it's not `/Pages`
				if b[j+5] != 's' {
					n++
					i = j + 5
				}
			}
		}
	}
	return n
}

// PageTextChars returns per-page text-layer character counts for a 1-based
// inclusive page range (ocr_gate.py's page_text_chars, via pdfcpu's pure-Go
// content extraction). One failed page counts as 0 chars — the gate is a
// mean threshold, not a hard verifier.
func PageTextChars(path string, pageStart, pageEnd int) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	conf := model.NewDefaultConfiguration()
	ctx, err := api.ReadValidateAndOptimize(f, conf)
	if err != nil {
		return nil, err
	}
	if pageEnd > ctx.PageCount {
		pageEnd = ctx.PageCount
	}
	counts := make([]int, 0, pageEnd-pageStart+1)
	for p := pageStart; p <= pageEnd; p++ {
		rd, err := pdfcpu.ExtractPageContent(ctx, p)
		if err != nil {
			counts = append(counts, 0)
			continue
		}
		b, err := io.ReadAll(rd)
		if err != nil {
			counts = append(counts, 0)
			continue
		}
		counts = append(counts, len(b))
	}
	return counts, nil
}

// ClassifyPDFError maps pdfcpu open failures onto the 1.0 taxonomy:
// encrypted → PDF_ENCRYPTED, everything else → PDF_CORRUPT.
func ClassifyPDFError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case containsFold(msg, "password"), containsFold(msg, "encrypt"):
		return "PDF_ENCRYPTED"
	default:
		return "PDF_CORRUPT"
	}
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 32
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

var _ = fmt.Sprintf
