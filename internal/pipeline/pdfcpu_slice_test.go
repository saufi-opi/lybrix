package pipeline

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// SlicePDF on pages 2-4 → output page count is 3, and the text of slice page
// 1 equals the text of source page 2 (pages preserved 1:1, in order).
func TestSlicePDFMiddleRange(t *testing.T) {
	src := fixturePath(t, 6)
	dst := filepath.Join(t.TempDir(), "slice-2-4.pdf")

	n, err := SlicePDF(src, dst, 2, 4)
	if err != nil {
		t.Fatalf("SlicePDF: %v", err)
	}
	if n != 3 {
		t.Fatalf("SlicePDF returned n=%d, want 3", n)
	}
	got, err := PageCount(dst)
	if err != nil || got != 3 {
		t.Fatalf("sliced PageCount = %d, %v; want 3", got, err)
	}

	want, werr := PageTextChars(src, 2, 2)
	if werr != nil || len(want) != 1 {
		t.Fatalf("source page 2 text: %v, %v", want, werr)
	}
	gotCounts, gerr := PageTextChars(dst, 1, 1)
	if gerr != nil || len(gotCounts) != 1 {
		t.Fatalf("slice page 1 text: %v, %v", gotCounts, gerr)
	}
	// Extracted content should carry the page-2 sentinel, not page 1's.
	t2, terr := pageRawText(t, src, 2)
	if terr != nil {
		t.Fatalf("raw page 2: %v", terr)
	}
	t1, terr1 := pageRawText(t, dst, 1)
	if terr1 != nil {
		t.Fatalf("raw slice page 1: %v", terr1)
	}
	if !strings.Contains(t2, fixturePageText(2)) {
		t.Fatalf("source page 2 lost its sentinel during fixture generation")
	}
	if !strings.Contains(t1, fixturePageText(2)) {
		t.Fatalf("slice page 1 does not carry source page 2's sentinel (got %q)", t1)
	}
	if strings.Contains(t1, fixturePageText(1)) {
		t.Fatalf("slice page 1 leaked source page 1's sentinel")
	}
}

// Full-doc range 1..N: the function itself must still produce a valid N-page
// file (identity behavior); the caller skips slicing for this case.
func TestSlicePDFFullDoc(t *testing.T) {
	src := fixturePath(t, 6)
	dst := filepath.Join(t.TempDir(), "slice-full.pdf")

	n, err := SlicePDF(src, dst, 1, 6)
	if err != nil {
		t.Fatalf("SlicePDF: %v", err)
	}
	if n != 6 {
		t.Fatalf("SlicePDF returned n=%d, want 6", n)
	}
}

// Out-of-range end: TrimFile must error (the caller degrades to whole-file
// parsing) — no silent clamping in the helper.
func TestSlicePDFOutOfRangeErrors(t *testing.T) {
	src := fixturePath(t, 6)
	dst := filepath.Join(t.TempDir(), "slice-oob.pdf")

	if _, err := SlicePDF(src, dst, 5, 9); err == nil {
		t.Fatal("SlicePDF(5..9 of 6 pages) must error, got nil")
	}
}

// Encrypted fixture: the error surfaces (callers wrap with ClassifyPDFError).
func TestSlicePDFEncryptedErrors(t *testing.T) {
	src := encryptedFixturePath(t, 6)
	dst := filepath.Join(t.TempDir(), "slice-enc.pdf")

	_, err := SlicePDF(src, dst, 2, 4)
	if err == nil {
		t.Fatal("SlicePDF on an encrypted PDF must error, got nil")
	}
}

// pageRawText returns one page's extracted content-stream text as a string
// (test-only convenience over PageTextChars' machinery).
func pageRawText(t *testing.T, path string, page int) (string, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := api.ReadValidateAndOptimize(f, conf)
	if err != nil {
		return "", err
	}
	rd, err := pdfcpu.ExtractPageContent(ctx, page)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
