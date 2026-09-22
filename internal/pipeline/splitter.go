package pipeline

import (
	"fmt"
	"sort"
)

// ShardBound is one shard's 1-based inclusive page range (splitter.py).
type ShardBound struct {
	Idx       int
	PageStart int // inclusive, 1-based
	PageEnd   int // inclusive
}

// FixedBounds returns fixed windows with a trailing 1-page overlap between
// consecutive shards. The embedder dedupes overlapping content by chunk
// hash. Line-by-line port of libs/parsing splitter.py fixed_bounds —
// including the shard_pages=1 anti-loop guard (a bugfix, mandatory).
func FixedBounds(pageCount, shardPages, overlap int) ([]ShardBound, error) {
	if pageCount < 1 {
		return nil, fmt.Errorf("page_count must be >= 1, got %d", pageCount)
	}
	if shardPages < 1 {
		return nil, fmt.Errorf("shard_pages must be >= 1, got %d", shardPages)
	}
	var bounds []ShardBound
	start := 1
	idx := 0
	for start <= pageCount {
		end := start + shardPages - 1
		if end > pageCount {
			end = pageCount
		}
		bounds = append(bounds, ShardBound{Idx: idx, PageStart: start, PageEnd: end})
		idx++
		if end < pageCount {
			start = end + 1 - overlap
		} else {
			start = end + 1
		}
		// The overlap shifts the next start back one page, but never
		// before the current start (guard against shard_pages=1 loops).
		if start <= bounds[len(bounds)-1].PageStart && end < pageCount {
			start = bounds[len(bounds)-1].PageStart + 1
		}
	}
	return bounds, nil
}

// ChapterAlignedBounds snaps shard boundaries to bookmarks (1-based target
// pages). Port of splitter.py chapter_aligned_bounds, including the
// degenerate-outline fallback (a bugfix, mandatory): an outline that yields
// zero or inverted bounds falls back to plain fixed bounds.
func ChapterAlignedBounds(outline []Bookmark, pageCount, shardPages int) ([]ShardBound, error) {
	if shardPages < 1 {
		shardPages = 1
	}
	clean := map[int]string{}
	for _, bm := range outline {
		p := bm.Page
		if p < 1 {
			p = 1
		}
		if p > pageCount {
			p = pageCount
		}
		if _, ok := clean[p]; !ok || bm.Title != "" {
			clean[p] = bm.Title
		}
	}
	pages := make([]int, 0, len(clean))
	for p := range clean {
		pages = append(pages, p)
	}
	sort.Ints(pages)

	var bounds []ShardBound
	idx := 0
	prevStart := 1
	for _, page := range pages {
		// shard the range [prev_start, page-1] with fixed windows
		if page > prevStart {
			span := page - prevStart
			fb, err := FixedBounds(span, shardPages, 0)
			if err != nil {
				return nil, err
			}
			for _, b := range fb {
				bounds = append(bounds, ShardBound{
					Idx: idx, PageStart: b.PageStart + prevStart - 1,
					PageEnd: b.PageEnd + prevStart - 1,
				})
				idx++
			}
		}
		prevStart = page
	}
	if prevStart <= pageCount {
		span := pageCount - prevStart + 1
		fb, err := FixedBounds(span, shardPages, 0)
		if err != nil {
			return nil, err
		}
		for _, b := range fb {
			bounds = append(bounds, ShardBound{
				Idx: idx, PageStart: b.PageStart + prevStart - 1,
				PageEnd: b.PageEnd + prevStart - 1,
			})
			idx++
		}
	}

	// Degenerate outline (e.g. every bookmark on page 1) can yield zero
	// or single-page shards only; fall back to plain fixed bounds.
	degenerate := len(bounds) == 0
	for _, b := range bounds {
		if b.PageEnd < b.PageStart {
			degenerate = true
			break
		}
	}
	if degenerate {
		return FixedBounds(pageCount, shardPages, 1)
	}
	return bounds, nil
}

// Bookmark is one extracted PDF outline entry (1-based page).
type Bookmark struct {
	Page  int
	Title string
}
