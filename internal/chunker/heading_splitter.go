// heading_splitter.go implements the heading-aware tier: documents with a real
// heading structure are split at heading boundaries, and each chunk carries a
// breadcrumb of the active heading context (e.g. "# Chapter 1\n## Section 1.2").
package chunker

import (
	"strings"
	"unicode/utf8"
)

// headingBoundary marks where a section starts. The first boundary sits at rune
// offset 0 (covering any preamble before the first heading); later boundaries
// sit at headings whose level is <= primaryLevel.
type headingBoundary struct {
	runeStart int
	line      string // raw heading line; "" for the leading boundary
}

// splitByHeadingsImpl is the heading-aware tier. It falls through to the legacy
// splitter when the document has no usable heading structure, or when heading
// splitting would produce a single section anyway.
//
// profile may be nil, in which case one is computed here. The strategy resolver
// threads its own profile through so the document is not re-scanned.
func splitByHeadingsImpl(text string, cfg SplitterConfig, profile *DocProfile) []Chunk {
	if text == "" {
		return nil
	}
	if profile == nil {
		profile = ProfileDocument(text)
	}
	primaryLevel := profile.DominantHeadingLevel()
	if primaryLevel == 0 {
		return SplitText(text, cfg)
	}

	bounds := findHeadingBoundaries(text, primaryLevel)
	if len(bounds) <= 1 {
		return SplitText(text, cfg)
	}

	runes := splitRunes(text)
	hierarchy := NewHeadingHierarchy()

	// Pre-walk every heading so the hierarchy reflects full nesting for each
	// section's start. Only section boundaries snapshot the breadcrumb; deeper
	// sub-headings update the hierarchy without changing the section's own
	// breadcrumb, so chunks within one section share it.
	var out []Chunk
	seq := 0

	for i, b := range bounds {
		endRune := len(runes)
		if i+1 < len(bounds) {
			endRune = bounds[i+1].runeStart
		}
		if b.line != "" {
			hierarchy.Observe(b.line)
		}
		// Catch sub-headings between this boundary and the next so the
		// hierarchy stays in sync for later sections. Done after observing the
		// section heading, so the breadcrumb reflects it.
		breadcrumb := hierarchy.BreadcrumbWithHashes()
		sectionStart := *hierarchy
		observeSubHeadings(runes[b.runeStart:endRune], primaryLevel, hierarchy)

		sectionRunes := runes[b.runeStart:endRune]
		if len(sectionRunes) == 0 {
			continue
		}
		sectionContent := string(sectionRunes)
		secLen := len(sectionRunes)
		bcLen := runeLen(breadcrumb)

		// A section that fits becomes one chunk. The breadcrumb travels via
		// ContextHeader, NOT Content, so the position invariant holds.
		if bcLen+2+secLen <= cfg.ChunkSize {
			out = append(out, Chunk{
				Content:       sectionContent,
				ContextHeader: breadcrumb,
				Seq:           seq,
				Start:         b.runeStart,
				End:           endRune,
			})
			seq++
			continue
		}

		// Too large: hand the interior to the legacy splitter. The breadcrumb
		// is not counted against the inner budget (it is out of band), and each
		// sub-chunk gets the breadcrumb active at its own start, so deep
		// sub-headings inside a long section are not collapsed away.
		subBreadcrumbs := sectionBreadcrumbs(sectionRunes, primaryLevel, sectionStart)
		for _, sub := range SplitText(sectionContent, cfg) {
			// sub.Start/End are offsets into sectionContent, which is itself a
			// pure source slice at b.runeStart — so the shift is additive.
			// SyntheticPrefixRunes must be carried over: a sub-chunk may carry a
			// re-injected table header, and dropping the count would make its
			// Content look longer than its source span.
			out = append(out, Chunk{
				Content:              sub.Content,
				ContextHeader:        breadcrumbAtOffset(subBreadcrumbs, sub.Start, breadcrumb),
				Seq:                  seq,
				Start:                b.runeStart + sub.Start,
				End:                  b.runeStart + sub.End,
				SyntheticPrefixRunes: sub.SyntheticPrefixRunes,
			})
			seq++
		}
	}

	return coalesceTinyChunks(out, cfg.ChunkSize)
}

