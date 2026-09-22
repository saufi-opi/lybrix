package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Format is the ingestion format family (Workstream 2's shared table). The
// anydoc fast path consumes the numeric codes directly: PDF=0 DOCX=1
// PPTX=2 XLSX=3 TXT=4 (third_party/anydoc-go/include/anydoc.h).
type Format int

const (
	FmtPDF Format = iota
	FmtEPUB
	FmtDOCX
	FmtPPTX
	FmtXLSX
	FmtTXT
	FmtMD
	FmtHTML
)

// ErrUnsupportedFormat marks a sniffed-but-unknown body.
var ErrUnsupportedFormat = fmt.Errorf("unsupported file type")

// anydocCode is the CGO fast path's format code; -1 = not handled by anydoc.
func (f Format) anydocCode() int {
	switch f {
	case FmtPDF:
		return 0
	case FmtDOCX:
		return 1
	case FmtPPTX:
		return 2
	case FmtXLSX:
		return 3
	case FmtTXT:
		return 4
	}
	return -1
}

// AnydocCode exposes the CGO fast-path code (-1 = not anydoc-handled).
func (f Format) AnydocCode() int { return f.anydocCode() }

// MimeType is the wire mime type stored on documents rows / sent to MinIO.
func (f Format) MimeType() string {
	switch f {
	case FmtPDF:
		return "application/pdf"
	case FmtEPUB:
		return "application/epub+zip"
	case FmtDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FmtPPTX:
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case FmtXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case FmtMD:
		return "text/markdown"
	case FmtHTML:
		return "text/html"
	}
	return "text/plain"
}

// Ext is the canonical file extension (no dot).
func (f Format) Ext() string {
	switch f {
	case FmtPDF:
		return "pdf"
	case FmtEPUB:
		return "epub"
	case FmtDOCX:
		return "docx"
	case FmtPPTX:
		return "pptx"
	case FmtXLSX:
		return "xlsx"
	case FmtMD:
		return "md"
	case FmtHTML:
		return "html"
	}
	return "txt"
}

// extFormat maps a filename extension to a Format; "" when unknown.
func extFormat(filename string) Format {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), ".")) {
	case "pdf":
		return FmtPDF
	case "epub":
		return FmtEPUB
	case "docx":
		return FmtDOCX
	case "pptx":
		return FmtPPTX
	case "xlsx":
		return FmtXLSX
	case "md", "markdown":
		return FmtMD
	case "htm", "html":
		return FmtHTML
	case "txt", "text":
		return FmtTXT
	}
	return -1
}

// DetectFormat sniffs a document's format from its magic bytes (disambiguated
// by filename extension inside the zip family) — the single source of truth
// for both upload paths (stream ingest and presign+commit).
//
// Rules: "%PDF-" → PDF; "PK\x03\x04" → zip family by extension (EPUB
// additionally verifiable by its mimetype member name in the first bytes);
// otherwise valid UTF-8 text → .md/.txt by extension, "<html"/"<!doctype"
// → HTML. Unknown → error.
func DetectFormat(head []byte, filename string) (Format, error) {
	if len(head) >= 5 && string(head[:5]) == "%PDF-" {
		return FmtPDF, nil
	}
	if len(head) >= 4 && string(head[:4]) == "PK\x03\x04" {
		// zip family: disambiguate by extension. EPUB is additionally
		// verifiable by its uncompressed "mimetype" member name at offset 30.
		switch ef := extFormat(filename); ef {
		case FmtEPUB, FmtDOCX, FmtPPTX, FmtXLSX:
			return ef, nil
		}
		// extensionless zip: EPUB's mimetype member sits right after the
		// fixed 30-byte local-file header.
		if len(head) >= 38 && string(head[30:38]) == "mimetype" {
			return FmtEPUB, nil
		}
		return -1, fmt.Errorf("%w: zip container without a known extension", ErrUnsupportedFormat)
	}
	// text family: valid UTF-8 decides; extension picks md vs txt; HTML by
	// content regardless of extension.
	if utf8.Valid(head) && looksTextual(head) {
		lower := strings.ToLower(string(head[:min(200, len(head))]))
		trimmed := strings.TrimLeft(lower, "\xef\xbb\xbf \t\r\n")
		if strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") {
			return FmtHTML, nil
		}
		switch extFormat(filename) {
		case FmtMD:
			return FmtMD, nil
		case FmtHTML:
			return FmtHTML, nil
		case FmtTXT:
			return FmtTXT, nil
		}
		// extensionless text: default to plain text
		return FmtTXT, nil
	}
	return -1, ErrUnsupportedFormat
}

// looksTextual rejects binary garbage that happens to be valid UTF-8:
// text documents are overwhelmingly printable.
func looksTextual(head []byte) bool {
	n := min(len(head), 512)
	printable := 0
	for _, b := range head[:n] {
		if b == '\t' || b == '\n' || b == '\r' || (b >= 0x20 && b != 0x7f) {
			printable++
		}
	}
	if n == 0 {
		return false
	}
	return printable*100 >= n*90
}

// ParseSourceName is the docling/anydoc upload filename for a format —
// docling-serve's format sniffing fights the extension otherwise.
func ParseSourceName(f Format) string {
	return "source." + f.Ext()
}

// FormatFromMime maps a stored mime_type back onto the format table — the
// parser's routing key. Unknown/empty mime → PDF (the historical default:
// rows predating multi-format ingestion are all PDFs).
func FormatFromMime(mime string) Format {
	switch mime {
	case "application/epub+zip":
		return FmtEPUB
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return FmtDOCX
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return FmtPPTX
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return FmtXLSX
	case "text/markdown":
		return FmtMD
	case "text/html":
		return FmtHTML
	case "text/plain":
		return FmtTXT
	}
	return FmtPDF
}
