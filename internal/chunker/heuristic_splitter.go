// heuristic_splitter.go implements the heuristic tier: documents without a
// usable markdown heading backbone are split at structural markers detected by
// language-aware patterns (chapter headings, numbered sections, all-caps lines,
// visual separators, page footers, form feeds, blank blocks).
package chunker

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// boundary marks a candidate split point.
type boundary struct {
	runeStart int // rune offset where the next chunk should start
	priority  int
}

// splitByHeuristicsImpl is the heuristic tier. It falls through to the legacy
// splitter when no heuristic boundaries are found.
//
// profile is accepted for signature uniformity with the heading tier but is not
// consulted: this tier scans for boundaries directly. Languages come from cfg.
func splitByHeuristicsImpl(text string, cfg SplitterConfig, _ *DocProfile) []Chunk {
	if text == "" {
		return nil
	}
	runes := splitRunes(text)
	totalRunes := len(runes)
	if totalRunes <= cfg.ChunkSize {
		return SplitText(text, cfg)
	}

	bounds := findHeuristicBoundaries(text, cfg.Languages)
	// A boundary strictly inside a protected region (table, code fence, math)
	// would tear atomic content, so drop those. Boundaries on a span edge are
	// kept — they align with the region and do not split it.
	if prot := protectedSpansRune(text, protectedSpans(text)); len(prot) > 0 {
		bounds = dropBoundsInsideSpans(bounds, prot)
	}
	if len(bounds) == 0 {
		return SplitText(text, cfg)
	}

	// Sentinel at end-of-document so the packer can flush.
	bounds = append(bounds, boundary{runeStart: totalRunes})
	if bounds[0].runeStart != 0 {
		bounds = append([]boundary{{runeStart: 0}}, bounds...)
	}

	var out []Chunk
	seq := 0
	chunkStart := bounds[0].runeStart
	curEnd := chunkStart
	minChunkSize := cfg.ChunkSize / 4
	if minChunkSize < 50 {
		minChunkSize = 50
	}

	for i := 1; i < len(bounds); i++ {
		nextEnd := bounds[i].runeStart
		blockLen := nextEnd - curEnd

		if blockLen > cfg.ChunkSize {
			// The block between the previous boundary and this one is too
			// large for any chunk: flush what accumulated, then recurse into
			// the oversize block via the legacy splitter.
			if curEnd-chunkStart > 0 {
				out = appendChunk(out, runes, chunkStart, curEnd, &seq)
				chunkStart = curEnd
			}
			out = appendOversizeBlock(out, runes, curEnd, nextEnd, cfg, &seq)
			curEnd, chunkStart = nextEnd, nextEnd
			continue
		}

		if nextEnd-chunkStart > cfg.ChunkSize && curEnd-chunkStart >= minChunkSize {
			out = appendChunk(out, runes, chunkStart, curEnd, &seq)
			// Snap the overlap start to a semantic boundary or line break
			// rather than slicing mid-line.
			chunkStart = applyOverlapAligned(runes, curEnd, cfg.ChunkOverlap, bounds)
		}
		curEnd = nextEnd
	}

	if curEnd > chunkStart {
		out = appendChunk(out, runes, chunkStart, curEnd, &seq)
	}
	return out
}

// findHeuristicBoundaries scans text and returns boundary positions in ascending
// order. Duplicates at the same offset keep only the highest priority.
func findHeuristicBoundaries(text string, langs []string) []boundary {
	var bounds []boundary

	// Form feeds are the strongest single-character boundary.
	for _, idx := range allRuneIndices(text, "\f") {
		bounds = append(bounds, boundary{runeStart: idx, priority: PrioFormFeed})
	}

	lines := strings.Split(text, "\n")
	chapterPatterns := ChapterPatternsForLangs(langs)
	pos := 0
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		} else if !inFence {
			added := false
			for _, pat := range chapterPatterns {
				if pat.MatchString(line) {
					bounds = append(bounds, boundary{runeStart: pos, priority: PrioChapterMarker})
					added = true
					break
				}
			}
			if !added && NumberedSectionPattern.MatchString(line) {
				bounds = append(bounds, boundary{runeStart: pos, priority: PrioNumberedHead})
				added = true
			}
			if !added && AllCapsHeadingPattern.MatchString(line) {
				bounds = append(bounds, boundary{runeStart: pos, priority: PrioAllCapsHeading})
				added = true
			}
			if !added && VisualSeparatorPattern.MatchString(line) {
				bounds = append(bounds, boundary{runeStart: pos, priority: PrioVisualSep})
				added = true
			}
			if !added && PageFooterPattern.MatchString(line) {
				bounds = append(bounds, boundary{runeStart: pos, priority: PrioPageFooter})
			}
		}
		pos += utf8.RuneCountInString(line)
		if i < len(lines)-1 {
			pos++
		}
	}

	// Excessive blank runs (\n{3,}). Match at the end of the run so the next
	// paragraph starts cleanly.
	for _, idx := range ExcessiveBlanksPattern.FindAllStringIndex(text, -1) {
		runeStart := utf8.RuneCountInString(text[:idx[1]])
		bounds = append(bounds, boundary{runeStart: runeStart, priority: PrioBlankBlock})
	}

	if len(bounds) == 0 {
		return nil
	}

	// Sort by position, then keep the highest priority per offset.
	sort.Slice(bounds, func(i, j int) bool {
		if bounds[i].runeStart != bounds[j].runeStart {
			return bounds[i].runeStart < bounds[j].runeStart
		}
		return bounds[i].priority > bounds[j].priority
	})
	deduped := bounds[:0]
	prev := -1
	for _, b := range bounds {
		if b.runeStart != prev {
			deduped = append(deduped, b)
			prev = b.runeStart
		}
	}
	return deduped
}

