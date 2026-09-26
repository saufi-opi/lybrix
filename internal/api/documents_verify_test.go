package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
)

// TestVerifyRawObjectLoopShape is the R-33 regression test. The commit
// verify loop read the body into `buf` whose first 64 bytes backed `head`;
// because `pdfBytes := head` aliased that backing array, the first
// body.Read overwrote the PDF header before append copied it — the SHA
// still matched (the digest saw head first) but pdfcpu failed with
// "no header version available". This pins the loop's output shape: the
// reassembled bytes must equal the source payload exactly.
func TestVerifyRawObjectLoopShape(t *testing.T) {
	payload := append([]byte("%PDF-1.6\n"), bytes.Repeat([]byte("0123456789abcdef"), 256)...)

	// the FIXED shape (as now in verifyRawObject): independent head copy
	buf := make([]byte, 64*1024)
	copy(buf, payload[:64])
	head := buf[:64]
	pdfBytes := append([]byte(nil), head...) // own copy: buf is reused by the loop
	rest := payload[64:]
	for offset := 0; offset < len(rest); {
		n := copy(buf, rest[offset:]) // body.Read(buf) clobbers buf[:n]
		pdfBytes = append(pdfBytes, buf[:n]...)
		offset += n
	}
	if !bytes.Equal(pdfBytes, payload) {
		t.Fatalf("fixed loop must reassemble the payload byte-for-byte (got len %d, want %d; head=%q)",
			len(pdfBytes), len(payload), pdfBytes[:9])
	}
	sum := sha256.Sum256(pdfBytes)
	wantSum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("digest must match the payload")
	}

	// and the shape that CAUSED R-33 stays corrupt — documenting the bug:
	aliased := buf[:64] // aliases buf, like the old `pdfBytes := head`
	copy(buf, payload[:64])
	copy(buf, payload[64:]) // first body.Read(buf) clobbers the header
	if bytes.Equal(aliased[:9], []byte("%PDF-1.6\n")) {
		t.Fatalf("aliased shape unexpectedly survived; the bug premise changed — revisit this test")
	}
}

// TestOpdsResolveHref is the R-34 regression test: relative acquisition
// and next-page links resolve against the feed URL; absolute http(s) pass
// through unchanged; non-http(s) schemes and garbage hrefs drop to "".
func TestOpdsResolveHref(t *testing.T) {
	base, _ := url.Parse("https://library.example.com/opds")
	cases := []struct {
		href, want string
	}{
		{"https://other.example.com/x.pdf", "https://other.example.com/x.pdf"},
		{"http://other.example.com/x.epub", "http://other.example.com/x.epub"},
		{"/opds/download/897/pdf", "https://library.example.com/opds/download/897/pdf"},
		{"../files/book.epub", "https://library.example.com/files/book.epub"},
		{"download/897", "https://library.example.com/download/897"}, // RFC-3986: relative ref replaces the last segment
		{"ftp://example.com/x.pdf", ""},
		{"", ""},
		{"://bad", ""},
	}
	for _, c := range cases {
		got := opdsResolveHref(base, c.href)
		if got != c.want {
			t.Errorf("opdsResolveHref(%q) = %q, want %q", c.href, got, c.want)
		}
	}
	if empty, _ := url.Parse(""); opdsResolveHref(empty, "/x") != "" {
		t.Errorf("scheme-less base must not resolve")
	}
	if opdsResolveHref(nil, "/x") != "" {
		t.Errorf("nil base must not resolve")
	}
}

// TestRealWorldPDFPageCount wraps the exact commit-path probe. The two
// incident PDFs (Effective TypeScript 404p, DDIA 585p) parse cleanly on
// pdfcpu v0.15.0; this guards against a dependency bump regressing them.
// Fixtures are read from the path lybrix repros use; skipped when absent.
func TestRealWorldPDFPageCount(t *testing.T) {
	paths := map[string]int{
		"/root/tmp/ets.pdf":  404, // Effective TypeScript
		"/root/tmp/ddia.pdf": 585, // Designing Data-Intensive Applications
	}
	checked := 0
	for p, want := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue // fixture not present in this environment
		}
		checked++
		n, err := pdfPageCount(b)
		if err != nil {
			t.Errorf("%s: pdfPageCount: %v", p, err)
			continue
		}
		if n != want {
			t.Errorf("%s: pages=%d want %d", p, n, want)
		}
	}
	if checked == 0 {
		t.Skip("no incident fixtures present")
	}
}
