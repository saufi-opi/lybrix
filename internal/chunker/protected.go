package chunker

import (
	"regexp"
	"unicode/utf8"
)

// protectedPatterns are the regions that must never be split: cutting inside
// one would tear a table row, a code fence or a math block in half.
//
// The link and image patterns are deliberately newline-bounded and
// length-bounded. An unbounded `[^\]]*` / `[^)]+` let a stray `[` left behind
// by OCR swallow whole paragraphs as one "protected" atomic span, which
// defeated chunking entirely (the document came back as one giant chunk). The
// bounds also encode a real CommonMark rule: a link may not span a blank line.
var protectedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\$\$.*?\$\$`), // LaTeX block math
	// Markdown images / links: single line, bounded so a stray OCR '[' cannot
	// swallow a paragraph (CommonMark forbids blank-line spans).
	regexp.MustCompile(`!\[[^\]\n]{0,200}\]\([^)\n]{1,500}\)`),
	regexp.MustCompile(`\[[^\]\n]{1,200}\]\([^)\n]{1,500}\)`),
	// Table header + separator row: "| A | B |\n| --- | --- |\n". Listed before
	// the plain row pattern so that at a shared start offset the longer span
	// wins overlap resolution and the header is kept whole.
	regexp.MustCompile(`(?m)[ ]*(?:\|[^|\n]*)+\|[\r\n]+\s*(?:\|\s*:?-{3,}:?\s*)+\|[\r\n]+`),
	regexp.MustCompile(`(?m)[ ]*(?:\|[^|\n]*)+\|[\r\n]+`), // table rows
	regexp.MustCompile("(?s)```(?:\\w+)?[\\r\\n].*?```"),  // fenced code blocks
	regexp.MustCompile("`[^`\\r\\n]+`"),                   // inline code
}

// span is a byte-offset range in the source text.
type span struct {
	start, end int
}

// protectedSpans finds all non-overlapping protected regions in text, as byte
// offsets.
//
// Overlap resolution: matches are sorted by (start asc, length desc) and then
// scanned greedily. The longest-match-at-a-shared-start rule is what keeps a
// table's header+separator row whole — both the header pattern and the plain
// row pattern match at that offset, and the longer one survives.
func protectedSpans(text string) []span {
	var all []span
	for _, pat := range protectedPatterns {
		for _, loc := range pat.FindAllStringIndex(text, -1) {
			if loc[1] > loc[0] {
				all = append(all, span{loc[0], loc[1]})
			}
		}
	}
	if len(all) == 0 {
		return nil
	}

	// Insertion sort by start asc, then length desc. The match count is small
	// (bounded by the number of protected regions), so this beats the
	// allocation of a sort.Slice closure.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0; j-- {
			if all[j].start < all[j-1].start ||
				(all[j].start == all[j-1].start &&
					(all[j].end-all[j].start) > (all[j-1].end-all[j-1].start)) {
				all[j], all[j-1] = all[j-1], all[j]
			} else {
				break
			}
		}
	}

	result := make([]span, 0, len(all))
	lastEnd := 0
	for _, m := range all {
		if m.start >= lastEnd {
			result = append(result, m)
			lastEnd = m.end
		}
	}
	return result
}

// protectedSpansRune converts byte-offset spans to rune offsets in a single
// forward pass. Callers that work in rune space (the heuristic tier, overlap
// selection) use this so a boundary is never placed inside protected content.
// byteSpans must be sorted by start, which protectedSpans guarantees.
func protectedSpansRune(text string, byteSpans []span) []span {
	if len(byteSpans) == 0 {
		return nil
	}
	out := make([]span, 0, len(byteSpans))
	runeIdx, byteIdx := 0, 0
	for _, s := range byteSpans {
		for byteIdx < s.start && byteIdx < len(text) {
			_, size := utf8.DecodeRuneInString(text[byteIdx:])
			byteIdx += size
			runeIdx++
		}
		startRune := runeIdx
		for byteIdx < s.end && byteIdx < len(text) {
			_, size := utf8.DecodeRuneInString(text[byteIdx:])
			byteIdx += size
			runeIdx++
		}
		out = append(out, span{start: startRune, end: runeIdx})
	}
	return out
}

// insideSpan reports whether runeOff falls strictly inside any span.
func insideSpan(spans []span, runeOff int) bool {
	for _, s := range spans {
		if runeOff > s.start && runeOff < s.end {
			return true
		}
	}
	return false
}

// splitUnit is a piece of text with its original rune position in the document.
//
// A unit may be SYNTHETIC: a table header re-injected across a chunk boundary
// carries start == end (zero width) because it does not exist at any position in
// the source. mergeUnits and the overlap logic both special-case that.
type splitUnit struct {
	text       string
	start, end int // rune offsets; start == end marks a synthetic unit
}

