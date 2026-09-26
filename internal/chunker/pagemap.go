// pagemap.go maps rune offsets in the stitched markdown to source page numbers,
// using the per-line page map produced by pipeline.Stitch.
//
// This is the whole reason child page ranges work at all (BACKLOG R-27): because
// Chunk.Content is a verbatim slice of the document, a chunk's rune offsets are
// exact and its page range follows from them by lookup. The previous
// implementation destroyed the offsets and then tried to recover them by
// matching word sequences, which is why the recovery function was never wired
// into production.
package chunker

import "sort"

// pageMapper maps rune offsets to source page numbers.
type pageMapper struct {
	pages []int
	// lineStartRunes[i] is the rune offset at which line i begins.
	lineStartRunes []int
}

// newPageMapper builds a mapper from markdown and its per-line page map.
//
// Stitch's contract is one Pages entry per newline: the markdown it returns ends
// in exactly one "\n", which terminates the last line rather than starting an
// empty one. So "a\nb\n" carries 2 pages with line starts [0, 2].
//
// A length mismatch degrades to "no page info" rather than mis-attributing
// pages, so a future Stitch change cannot silently produce wrong citations.
func newPageMapper(markdown string, pages []int) *pageMapper {
	if len(pages) == 0 || markdown == "" {
		return &pageMapper{}
	}

	starts := make([]int, 0, len(pages))
	starts = append(starts, 0)
	total := runeLen(markdown)
	runePos := 0
	for _, r := range markdown {
		if r == '\n' && runePos < total-1 {
			// A newline that is not the final rune starts the next line.
			starts = append(starts, runePos+1)
		}
		runePos++
	}

	if len(starts) != len(pages) {
		return &pageMapper{}
	}
	return &pageMapper{pages: pages, lineStartRunes: starts}
}

// rangeFor returns the 1-based inclusive page range covering rune offsets
// [start, end), or (nil, nil) when no page information exists.
func (m *pageMapper) rangeFor(start, end int) (*int, *int) {
	if m == nil || len(m.pages) == 0 {
		return nil, nil
	}
	if end <= start {
		end = start + 1
	}
	first := m.lineForRune(start)
	// end is exclusive, so the last rune actually covered is end-1.
	last := m.lineForRune(end - 1)
	if first < 0 || last < 0 {
		return nil, nil
	}
	ps, pe := m.pages[first], m.pages[last]
	return &ps, &pe
}

// lineForRune returns the index of the line containing a rune offset, or -1 when
// the offset is out of range.
func (m *pageMapper) lineForRune(off int) int {
	if off < 0 || len(m.lineStartRunes) == 0 {
		return -1
	}
	// The last line whose start is <= off.
	idx := sort.SearchInts(m.lineStartRunes, off+1) - 1
	if idx < 0 || idx >= len(m.pages) {
		return -1
	}
	return idx
}
