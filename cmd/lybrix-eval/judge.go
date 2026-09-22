package main

import (
	"regexp"
	"strings"
)

// Judge: retrieval metrics over plain maps — title normalization/matching,
// hit@k, MRR, heading-path check, per-category breakdowns, junk filtering.
// Port of scripts/eval/judge.py; all functions are pure on plain maps.

var nonAlnum = regexp.MustCompile(`[^a-z0-9\s]+`)

// NormalizeTitle normalizes a document title for matching.
//
//  1. Fold symbol tokens IN PLACE, inserting no spaces: '+' -> 'plus' (so
//     "C++" -> "cplusplus", adjacent — normative), '#' -> 'sharp',
//     '.'/'_'/'-' -> space.
//  2. Lowercase; replace every remaining non-alphanumeric char with space;
//     collapse whitespace.
func NormalizeTitle(title string) string {
	text := strings.ReplaceAll(title, "+", "plus")
	text = strings.ReplaceAll(text, "#", "sharp")
	text = strings.ReplaceAll(text, ".", " ")
	text = strings.ReplaceAll(text, "_", " ")
	text = strings.ReplaceAll(text, "-", " ")
	text = nonAlnum.ReplaceAllString(strings.ToLower(text), " ")
	return strings.Join(strings.Fields(text), " ")
}

// SpaceStrippedLen is the length of the space-stripped normal form — the
// loader's seed-title contract threshold (>= 8) is measured with this.
func SpaceStrippedLen(title string) int {
	return len(strings.ReplaceAll(NormalizeTitle(title), " ", ""))
}

// TitlesMatch reports whether normalized titles a and b match.
//
// Match = exact-equal normal forms, OR — after additionally removing ALL
// spaces from both normal forms — one is a substring of the other, with a
// length guard: the shorter space-stripped side must be >= 8 chars OR the
// match is exact-equal on the space-stripped forms.
//
// Space-stripping is what joins slugs to titles: "cpluspluscrashcourse" is
// a substring of "cpluspluscrashcourseafastpacedintroduction". The guard
// prevents "Go" (2 chars) from matching every title containing "go".
func TitlesMatch(a, b string) bool {
	aNorm, bNorm := NormalizeTitle(a), NormalizeTitle(b)
	if aNorm == bNorm {
		return true
	}
	aFlat := strings.ReplaceAll(aNorm, " ", "")
	bFlat := strings.ReplaceAll(bNorm, " ", "")
	if len(aFlat) < 8 || len(bFlat) < 8 {
		return false
	}
	shorter, longer := aFlat, bFlat
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	return strings.Contains(longer, shorter)
}

// TitleHit returns the rank (1-based) of the first result whose doc_title
// matches ANY expected title via TitlesMatch; nil if no result matches.
func TitleHit(expectedTitles []string, results []map[string]any) *int {
	for rank, result := range results {
		title, ok := result["doc_title"].(string)
		if !ok {
			continue // doc_title can be null on live data
		}
		for _, expected := range expectedTitles {
			if TitlesMatch(title, expected) {
				r := rank + 1
				return &r
			}
		}
	}
	return nil
}

// HeadingPathString is the join form heading checks run against:
// " › ".join(heading_path).
func HeadingPathString(result map[string]any) string {
	path, ok := result["heading_path"].([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, toStr(p))
	}
	return strings.Join(parts, " › ")
}

// HeadingHit is a case-insensitive substring check of expected headings
// against the joined heading_path — only meaningful on the title-matching
// hit; records whether the query landed in the right section.
func HeadingHit(expectedHeadings []string, result map[string]any) bool {
	if len(expectedHeadings) == 0 {
		return false
	}
	path := strings.ToLower(HeadingPathString(result))
	for _, expected := range expectedHeadings {
		if strings.Contains(path, strings.ToLower(expected)) {
			return true
		}
	}
	return false
}

// --- junk filter (filters.py port) ---------------------------------------

var junkLastHeadings = map[string]bool{"index": true, "see also": true, "problem": true, "solution": true}

var frontMatterRE = regexp.MustCompile(`(?i)(^\d+-FM-|\.(?:indd|dvi|pdf)$)`)

// IsJunk reports whether a search result is TOC/junk noise that should not
// count as a retrieval hit (or against one) in filtered metrics.
func IsJunk(result map[string]any) bool {
	if path, ok := result["heading_path"].([]any); ok && len(path) > 0 {
		var parts []string
		for _, p := range path {
			parts = append(parts, toStr(p))
		}
		joined := strings.ToLower(strings.Join(parts, " › "))
		if strings.Contains(joined, "table of contents") {
			return true
		}
		last := strings.ToLower(strings.TrimSpace(toStr(path[len(path)-1])))
		if junkLastHeadings[last] {
			return true
		}
	}
	if title, ok := result["doc_title"].(string); ok {
		return frontMatterRE.MatchString(title)
	}
	return false
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}

func jsonUnmarshal(b []byte, v any) error { return jsonUnmarshalReal(b, v) }