// dropBoundsInsideSpans removes bounds falling strictly inside a protected span.
// Bounds on a span edge are kept: they align with the region and do not split
// it. spans must be sorted by start.
func dropBoundsInsideSpans(bounds []boundary, spans []span) []boundary {
	if len(spans) == 0 {
		return bounds
	}
	out := bounds[:0]
boundLoop:
	for _, b := range bounds {
		for _, s := range spans {
			if s.start >= b.runeStart {
				break // remaining spans start at or after b, so cannot contain it
			}
			if b.runeStart < s.end {
				continue boundLoop
			}
		}
		out = append(out, b)
	}
	return out
}

// allRuneIndices returns every rune offset where a single-rune needle starts.
func allRuneIndices(text, needle string) []int {
	var out []int
	if needle == "" {
		return out
	}
	pos := 0
	for _, r := range text {
		if string(r) == needle {
			out = append(out, pos)
		}
		pos++
	}
	return out
}

// appendChunk slices runes[start:end] into a Chunk. Pure-whitespace slices are
// skipped, since boundary clustering can produce them. Content is the raw slice,
// so Start/End match runeLen(Content); whitespace trimming for embedding happens
// in Chunk.EmbeddingContent.
func appendChunk(out []Chunk, runes []rune, start, end int, seq *int) []Chunk {
	if end <= start {
		return out
	}
	raw := string(runes[start:end])
	if strings.TrimSpace(raw) == "" {
		return out
	}
	c := Chunk{Content: raw, Seq: *seq, Start: start, End: end}
	*seq++
	return append(out, c)
}

// appendOversizeBlock recursively chunks a region larger than cfg.ChunkSize via
// the legacy splitter, so internal budgets and protected patterns still apply.
func appendOversizeBlock(out []Chunk, runes []rune, start, end int, cfg SplitterConfig, seq *int) []Chunk {
	if end <= start {
		return out
	}
	for _, s := range SplitText(string(runes[start:end]), cfg) {
		out = append(out, Chunk{
			Content:              s.Content,
			ContextHeader:        s.ContextHeader,
			Seq:                  *seq,
			Start:                start + s.Start,
			End:                  start + s.End,
			SyntheticPrefixRunes: s.SyntheticPrefixRunes,
		})
		*seq++
	}
	return out
}

// applyOverlapAligned returns the rune offset where the next chunk should start.
// The target is curEnd - overlap, snapped to the nearest preceding boundary
// within 2x overlap, or failing that the previous newline, so chunks do not begin
// mid-line or mid-word.
//
// curEnd is itself always a boundary, so it is excluded from the search —
// choosing it would yield zero overlap.
func applyOverlapAligned(runes []rune, curEnd, overlap int, bounds []boundary) int {
	if overlap <= 0 {
		return curEnd
	}
	target := curEnd - overlap
	if target < 0 {
		target = 0
	}
	windowStart := curEnd - 2*overlap
	if windowStart < 0 {
		windowStart = 0
	}

	bestBound := -1
	for _, b := range bounds {
		if b.runeStart >= windowStart && b.runeStart < curEnd && b.runeStart > bestBound {
			bestBound = b.runeStart
		}
	}
	if bestBound >= 0 {
		return bestBound
	}

	// Fall back to the previous newline, without going past windowStart so the
	// overlap stays roughly the intended size.
	for i := target; i > windowStart && i < len(runes); i-- {
		if runes[i] == '\n' {
			return i + 1
		}
	}
	return target
}
