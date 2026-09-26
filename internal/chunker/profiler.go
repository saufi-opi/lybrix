// profiler.go scans a document once to gather structure indicators that drive
// strategy selection (heading-aware vs. heuristic vs. recursive). Profiling is
// cheap — a few regex passes plus rune counting — and runs before any chunking
// decision is made.
package chunker

import (
	"math"
	"strings"
)

// DocProfile holds the document-level signals used to choose a chunking tier.
type DocProfile struct {
	TotalChars int     `json:"total_chars"`
	TotalLines int     `json:"total_lines"`
	AvgLineLen float64 `json:"avg_line_len"`
	StdLineLen float64 `json:"std_line_len"`

	// Markdown structure
	MdHeadingCounts map[int]int `json:"md_heading_counts"` // level (1..6) -> count
	MdHeadingTotal  int         `json:"md_heading_total"`

	// Heuristic indicators
	NumberedSectionCount  int `json:"numbered_section_count"`
	AllCapsShortLineCount int `json:"all_caps_short_line_count"`
	BlankParagraphBreaks  int `json:"blank_paragraph_breaks"`
	FormFeedCount         int `json:"form_feed_count"`
	VisualSepCount        int `json:"visual_sep_count"`
	GermanChapterCount    int `json:"german_chapter_count"`
	EnglishChapterCount   int `json:"english_chapter_count"`
	ChineseChapterCount   int `json:"chinese_chapter_count"`
	RepeatedFooterCount   int `json:"repeated_footer_count"`

	// Content characteristics
	HasTables bool    `json:"has_tables"`
	HasCode   bool    `json:"has_code"`
	CodeRatio float64 `json:"code_ratio"`

	// DetectedLangs are best-effort language hints.
	DetectedLangs []string `json:"detected_langs"`
}

// HeadingDensity returns the share of lines that are markdown headings.
func (p *DocProfile) HeadingDensity() float64 {
	if p.TotalLines == 0 {
		return 0
	}
	return float64(p.MdHeadingTotal) / float64(p.TotalLines)
}

// DominantHeadingLevel returns the level that should drive section splitting:
// the shallowest level with at least 3 occurrences (a real structural
// backbone), else the deepest level present. 0 when there are no headings.
func (p *DocProfile) DominantHeadingLevel() int {
	if p.MdHeadingTotal == 0 {
		return 0
	}
	for level := 1; level <= 6; level++ {
		if p.MdHeadingCounts[level] >= 3 {
			return level
		}
	}
	for level := 6; level >= 1; level-- {
		if p.MdHeadingCounts[level] > 0 {
			return level
		}
	}
	return 0
}

// HeuristicMarkerTotal sums the non-markdown structural markers.
func (p *DocProfile) HeuristicMarkerTotal() int {
	return p.NumberedSectionCount +
		p.GermanChapterCount + p.EnglishChapterCount + p.ChineseChapterCount +
		p.AllCapsShortLineCount + p.VisualSepCount + p.FormFeedCount
}

// ProfileDocument runs a single pass over text and returns its profile.
func ProfileDocument(text string) *DocProfile {
	p := &DocProfile{MdHeadingCounts: make(map[int]int)}
	if text == "" {
		return p
	}

	p.TotalChars = runeLen(text)
	p.FormFeedCount = strings.Count(text, "\f")

	lines := strings.Split(text, "\n")
	p.TotalLines = len(lines)

	var lengths []float64
	inFence := false
	codeChars := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// A 3-backtick prefix detector rather than the full protected-pattern
		// regex, so fence state does not fight the protection logic.
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			p.HasCode = true
			continue
		}
		if inFence {
			codeChars += runeLen(line)
			continue
		}

		lengths = append(lengths, float64(runeLen(line)))

		if matchHeading(line, &p.MdHeadingCounts) {
			p.MdHeadingTotal++
			continue
		}
		if NumberedSectionPattern.MatchString(line) {
			p.NumberedSectionCount++
		}
		if GermanChapterPattern.MatchString(line) {
			p.GermanChapterCount++
		}
		if EnglishChapterPattern.MatchString(line) {
			p.EnglishChapterCount++
		}
		if ChineseChapterPattern.MatchString(line) {
			p.ChineseChapterCount++
		}
		if AllCapsHeadingPattern.MatchString(line) {
			p.AllCapsShortLineCount++
		}
		if VisualSeparatorPattern.MatchString(line) {
			p.VisualSepCount++
		}
		if PageFooterPattern.MatchString(line) {
			p.RepeatedFooterCount++
		}
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			p.HasTables = true
		}
	}

	if len(lengths) > 0 {
		var sum float64
		for _, l := range lengths {
			sum += l
		}
		p.AvgLineLen = sum / float64(len(lengths))
		var variance float64
		for _, l := range lengths {
			d := l - p.AvgLineLen
			variance += d * d
		}
		variance /= float64(len(lengths))
		p.StdLineLen = math.Sqrt(variance)
	}

	if p.TotalChars > 0 {
		p.CodeRatio = float64(codeChars) / float64(p.TotalChars)
	}

	p.BlankParagraphBreaks = strings.Count(text, "\n\n\n")

	// Sample the head of the document for language detection: O(1) cost on huge
	// inputs, and a stable signal.
	sample := text
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	lang := DetectLanguage(sample)
	p.DetectedLangs = []string{lang}
	if lang == LangMixed {
		p.DetectedLangs = []string{LangEnglish, LangGerman, LangChinese}
	}
	return p
}

// matchHeading reports whether line is an ATX heading, incrementing the level
// counter when so.
func matchHeading(line string, counts *map[int]int) bool {
	m := MarkdownHeadingPattern.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	level := len(m[1])
	if level < 1 || level > 6 {
		return false
	}
	(*counts)[level]++
	return true
}

// StrategyTier identifies which splitting implementation should run.
type StrategyTier string

const (
	TierHeading   StrategyTier = "heading"
	TierHeuristic StrategyTier = "heuristic"
	TierLegacy    StrategyTier = "legacy"
)

// SelectStrategy returns the ordered tier chain to attempt. The first tier is
// the primary choice; later tiers run when the validator rejects the previous
// output. TierLegacy is always appended as a safety net, so a caller always
// receives at least one chunk-set.
func SelectStrategy(p *DocProfile) []StrategyTier {
	if p == nil {
		return []StrategyTier{TierLegacy}
	}
	var chain []StrategyTier

	// A real heading backbone: enough headings, spread over enough of the
	// document, with a usable dominant level.
	if p.MdHeadingTotal >= 3 && p.HeadingDensity() > 0.005 && p.DominantHeadingLevel() > 0 {
		chain = append(chain, TierHeading)
	}
	// Non-markdown structural markers worth cutting on.
	if p.HeuristicMarkerTotal() >= 5 || p.FormFeedCount > 0 ||
		p.GermanChapterCount+p.EnglishChapterCount+p.ChineseChapterCount > 0 {
		chain = append(chain, TierHeuristic)
	}

	chain = append(chain, TierLegacy)
	return chain
}
