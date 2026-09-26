package chunker

import (
	"strings"
)

// Default chunk sizing constants. Single source of truth for this package.
//
// DefaultChunkSize = 512 runes ≈ 100–130 English tokens / ~300 Chinese tokens,
// a widely-validated baseline for retrieval. Use 200–400 for FAQ-style atomic
// content, 1000–2000 for narrative documents.
//
// DefaultChunkOverlap = 80 runes (≈15%): the sweet spot between recall (an
// answer split across a boundary needs overlap to be retrievable) and storage
// cost.
const (
	DefaultChunkSize    = 512
	DefaultChunkOverlap = 80

	// Parent/child defaults for hierarchical chunking.
	DefaultParentSize = 2048
	DefaultChildSize  = 384

	// absoluteMaxSize is a hard ceiling on one chunk, independent of the
	// configured budget, so a single pathological unit cannot exceed a
	// downstream embedding API's limit.
	absoluteMaxSize = 7500
)

// AbsoluteMaxSize exposes the hard per-chunk ceiling for the settings readout.
func AbsoluteMaxSize() int { return absoluteMaxSize }

// DefaultSeparators is the separator priority chain: paragraph, line, CJK
// sentence end.
var DefaultSeparators = []string{"\n\n", "\n", "。"}

// Strategy values for SplitterConfig.Strategy.
const (
	StrategyAuto      = "auto"
	StrategyHeading   = "heading"
	StrategyHeuristic = "heuristic"
	StrategyRecursive = "recursive"
	StrategyLegacy    = "legacy"
)

// SplitterConfig configures splitting. Strategy and TokenLimit are honored by
// the strategy entry point; SplitText uses only size/overlap/separators.
type SplitterConfig struct {
	ChunkSize    int
	ChunkOverlap int
	Separators   []string

	// Strategy selects an adaptive tier. Empty means auto (the profiler
	// chooses) — deliberately NOT legacy, which would silently disable
	// heading breadcrumbs for every caller that forgot to set the field.
	Strategy string
	// TokenLimit caps chunk size in approximate tokens. 0 = use ChunkSize.
	TokenLimit int
	// Languages hints the multilingual heuristic patterns. Empty = auto-detect.
	Languages []string
}

// DefaultConfig returns the base defaults.
func DefaultConfig() SplitterConfig {
	return SplitterConfig{
		ChunkSize:    DefaultChunkSize,
		ChunkOverlap: DefaultChunkOverlap,
		Separators:   DefaultSeparators,
	}
}

// NormalizeConfig fills zero-value fields with defaults.
func NormalizeConfig(cfg SplitterConfig) SplitterConfig {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.ChunkOverlap <= 0 {
		cfg.ChunkOverlap = DefaultChunkOverlap
	}
	if len(cfg.Separators) == 0 {
		cfg.Separators = DefaultSeparators
	}
	return cfg
}

// DeriveParentChildConfigs produces the parent and child splitter configs for
// hierarchical chunking. TokenLimit is copied only to children because parents
// keep the configured context window.
func DeriveParentChildConfigs(base SplitterConfig, parentSize, childSize int) (parent, child SplitterConfig) {
	if parentSize <= 0 {
		parentSize = DefaultParentSize
	}
	if childSize <= 0 {
		childSize = DefaultChildSize
	}
	parent = SplitterConfig{
		ChunkSize:    parentSize,
		ChunkOverlap: base.ChunkOverlap,
		Separators:   base.Separators,
		Strategy:     base.Strategy,
		Languages:    base.Languages,
	}
	child = SplitterConfig{
		ChunkSize:    childSize,
		ChunkOverlap: childSize / 5,
		Separators:   base.Separators,
		Strategy:     base.Strategy,
		TokenLimit:   base.TokenLimit,
		Languages:    base.Languages,
	}
	return
}

