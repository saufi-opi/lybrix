// validator.go inspects a tier's output and decides whether it is good enough to
// ship or whether the strategy chain should fall through to the next tier.
//
// The validator is intentionally permissive: it rejects an obviously broken
// output but accepts plausible variation, so the chain does not oscillate
// between tiers. It is a tier-acceptance heuristic, not an invariant check —
// the Start/End-to-Content invariant is asserted in tests instead.
package chunker

import "math"

// ValidationResult captures the verdict and reason for a chunk set.
type ValidationResult struct {
	OK     bool
	Reason string
}

// ValidateChunks checks whether chunks form a usable result for a document of
// totalChars runes with the given target chunkSize.
func ValidateChunks(chunks []Chunk, totalChars, chunkSize int) ValidationResult {
	if len(chunks) == 0 {
		return ValidationResult{Reason: "no chunks produced"}
	}

	// One chunk for a document much larger than the budget means the strategy
	// did not actually split — fail so the next tier runs.
	if len(chunks) == 1 && totalChars > 2*chunkSize {
		return ValidationResult{Reason: "single chunk for large document"}
	}

	var sum float64
	maxLen, minLen := 0, math.MaxInt32
	for _, c := range chunks {
		l := runeLen(c.Content)
		sum += float64(l)
		if l > maxLen {
			maxLen = l
		}
		if l < minLen {
			minLen = l
		}
	}

	// Every chunk but the last should carry meaningful content; a tiny tail
	// chunk is normal.
	tinyCount := 0
	for i, c := range chunks {
		if i == len(chunks)-1 {
			continue
		}
		if runeLen(c.Content) < 50 {
			tinyCount++
		}
	}
	if tinyCount > len(chunks)/4 && tinyCount > 2 {
		return ValidationResult{Reason: "too many tiny chunks"}
	}

	// If nothing reached a quarter of the target, the splitter is fragmenting
	// too aggressively to be useful.
	if chunkSize > 0 && maxLen < chunkSize/4 && totalChars > chunkSize {
		return ValidationResult{Reason: "all chunks far below target size"}
	}

	// Past 2x the target the splitter ignored its budget.
	if chunkSize > 0 && maxLen > 2*chunkSize {
		return ValidationResult{Reason: "chunk exceeds 2x target size"}
	}

	return ValidationResult{OK: true}
}
