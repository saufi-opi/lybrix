package pipeline

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	pdffont "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/font"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// fixturePageText is the per-page sentinel text baked into the generated
// 6-page fixture: slice tests match on it to prove the right pages moved.
func fixturePageText(page int) string {
	return fmt.Sprintf("LYBRIX-FIXTURE-SENTINEL-PAGE-%02d unique body content for page %d.", page, page)
}

// writePageFixture generates a n-page PDF with one visible sentinel text line
// per page into dst, using pdfcpu's own test-support primitives (the same
// route pkg/api/test uses to write its demo PDFs). Deterministic content;
// generation runs once per test binary via TestMain-style caching on disk.
func writePageFixture(tb testing.TB, dst string, n int) {
	tb.Helper()

	xRefTable, err := pdfcpu.CreateXRefTableWithRootDict()
	if err != nil {
		tb.Fatalf("create xref table: %v", err)
	}
	rootDict, err := xRefTable.Catalog()
	if err != nil {
		tb.Fatalf("catalog: %v", err)
	}

	// Root page tree node with the shared font resource.
	fIndRef, err := pdffont.EnsureFontDict(xRefTable, "Courier", "", "", false, nil)
	if err != nil {
		tb.Fatalf("ensure font dict: %v", err)
	}
	rootPagesDict := types.Dict(map[string]types.Object{
		"Type":     types.Name("Pages"),
		"Count":    types.Integer(n),
		"MediaBox": types.RectForFormat("A4").Array(),
		"Resources": types.Dict(map[string]types.Object{
			"Font": types.Dict(map[string]types.Object{
				"F99": *fIndRef,
			}),
		}),
	})
	rootPageIndRef, err := xRefTable.IndRefForNewObject(rootPagesDict)
	if err != nil {
		tb.Fatalf("root pages indref: %v", err)
	}

	kids := types.Array{}
	for p := 1; p <= n; p++ {
		page := model.Page{MediaBox: types.RectForFormat("A4"), Fm: model.FontMap{}, Buf: new(bytes.Buffer)}
		k := page.Fm.EnsureKey("Courier")
		td := model.TextDescriptor{
			Text:     fixturePageText(p),
			FontName: "Courier",
			FontKey:  k,
			FontSize: 12,
			Scale:    1.,
			ScaleAbs: true,
			X:        60,
			Y:        700,
		}
		if _, err := model.WriteMultiLine(xRefTable, page.Buf, page.MediaBox, nil, td); err != nil {
			tb.Fatalf("render page %d: %v", p, err)
		}

		pageDict := types.Dict(map[string]types.Object{
			"Type":   types.Name("Page"),
			"Parent": *rootPageIndRef,
			"Resources": types.Dict(map[string]types.Object{
				"Font": types.Dict(map[string]types.Object{
					"F99": *fIndRef,
				}),
			}),
		})
		sd, err := xRefTable.NewStreamDictForBuf(page.Buf.Bytes())
		if err != nil {
			tb.Fatalf("stream dict page %d: %v", p, err)
		}
		if err := sd.Encode(); err != nil {
			tb.Fatalf("encode stream page %d: %v", p, err)
		}
		contentIndRef, err := xRefTable.IndRefForNewObject(*sd)
		if err != nil {
			tb.Fatalf("content indref page %d: %v", p, err)
		}
		pageDict.Insert("Contents", *contentIndRef)

		pageIndRef, err := xRefTable.IndRefForNewObject(pageDict)
		if err != nil {
			tb.Fatalf("page indref %d: %v", p, err)
		}
		kids = append(kids, *pageIndRef)
	}
	rootPagesDict.Insert("Kids", kids)
	rootDict.Insert("Pages", *rootPageIndRef)

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	if err := api.CreatePDFFile(xRefTable, dst, conf); err != nil {
		tb.Fatalf("create pdf: %v", err)
	}
}

// fixturePath materializes the n-page fixture once per process in t.TempDir.
func fixturePath(tb testing.TB, n int) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), fmt.Sprintf("fixture-%dp.pdf", n))
	writePageFixture(tb, p, n)
	return p
}

// encryptedFixturePath writes the n-page fixture encrypted with an owner
// password (pdfcpu EncryptFile).
func encryptedFixturePath(tb testing.TB, n int) string {
	tb.Helper()
	src := fixturePath(tb, n)
	dst := filepath.Join(tb.TempDir(), "fixture-encrypted.pdf")
	conf := model.NewDefaultConfiguration()
	conf.OwnerPW = "secret"
	conf.UserPW = "secret"
	if err := api.EncryptFile(src, dst, conf); err != nil {
		tb.Fatalf("encrypt fixture: %v", err)
	}
	return dst
}

