//go:build anydoc

package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	anydoc "github.com/firecrawl/anydoc/go"
)

// The tagged lane: these tests run only where libanydoc_go.a is linked
// (`-tags anydoc` — CI test-anydoc job, ingest hosts). They are the
// empirical decision gate for capability-driven routing (BACKLOG R-51):
// the binding's claimed capabilities are proven against real bytes, not
// taken on faith.

const (
	epubSentinelHeading = "Chapter One: The Capability Gate"
	epubSentinelBody    = "The binding converts this sentence to markdown."
)

// writeEpubFixture builds a minimal EPUB 3 zip in memory: the OCF spec
// requires the "mimetype" member first and STORED (uncompressed), so it is
// written before anything else with zip.Store.
func writeEpubFixture(t *testing.T) []byte {
	t.Helper()

	container := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="uid">urn:uuid:6b1f4b0e-0000-4000-8000-000000000001</dc:identifier>
    <dc:title>Capability Gate</dc:title>
    <dc:language>en</dc:language>
    <meta property="dcterms:modified">2026-01-01T00:00:00Z</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="chapter1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="nav"/>
    <itemref idref="chapter1"/>
  </spine>
</package>
`
	nav := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Contents</title></head>
<body><nav epub:type="toc"><ol><li><a href="chapter1.xhtml">Chapter One</a></li></ol></nav></body>
</html>
`
	chapter := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>Chapter One</title></head>
<body><h1>` + epubSentinelHeading + `</h1><p>` + epubSentinelBody + `</p></body>
</html>
`

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	// mimetype: first member, STORED, no extra fields (OCF requirement).
	mh, err := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("mimetype header: %v", err)
	}
	if _, err := mh.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("mimetype body: %v", err)
	}
	for _, m := range []struct{ name, body string }{
		{"META-INF/container.xml", container},
		{"OEBPS/content.opf", opf},
		{"OEBPS/nav.xhtml", nav},
		{"OEBPS/chapter1.xhtml", chapter},
	} {
		fh, err := w.Create(m.name)
		if err != nil {
			t.Fatalf("create %s: %v", m.name, err)
		}
		if _, err := fh.Write([]byte(m.body)); err != nil {
			t.Fatalf("write %s: %v", m.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// testCtx returns a background context for the tagged tests.
func testCtx() context.Context { return context.Background() }

// newAnydocFormat builds the binding-side format value for tests.
func newAnydocFormat(t *testing.T, f anydoc.Format) *anydoc.Format {
	t.Helper()
	return &f
}

// TestAnyDocConvertsSyntheticEPUB is the R-51 decision gate: if this fails,
// EPUB routing must NOT flip — docling-first stays and EPUB becomes an
// explicit anydocFormatFor exception.
func TestAnyDocConvertsSyntheticEPUB(t *testing.T) {
	data := writeEpubFixture(t)

	// both capability queries must claim EPUB
	if f, ok := anydoc.FormatFromExtension("epub"); !ok || f != anydoc.FormatEpub {
		t.Fatalf("FormatFromExtension(epub) = (%q, %v)", f, ok)
	}
	if f, ok := anydoc.FormatFromBytes(data); !ok || f != anydoc.FormatEpub {
		t.Fatalf("FormatFromBytes(epub bytes) = (%q, %v) — content sniffing missed the EPUB container", f, ok)
	}

	md, err := anydoc.ToMarkdownBytes(data, newAnydocFormat(t, anydoc.FormatEpub))
	if err != nil {
		t.Fatalf("ToMarkdownBytes(EPUB) failed — the binding cannot convert EPUB: %v", err)
	}
	if len(md) < 64 {
		t.Fatalf("markdown too short (%d bytes): binding degraded the EPUB", len(md))
	}
	for _, s := range []string{epubSentinelHeading, epubSentinelBody} {
		if !strings.Contains(md, s) {
			t.Fatalf("markdown missing sentinel %q — output garbled:\n%s", s, md)
		}
	}
}

// TestRoutingAgreesWithBindingCapability pins Supports against the
// binding's own claims. The positive pins (PDF/DOCX/PPTX/XLSX) and the
// text-family pins exist so a binding-side capability change flips this
// test instead of silently flipping production routing.
func TestRoutingAgreesWithBindingCapability(t *testing.T) {
	exceptions := map[Format]bool{FmtTXT: true, FmtMD: true, FmtHTML: true}
	for _, f := range allFormats() {
		_, bindingOK := anydoc.FormatFromExtension(f.Ext())
		want := bindingOK && !exceptions[f]
		if got := (AnyDocParser{}).Supports(f); got != want {
			t.Errorf("Supports(%v) = %v, want %v (binding ext query ok=%v)", f, got, want, bindingOK)
		}
	}
	// hard positive pins: the four fast formats must never silently
	// regress to docling because the binding query and Supports drift
	// together.
	for _, f := range []Format{FmtPDF, FmtDOCX, FmtPPTX, FmtXLSX} {
		if !(AnyDocParser{}).Supports(f) {
			t.Errorf("Supports(%v) = false — fast format lost; check binding capability drift", f)
		}
	}
	// hard negative pins: the text family stays false regardless of
	// binding drift (TXT would mis-parse as CSV; MD/HTML have no ABI
	// mapping).
	for _, f := range []Format{FmtTXT, FmtMD, FmtHTML} {
		if (AnyDocParser{}).Supports(f) {
			t.Errorf("Supports(%v) = true — text family must never route to anydoc", f)
		}
	}
}

// TestTwoTierRoutesEPUBThroughAnyDoc is the end-to-end flip proof: an
// EPUB shard routes through the fast path even with docling unreachable
// (the old special-case routing sent EPUB straight to docling and would
// fail here on the unreachable endpoint).
func TestTwoTierRoutesEPUBThroughAnyDoc(t *testing.T) {
	tp := NewTwoTierParser("http://127.0.0.1:1", 50, 20, true) // docling endpoint unreachable
	res, err := tp.Parse(testCtx(), ParseRequest{
		PDFBytes:  writeEpubFixture(t),
		PageStart: 1,
		PageEnd:   1,
		DocFormat: FmtEPUB,
	})
	if err != nil {
		t.Fatalf("EPUB parse through the two-tier parser failed: %v", err)
	}
	if res.Engine != "anydoc" {
		t.Fatalf("Engine = %q, want \"anydoc\" — EPUB did not route through the fast path", res.Engine)
	}
	if len(res.Markdown) == 0 {
		t.Fatal("anydoc returned empty markdown for the EPUB fixture")
	}
}