// NormalizeLineEndings canonicalizes CRLF/CR to LF before chunking, so the same
// document produces the same boundaries regardless of its original line endings.
func NormalizeLineEndings(text string) string {
	if !strings.Contains(text, "\r") {
		return text
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// SplitText splits text into chunks with overlap, keeping protected regions
// (tables, code fences, math, links) atomic. Every returned chunk's Content is a
// verbatim slice of text, so End-Start == runeLen(Content).
func SplitText(text string, cfg SplitterConfig) []Chunk {
	if text == "" {
		return nil
	}
	cfg = ensureDefaults(cfg)

	// Step 1: locate the regions that must not be split.
	protected := protectedSpans(text)

	// Step 2: split everything else by separator priority, keeping protected
	// spans as atomic units.
	units := buildUnitsWithProtection(text, protected, cfg.Separators, cfg.ChunkSize)

	// Step 3: merge units into chunks with overlap and header tracking.
	return mergeUnits(units, cfg.ChunkSize, cfg.ChunkOverlap)
}

// ensureDefaults applies the base defaults and clamps a pathological overlap.
//
// TokenLimit is translated into a character budget (with a 10% safety factor) so
// chunks stay inside an embedding API's hard token cap.
func ensureDefaults(cfg SplitterConfig) SplitterConfig {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.ChunkOverlap <= 0 {
		cfg.ChunkOverlap = DefaultChunkOverlap
	}
	if len(cfg.Separators) == 0 {
		cfg.Separators = DefaultSeparators
	}
	if cfg.TokenLimit > 0 {
		lang := LangMixed
		if len(cfg.Languages) > 0 {
			lang = cfg.Languages[0]
		}
		if budget := CharsForTokenLimit(cfg.TokenLimit, lang); budget > 0 && budget < cfg.ChunkSize {
			cfg.ChunkSize = budget
		}
	}
	// An overlap above half the chunk size makes almost every chunk a near-clone
	// of its predecessor; cap it so overlap stays a smoothing band.
	if cfg.ChunkSize > 0 && cfg.ChunkOverlap > cfg.ChunkSize/2 {
		cfg.ChunkOverlap = cfg.ChunkSize / 2
	}
	return cfg
}

// mergeUnits combines split units into chunks with overlap tracking, enforcing
// the absolute maximum and re-injecting active table headers so every chunk
// carries its own column context.
func mergeUnits(units []splitUnit, chunkSize, chunkOverlap int) []Chunk {
	if len(units) == 0 {
		return nil
	}

	ht := newHeaderTracker()
	var chunks []Chunk
	var current []splitUnit
	curLen := 0

	for _, u := range units {
		uLen := runeLen(u.text)

		// A single unit over the absolute max cannot be packed at all: flush,
		// then chop it directly.
		if uLen > absoluteMaxSize {
			chunks, current, curLen = flush(chunks, current, curLen)
			// Keep header state coherent even for an oversized unit.
			ht.update(u.text)
			chunks = append(chunks, chopOversize(u, len(chunks))...)
			continue
		}

		ht.update(u.text)
		// Flush at a table boundary, so the next table is not merged into a
		// chunk still carrying the previous table's prepended header.
		if ht.headerEndedThisUnit {
			chunks, current, curLen = flush(chunks, current, curLen)
		}

		headers := ht.getHeaders()
		headersLen := runeLen(headers)
		if headersLen > chunkSize {
			// A header wider than the whole budget is useless as context.
			headers, headersLen = "", 0
		}

		if curLen+uLen+headersLen > chunkSize && len(current) > 0 {
			chunks, _, _ = flush(chunks, current, curLen)

			current, curLen = computeOverlap(current, chunkOverlap, chunkSize, uLen)

			// Shrink the overlap further if needed to fit the header plus the
			// incoming unit.
			if headers != "" && headersLen+uLen <= chunkSize {
				for len(current) > 0 && curLen+uLen+headersLen > chunkSize {
					curLen -= runeLen(current[0].text)
					current = current[1:]
				}
				overlapText := unitsText(current)
				if !headerAlreadyPresent(headers, overlapText, u.text) &&
					!headerColumnMismatch(headers, u.text) {
					startPos := u.start
					if len(current) > 0 {
						startPos = current[0].start
					}
					// Zero-width synthetic unit: generated text with no source
					// range, which is what keeps the position invariant intact.
					hUnit := splitUnit{text: headers, start: startPos, end: startPos}
					current = append([]splitUnit{hUnit}, current...)
					curLen += headersLen
				}
			}
		}

		if curLen+uLen > absoluteMaxSize {
			chunks, current, curLen = flush(chunks, current, curLen)
		}

		current = append(current, u)
		curLen += uLen
	}

	chunks, _, _ = flush(chunks, current, curLen)
	return chunks
}

// flush emits the accumulated units as one chunk, skipping an accumulation that
// carries only whitespace (boundary clustering can produce one).
func flush(chunks []Chunk, current []splitUnit, curLen int) ([]Chunk, []splitUnit, int) {
	if len(current) > 0 && hasContent(current) {
		chunks = append(chunks, buildChunk(current, len(chunks)))
	}
	return chunks, nil, 0
}

// chopOversize force-splits one unit larger than the absolute max, preferring a
// newline or space within the last 200 runes of each slice.
func chopOversize(u splitUnit, startSeq int) []Chunk {
	runes := []rune(u.text)
	var out []Chunk
	offset := 0
	for offset < len(runes) {
		chunkEnd := offset + absoluteMaxSize
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
		out = append(out, Chunk{
			Content: string(runes[offset:chunkEnd]),
			Seq:     startSeq + len(out),
			Start:   u.start + offset,
			End:     u.start + chunkEnd,
		})
		offset = chunkEnd
	}
	return out
}

// buildChunk concatenates units verbatim.
//
// A re-injected table header is a synthetic unit carrying generated text, so the
// chunk's Content is longer than its source span. buildChunk accounts for that
// explicitly: Start/End always describe the SOURCE portion, and
// SyntheticPrefixRunes records how many leading runes are generated. Callers
// that reconstruct a document must strip the prefix first.
func buildChunk(units []splitUnit, seq int) Chunk {
	var sb strings.Builder
	synthetic := 0
	start, end := -1, -1
	for _, u := range units {
		if u.isSynthetic() {
			// Synthetic units are only ever prepended, so they contribute to
			// the prefix; the source range is untouched.
			synthetic += runeLen(u.text)
			sb.WriteString(u.text)
			continue
		}
		if start < 0 {
			start = u.start
		}
		end = u.end
		sb.WriteString(u.text)
	}
	if start < 0 {
		// Fully synthetic (no source backing): collapse the range.
		start, end = 0, 0
	}
	return Chunk{
		Content:              sb.String(),
		Seq:                  seq,
		Start:                start,
		End:                  end,
		SyntheticPrefixRunes: synthetic,
	}
}

// hasContent reports whether any unit carries non-whitespace text.
func hasContent(units []splitUnit) bool {
	for _, u := range units {
		if strings.TrimSpace(u.text) != "" {
			return true
		}
	}
	return false
}

// unitsText concatenates the text of all units.
func unitsText(units []splitUnit) string {
	var sb strings.Builder
	for _, u := range units {
		sb.WriteString(u.text)
	}
	return sb.String()
}

// semanticOverlapLookbehind is the rune length of the longest separator
// (\r\n\r\n), so a separator cut by the window boundary stays visible.
const semanticOverlapLookbehind = 4

// computeOverlap returns the semantic suffix of current to carry into the next
// chunk, plus its rune length.
//
// The configured overlap is a hard upper bound, not a raw character slice.
// Eligible boundaries, in priority order:
//
//  1. paragraph break (\n\n / \r\n\r\n)
//  2. line break (\n / \r\n)
//  3. sentence end (。？！, ". ", "? ", "! ")
//
// Priority wins first; among equal priorities the earliest boundary in the
// window wins, so the retained overlap is as useful as possible.
func computeOverlap(current []splitUnit, chunkOverlap, chunkSize, nextLen int) ([]splitUnit, int) {
	if chunkOverlap <= 0 {
		return nil, 0
	}
	// Overlap counts toward the next chunk's size: if the incoming unit is
	// already near the budget, shrink the window instead of overshooting.
	maxOverlap := chunkOverlap
	if remaining := chunkSize - nextLen; remaining < maxOverlap {
		maxOverlap = remaining
	}
	if maxOverlap <= 0 {
		return nil, 0
	}

	window := semanticOverlapWindow(current, maxOverlap+semanticOverlapLookbehind)
	if len(window) == 0 {
		return nil, 0
	}

	windowText := unitsText(window)
	originalWindowStart := runeLen(windowText) - maxOverlap
	if originalWindowStart < 0 {
		originalWindowStart = 0
	}
	boundaryEnd, ok := findSemanticOverlapBoundaryEndingAtOrAfter(windowText, originalWindowStart)
	if !ok {
		return nil, 0
	}

	overlap := trimUnitsPrefix(window, boundaryEnd)
	overlapLen := 0
	for _, u := range overlap {
		overlapLen += runeLen(u.text)
	}
	if overlapLen <= 0 || overlapLen > maxOverlap || strings.TrimSpace(unitsText(overlap)) == "" {
		return nil, 0
	}
	return overlap, overlapLen
}

// semanticOverlapWindow returns at most maxLen source-backed runes from the tail
// of current. It may slice inside the first retained unit so a semantic boundary
// inside a large paragraph stays discoverable.
//
// Synthetic zero-width units (re-injected table headers) are a hard barrier:
// crossing one would break the Start/End-to-Content invariant.
func semanticOverlapWindow(current []splitUnit, maxLen int) []splitUnit {
	if maxLen <= 0 || len(current) == 0 {
		return nil
	}
	remaining := maxLen
	reversed := make([]splitUnit, 0, len(current))
	for i := len(current) - 1; i >= 0 && remaining > 0; i-- {
		u := current[i]
		uLen := runeLen(u.text)
		if uLen == 0 {
			continue
		}
		if u.isSynthetic() {
			break
		}
		if uLen <= remaining {
			reversed = append(reversed, u)
			remaining -= uLen
			continue
		}
		runes := []rune(u.text)
		start := uLen - remaining
		reversed = append(reversed, splitUnit{
			text:  string(runes[start:]),
			start: u.start + start,
			end:   u.end,
		})
		remaining = 0
	}
	if len(reversed) == 0 {
		return nil
	}
	window := make([]splitUnit, len(reversed))
	for i := range reversed {
		window[len(reversed)-1-i] = reversed[i]
	}
	return window
}

type semanticOverlapBoundary struct {
	start, end int
	priority   int
}

// findSemanticOverlapBoundaryEndingAtOrAfter applies the priority and
// earliest-position rules only to candidates ending at or after minEnd.
// Filtering before comparison stops an earlier but ineligible lookbehind
// separator from hiding a later eligible boundary.
//
// Boundaries inside protected regions are ignored, as is a boundary followed
// only by whitespace.
func findSemanticOverlapBoundaryEndingAtOrAfter(text string, minEnd int) (int, bool) {
	runes := []rune(text)
	if len(runes) == 0 {
		return 0, false
	}
	if minEnd < 0 {
		minEnd = 0
	}
	if minEnd > len(runes) {
		return 0, false
	}

	protected := protectedSpansRune(text, protectedSpans(text))
	hasMeaningfulTail := func(end int) bool {
		return end >= 0 && end < len(runes) && strings.TrimSpace(string(runes[end:])) != ""
	}

	var best semanticOverlapBoundary
	found := false
	consider := func(start, end, priority int) {
		if start < 0 || end <= start || end < minEnd || end > len(runes) ||
			insideSpan(protected, start) || !hasMeaningfulTail(end) {
			return
		}
		candidate := semanticOverlapBoundary{start: start, end: end, priority: priority}
		if !found || candidate.priority < best.priority ||
			(candidate.priority == best.priority && candidate.start < best.start) {
			best, found = candidate, true
		}
	}

	// Mark paragraph-break runes so their component newlines are not also
	// emitted as lower-priority line-break candidates.
	paragraphRune := make([]bool, len(runes))
	for i := 0; i < len(runes); i++ {
		switch {
		case i+3 < len(runes) && runes[i] == '\r' && runes[i+1] == '\n' &&
			runes[i+2] == '\r' && runes[i+3] == '\n':
			consider(i, i+4, 1)
			for j := i; j < i+4; j++ {
				paragraphRune[j] = true
			}
			i += 3
		case i+1 < len(runes) && runes[i] == '\n' && runes[i+1] == '\n':
			consider(i, i+2, 1)
			paragraphRune[i], paragraphRune[i+1] = true, true
			i++
		}
	}

	for i := 0; i < len(runes); i++ {
		if paragraphRune[i] {
			continue
		}
		if runes[i] == '\r' && i+1 < len(runes) && runes[i+1] == '\n' && !paragraphRune[i+1] {
			consider(i, i+2, 2)
			i++
			continue
		}
		if runes[i] == '\n' {
			consider(i, i+1, 2)
		}
	}

	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '。', '？', '！':
			consider(i, i+1, 3)
		case '.', '?', '!':
			if i+1 < len(runes) && runes[i+1] == ' ' {
				consider(i, i+2, 3)
			}
		}
	}

	if !found {
		return 0, false
	}
	return best.end, true
}

// trimUnitsPrefix removes prefixLen source runes while preserving the remaining
// units' original positions. prefixLen is relative to unitsText(units).
func trimUnitsPrefix(units []splitUnit, prefixLen int) []splitUnit {
	if prefixLen <= 0 {
		out := make([]splitUnit, len(units))
		copy(out, units)
		return out
	}
	remaining := prefixLen
	out := make([]splitUnit, 0, len(units))
	for _, u := range units {
		uLen := runeLen(u.text)
		if remaining >= uLen {
			remaining -= uLen
			continue
		}
		if remaining > 0 {
			runes := []rune(u.text)
			u.text = string(runes[remaining:])
			u.start += remaining
			remaining = 0
		}
		out = append(out, u)
	}
	return out
}
