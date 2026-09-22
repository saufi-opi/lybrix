package service

import (
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpu "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"

	"github.com/saufi-opi/lybrix/internal/pipeline"
)

// ExtractBookmarks is best-effort outline extraction via pdfcpu; [] when
// absent. Kept tolerant: a broken outline must never fail the split stage —
// fixed bounds are always a valid fallback (splitter.py extract_outline).
func ExtractBookmarks(pdfPath string) []pipeline.Bookmark {
	f, err := os.Open(pdfPath)
	if err != nil {
		return []pipeline.Bookmark{}
	}
	defer f.Close()
	bms, err := api.Bookmarks(f, nil)
	if err != nil {
		return []pipeline.Bookmark{}
	}
	out := []pipeline.Bookmark{}
	var walk func(nodes []pdfcpu.Bookmark)
	walk = func(nodes []pdfcpu.Bookmark) {
		for _, bm := range nodes {
			if bm.PageFrom > 0 {
				// 7-bit + UTF-16 title decode is pdfcpu's job; tolerant pass-through
				out = append(out, pipeline.Bookmark{Page: bm.PageFrom, Title: bm.Title})
			}
			walk(bm.Kids)
		}
	}
	walk(bms)
	return out
}
