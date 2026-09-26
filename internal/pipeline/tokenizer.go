package pipeline

import "strings"

// Tokenizer counts tokens. Kept separate from the chunker package because it
// serves the EMBED BATCHING budget, not chunk sizing: PlanBatches uses it to
// pack texts into requests without exceeding the backend's context window.
type Tokenizer interface {
	Count(text string) int
}

// WhitespaceTokenizer is the cheap word-count heuristic. It is deliberately
// conservative and stable: changing it would re-tune every embed batch, which is
// a separate concern from how text is chunked.
type WhitespaceTokenizer struct{}

// Count returns the number of whitespace-separated fields.
func (WhitespaceTokenizer) Count(text string) int { return len(strings.Fields(text)) }