// isSynthetic reports whether the unit is generated text with no source range.
func (u splitUnit) isSynthetic() bool {
	return u.start == u.end || u.end-u.start != runeLen(u.text)
}

// maxProtectedSize caps a single protected unit. Oversize units are force-split
// at the nearest newline or space within the last 200 runes, leaving headroom
// for a re-injected header.
//
// It is deliberately the same value as absoluteMaxSize: both express the same
// constraint — one unit must fit a downstream limit — and a protected region
// that exceeded the chunk ceiling could not be emitted as a single chunk
// anyway. Kept as a distinct name because the two are read for different
// reasons (protection vs packing) and could diverge if a backend ever needs a
// smaller protection bound than the packing bound.
const maxProtectedSize = absoluteMaxSize

// splitBySeparators splits text by separators in priority order, recursively
// applying the next separator to any piece still larger than chunkSize.
//
// The recursion is per-piece, not over the whole text: if "\n\n" leaves a piece
// too big, "\n" is applied within that piece alone.
//
// chunkSize == 0 disables the recursion guard, for callers that only need the
// separator structure.
func splitBySeparators(text string, separators []string, chunkSize int) []string {
	if text == "" || len(separators) == 0 {
		return []string{text}
	}
	if chunkSize > 0 && runeLen(text) <= chunkSize {
		return []string{text}
	}

	for i, sep := range separators {
		if sep == "" {
			continue
		}
		re := regexp.MustCompile("(" + regexp.QuoteMeta(sep) + ")")
		splits := re.Split(text, -1)
		matches := re.FindAllString(text, -1)
		if len(matches) == 0 {
			continue
		}

		var pieces []string
		for j, s := range splits {
			if s != "" {
				pieces = append(pieces, s)
			}
			if j < len(matches) && matches[j] != "" {
				pieces = append(pieces, matches[j])
			}
		}
		if len(pieces) <= 1 {
			continue
		}

		var out []string
		remaining := separators[i+1:]
		for _, p := range pieces {
			if chunkSize > 0 && runeLen(p) > chunkSize && len(remaining) > 0 {
				out = append(out, splitBySeparators(p, remaining, chunkSize)...)
			} else {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{text}
}

// buildUnitsWithProtection splits text into units, keeping every protected span
// atomic: protected content is never routed through splitBySeparators, so no
// boundary can land inside a table row, a code fence or a math block.
//
// All returned offsets are rune offsets, because the merge logic indexes content
// by []rune slicing.
func buildUnitsWithProtection(text string, protected []span, separators []string, chunkSize int) []splitUnit {
	var units []splitUnit
	bytePos, runePos := 0, 0

	for _, p := range protected {
		if p.start > bytePos {
			pre := text[bytePos:p.start]
			parts := splitBySeparators(pre, separators, chunkSize)
			runeOffset := runePos
			for _, part := range parts {
				partRuneLen := runeLen(part)
				units = append(units, splitUnit{
					text:  part,
					start: runeOffset,
					end:   runeOffset + partRuneLen,
				})
				runeOffset += partRuneLen
			}
			runePos = runeOffset
		}

		protText := text[p.start:p.end]
		protRuneLen := runeLen(protText)
		if protRuneLen > maxProtectedSize {
			// A single protected region bigger than the hard cap: force-split it
			// at a newline or space near the boundary so the break is as
			// content-respecting as possible.
			runes := []rune(protText)
			offset := 0
			for offset < len(runes) {
				chunkEnd := offset + maxProtectedSize
				if chunkEnd > len(runes) {
					chunkEnd = len(runes)
				} else {
					for i := chunkEnd - 1; i > offset && i > chunkEnd-200; i-- {
						if runes[i] == '\n' || runes[i] == ' ' {
							chunkEnd = i + 1
							break
						}
					}
				}
				part := string(runes[offset:chunkEnd])
				partRuneLen := runeLen(part)
				units = append(units, splitUnit{
					text:  part,
					start: runePos,
					end:   runePos + partRuneLen,
				})
				runePos += partRuneLen
				offset = chunkEnd
			}
		} else {
			units = append(units, splitUnit{
				text:  protText,
				start: runePos,
				end:   runePos + protRuneLen,
			})
			runePos += protRuneLen
		}
		bytePos = p.end
	}

	if bytePos < len(text) {
		tail := text[bytePos:]
		parts := splitBySeparators(tail, separators, chunkSize)
		runeOffset := runePos
		for _, part := range parts {
			partRuneLen := runeLen(part)
			units = append(units, splitUnit{
				text:  part,
				start: runeOffset,
				end:   runeOffset + partRuneLen,
			})
			runeOffset += partRuneLen
		}
	}
	return units
}
