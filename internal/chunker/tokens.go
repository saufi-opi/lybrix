package chunker

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Language identifiers used by the token estimator and the heuristic splitter.
const (
	LangEnglish = "en"
	LangGerman  = "de"
	LangChinese = "zh"
	LangMixed   = "mixed"
)

// charsPerToken holds approximate chars/token ratios per language. The numbers
// err on the conservative side so estimates over-shoot a little and chunks stay
// safely under a model's hard limit.
//
// A real tokenizer (tiktoken and friends) is deliberately avoided: it would add
// a large dependency and a per-model vocabulary to keep in sync, for a number
// that only has to be approximately right.
var charsPerToken = map[string]float64{
	LangEnglish: 4.0,
	LangGerman:  4.5,
	LangChinese: 1.7,
	LangMixed:   3.0,
}

// ApproxTokenCount returns a conservative token estimate for s in lang. An
// empty or unknown lang falls back to "mixed".
func ApproxTokenCount(s string, lang string) int {
	if s == "" {
		return 0
	}
	return ApproxTokenCountFromRuneLen(utf8.RuneCountInString(s), lang)
}

// ApproxTokenCountFromRuneLen is the allocation-free variant for callers that
// already hold the rune length.
func ApproxTokenCountFromRuneLen(runeLen int, lang string) int {
	if runeLen <= 0 {
		return 0
	}
	ratio, ok := charsPerToken[lang]
	if !ok {
		ratio = charsPerToken[LangMixed]
	}
	approx := float64(runeLen) / ratio
	if approx < 1 {
		return 1
	}
	return int(approx + 0.5)
}

// CharsForTokenLimit converts a token limit into an approximate character
// budget for lang, with a 0.9 safety factor so the estimate under-shoots the
// model limit rather than over-shooting it.
func CharsForTokenLimit(tokens int, lang string) int {
	if tokens <= 0 {
		return 0
	}
	ratio, ok := charsPerToken[lang]
	if !ok {
		ratio = charsPerToken[LangMixed]
	}
	return int(float64(tokens) * ratio * 0.9)
}

// DetectLanguage returns a coarse language label by counting CJK versus Latin
// runes. Cheap, and meant only for heuristic dispatch — not a replacement for
// proper language identification.
func DetectLanguage(s string) string {
	if s == "" {
		return LangMixed
	}
	var cjk, latin, umlaut int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) ||
			unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			cjk++
		case isGermanUmlaut(r):
			umlaut++
			latin++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			latin++
		}
	}
	total := cjk + latin
	if total == 0 {
		return LangMixed
	}
	cjkRatio := float64(cjk) / float64(total)
	latinRatio := float64(latin) / float64(total)
	// Mixed: a meaningful presence of both scripts (>=15% each).
	if cjkRatio >= 0.15 && latinRatio >= 0.15 {
		return LangMixed
	}
	if cjkRatio > 0.3 {
		return LangChinese
	}
	if umlaut > 0 || hasGermanWords(s) {
		return LangGerman
	}
	return LangEnglish
}

func isGermanUmlaut(r rune) bool {
	switch r {
	case 'ä', 'ö', 'ü', 'Ä', 'Ö', 'Ü', 'ß':
		return true
	}
	return false
}

// hasGermanWords is a tiny stop-word check to bias towards "de" when the text
// uses common German function words. False positives on borrowed terms are
// acceptable.
func hasGermanWords(s string) bool {
	const sample = 512
	if len(s) > sample {
		s = s[:sample]
	}
	lower := strings.ToLower(s)
	for _, w := range []string{" der ", " die ", " das ", " und ", " ist ", " nicht ", " mit ", " auf "} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}
