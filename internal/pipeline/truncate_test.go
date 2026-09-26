package pipeline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateRunesIsUTF8Safe is the regression test for the byte-slice
// truncation bug: t[:n] can cut a multi-byte rune in half, emitting invalid
// UTF-8 into a JSON response or an embed request.
func TestTruncateRunesIsUTF8Safe(t *testing.T) {
	// Each CJK rune is 3 bytes, so byte cuts at 1..2 land mid-rune.
	s := strings.Repeat("日本語", 10)

	for n := 1; n <= len(s); n++ {
		got := TruncateRunes(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("TruncateRunes(%d) produced invalid UTF-8: %q", n, got)
		}
		if len(got) > n {
			t.Fatalf("TruncateRunes(%d) returned %d bytes — over the limit", n, len(got))
		}
		if len(got) < n-utf8.UTFMax {
			t.Fatalf("TruncateRunes(%d) returned only %d bytes — over-trimmed", n, len(got))
		}
	}

	// Mixed ASCII + multibyte, cut at every offset.
	mixed := "hello 世界 and more ünïcödé text"
	for n := 1; n <= len(mixed); n++ {
		if got := TruncateRunes(mixed, n); !utf8.ValidString(got) {
			t.Fatalf("mixed TruncateRunes(%d) invalid: %q", n, got)
		}
	}
}

// TestTruncateRunesEdgeCases covers the degenerate inputs.
func TestTruncateRunesEdgeCases(t *testing.T) {
	if got := TruncateRunes("abc", 0); got != "" {
		t.Fatalf("n=0 should yield empty, got %q", got)
	}
	if got := TruncateRunes("abc", -5); got != "" {
		t.Fatalf("negative n should yield empty, got %q", got)
	}
	if got := TruncateRunes("abc", 10); got != "abc" {
		t.Fatalf("n beyond length should return the whole string, got %q", got)
	}
	if got := TruncateRunes("", 5); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}
