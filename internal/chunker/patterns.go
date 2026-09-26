// patterns.go is the source of truth for the multilingual regex patterns used
// by the heading-aware and heuristic tiers. Patterns are grouped by purpose and
// tagged with a priority the heuristic splitter uses to rank candidate
// boundaries.
package chunker

import "regexp"

// BoundaryPriority levels for heuristic chunk boundaries. Higher = stronger.
const (
	PrioFormFeed       = 100
	PrioNumberedHead   = 90
	PrioChapterMarker  = 85
	PrioAllCapsHeading = 70
	PrioVisualSep      = 60
	PrioPageFooter     = 50
	PrioBlankBlock     = 40
)

// MarkdownHeadingPattern matches an ATX-style markdown heading at line start.
// Capture groups: (1) hashes, (2) heading text.
var MarkdownHeadingPattern = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+?)\s*#*\s*$`)

// FormFeedPattern matches the form-feed control character some PDF converters
// emit as a page-break marker.
var FormFeedPattern = regexp.MustCompile(`\f`)

// NumberedSectionPattern matches lines starting with numeric or roman numbering
// followed by a non-empty title: "1. Intro", "2.3 Methods", "IV. Results",
// "2.2.1 用户与权限". The trailing dot after a multi-level numeral is optional
// because many technical documents write "1.1 Foo" without a closing dot.
var NumberedSectionPattern = regexp.MustCompile(`(?m)^[ \t]*(?:\d+(?:\.\d+){1,3}\.?|(?:\d+|[IVX]{1,5})\.)[ \t]+\S.{0,200}$`)

// AllCapsHeadingPattern matches short all-caps lines, likely section titles
// rendered without markdown headings. Requires at least 4 letters, up to ~10
// words; trailing colons are tolerated.
var AllCapsHeadingPattern = regexp.MustCompile(`(?m)^[ \t]*([A-ZÄÖÜ][A-ZÄÖÜ \-]{3,80}):?\s*$`)

// VisualSeparatorPattern matches horizontal rules / divider lines used as
// section separators in plain text or pre-markdown documents.
var VisualSeparatorPattern = regexp.MustCompile(`(?m)^[ \t]*(?:-{3,}|={3,}|\*{3,}|_{3,})[ \t]*$`)

// ExcessiveBlanksPattern matches three or more consecutive newlines, which
// usually denote a hard section break.
var ExcessiveBlanksPattern = regexp.MustCompile(`\n{3,}`)

// PageFooterPattern matches "Seite X von Y" / "Page X of Y" style footers.
var PageFooterPattern = regexp.MustCompile(`(?mi)^[ \t]*(?:Seite|Page|页码?)\s+\d+(?:\s*(?:von|of|/)\s*\d+)?[ \t]*$`)

// GermanChapterPattern matches German chapter / section markers.
var GermanChapterPattern = regexp.MustCompile(`(?m)^[ \t]*(?:Kapitel|Abschnitt|Teil)\s+(?:[0-9]+|[IVX]{1,5})[\.: ].{0,200}$`)

// EnglishChapterPattern matches English chapter / section markers.
var EnglishChapterPattern = regexp.MustCompile(`(?m)^[ \t]*(?:Chapter|Section|Part)\s+(?:[0-9]+|[IVX]{1,5})[\.: ].{0,200}$`)

// ChineseChapterPattern matches CJK chapter markers like 第一章 / 第3节; the
// whitespace between 第, the numeral and the unit is tolerated.
var ChineseChapterPattern = regexp.MustCompile(`(?m)^[ \t]*第[ \t]*[一二三四五六七八九十百千零〇0-9]+[ \t]*(?:章|节|節|部分|篇)[ \t]?.{0,200}$`)

// SentenceSeparators returns sentence-level separators tuned for lang, used for
// fine-grained sub-splitting when a section is still too large.
func SentenceSeparators(lang string) []string {
	switch lang {
	case LangChinese:
		return []string{"。", "！", "？", "；", "\n"}
	case LangGerman, LangEnglish:
		return []string{". ", "! ", "? ", "; ", "\n"}
	default:
		return []string{"。", "！", "？", "；", ". ", "! ", "? ", "; ", "\n"}
	}
}

// ChapterPatternsForLangs returns the chapter-marker regexes that apply for the
// given language hints. An empty or unknown list returns all of them, so
// auto-detected documents still match.
func ChapterPatternsForLangs(langs []string) []*regexp.Regexp {
	if len(langs) == 0 {
		return []*regexp.Regexp{GermanChapterPattern, EnglishChapterPattern, ChineseChapterPattern}
	}
	var out []*regexp.Regexp
	for _, l := range langs {
		switch l {
		case LangGerman:
			out = append(out, GermanChapterPattern)
		case LangEnglish:
			out = append(out, EnglishChapterPattern)
		case LangChinese:
			out = append(out, ChineseChapterPattern)
		}
	}
	if len(out) == 0 {
		out = []*regexp.Regexp{GermanChapterPattern, EnglishChapterPattern, ChineseChapterPattern}
	}
	return out
}