// coalesceTinyChunks merges adjacent small chunks that share heading context, so
// documents whose primary sections are mostly short (FAQs, install logs,
// changelists) do not trip the validator's "too many tiny chunks" rule and fall
// all the way through to legacy.
//
// Only chunks where cur.End == next.Start are merged, which preserves the
// Start/End-to-Content invariant and naturally skips overlapping legacy
// sub-chunks.
func coalesceTinyChunks(in []Chunk, chunkSize int) []Chunk {
	if len(in) <= 1 || chunkSize <= 0 {
		return in
	}
	target := chunkSize / 2
	if target < 200 {
		target = 200
	}

	out := make([]Chunk, 0, len(in))
	cur := in[0]
	curLen := runeLen(cur.Content)

	for i := 1; i < len(in); i++ {
		next := in[i]
		nextLen := runeLen(next.Content)
		sharedHeader := commonHeadingPrefix(cur.ContextHeader, next.ContextHeader)
		if sharedHeader != "" && cur.End == next.Start &&
			curLen < target && curLen+nextLen <= chunkSize {
			cur.Content += next.Content
			cur.ContextHeader = sharedHeader
			cur.End = next.End
			curLen += nextLen
			continue
		}
		out = append(out, cur)
		cur, curLen = next, nextLen
	}
	out = append(out, cur)

	// Downstream expects Seq to be a dense 0..N-1 range.
	for i := range out {
		out[i].Seq = i
	}
	return out
}

// findHeadingBoundaries returns a boundary at offset 0 plus one per markdown
// heading at level <= primaryLevel that sits outside a fenced code block.
// Detection is line-oriented: a heading must occupy a whole line.
func findHeadingBoundaries(text string, primaryLevel int) []headingBoundary {
	runes := splitRunes(text)
	bounds := []headingBoundary{{runeStart: 0}}
	if len(runes) == 0 {
		return bounds
	}

	pos := 0
	inFence := false
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			pos += utf8.RuneCountInString(line)
			if i < len(lines)-1 {
				pos++
			}
			continue
		}
		if !inFence {
			if m := MarkdownHeadingPattern.FindStringSubmatch(line); m != nil {
				level := len(m[1])
				if level >= 1 && level <= primaryLevel {
					if pos > 0 {
						bounds = append(bounds, headingBoundary{runeStart: pos, line: line})
					} else {
						// The document opens with a heading: annotate the
						// leading boundary rather than adding a duplicate.
						bounds[0].line = line
					}
				}
			}
		}
		pos += utf8.RuneCountInString(line)
		if i < len(lines)-1 {
			pos++ // the newline strings.Split removed
		}
	}
	return bounds
}

// observeSubHeadings feeds every markdown heading deeper than primaryLevel into
// the hierarchy, keeping state correct so the next primary section's breadcrumb
// reflects the truly active stack.
func observeSubHeadings(runes []rune, primaryLevel int, h *HeadingHierarchy) {
	if len(runes) == 0 {
		return
	}
	inFence := false
	for _, line := range strings.Split(string(runes), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := MarkdownHeadingPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if len(m[1]) > primaryLevel {
			h.Observe(line)
		}
	}
}

// sectionBreadcrumb pairs a rune offset within a section with the breadcrumb in
// effect from that offset onward.
type sectionBreadcrumb struct {
	runeStart  int
	breadcrumb string
}

// sectionBreadcrumbs records, for each sub-heading deeper than primaryLevel, the
// rune offset where it takes effect and the resulting breadcrumb. seed is the
// hierarchy state at the section's start. The result is ordered by runeStart and
// always begins with the seed at offset 0, so a sub-chunk far below a deep
// heading still resolves to that heading's path.
func sectionBreadcrumbs(sectionRunes []rune, primaryLevel int, seed HeadingHierarchy) []sectionBreadcrumb {
	h := seed
	result := []sectionBreadcrumb{{runeStart: 0, breadcrumb: h.BreadcrumbWithHashes()}}
	pos := 0
	inFence := false
	lines := strings.Split(string(sectionRunes), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			pos += utf8.RuneCountInString(line)
			if i < len(lines)-1 {
				pos++
			}
			continue
		}
		if !inFence {
			if m := MarkdownHeadingPattern.FindStringSubmatch(line); m != nil && len(m[1]) > primaryLevel {
				h.Observe(line)
				result = append(result, sectionBreadcrumb{
					runeStart:  pos,
					breadcrumb: h.BreadcrumbWithHashes(),
				})
			}
		}
		pos += utf8.RuneCountInString(line)
		if i < len(lines)-1 {
			pos++
		}
	}
	return result
}

// breadcrumbAtOffset returns the breadcrumb in effect at a rune offset — the
// last entry whose runeStart is <= offset.
func breadcrumbAtOffset(bcs []sectionBreadcrumb, offset int, fallback string) string {
	bc := fallback
	for _, e := range bcs {
		if e.runeStart > offset {
			break
		}
		bc = e.breadcrumb
	}
	return bc
}
