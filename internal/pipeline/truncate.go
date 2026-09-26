package pipeline

import "unicode/utf8"

// TruncateRunes returns s cut to at most n bytes, backing the cut up to the
// nearest rune boundary so the result is always valid UTF-8.
//
// Go's t[:n] slices bytes, so a cut can land inside a multi-byte rune and emit
// invalid UTF-8 — which then poisons a JSON response or a tokenizer. Markdown is
// denser in bytes per word than the flattened text it replaced, so cuts are both
// more likely and more visible (BACKLOG R-26).
//
// n <= 0 returns "" (an explicit "truncate to nothing" rather than the whole
// string, so a misconfigured limit cannot silently disable truncation).
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	// Walk back over any incomplete trailing rune.
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}
