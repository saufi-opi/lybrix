package pipeline

import (
	"context"
	"fmt"
	"sync"
)

// PlanBatches is greedy token-budgeted batching (embedder.py:60-86 port):
// budget EMBED_CTX_BUDGET (1900), batch size cap EMBED_BATCH_SIZE (48).
// Estimated token count = whitespace-token count — 1.0 used the HF
// tokenizer and 2.0 uses the whitespace heuristic the chunker already
// standardized on; the budget exists to protect the backend, and the
// 6000-char truncate is the hard backstop.
func PlanBatches(texts []string, tok Tokenizer, ctxBudget, batchSize int) ([][]string, []string) {
	// second return: texts hard-cut to the budget (pathological single
	// chunk longer than the budget alone)
	if tok == nil {
		tok = WhitespaceTokenizer{}
	}
	batches := [][]string{}
	var truncated []string
	var cur []string
	curTokens := 0
	for _, text := range texts {
		t := tok.Count(text)
		if t > ctxBudget {
			// pathological single chunk: hard-cut to the token budget
			words := splitWordsTruncate(text, tok, ctxBudget)
			text = words
			t = tok.Count(text)
			truncated = append(truncated, text)
		}
		if (len(cur) > 0 && curTokens+t > ctxBudget) || len(cur) >= batchSize {
			batches = append(batches, cur)
			cur, curTokens = nil, 0
		}
		cur = append(cur, text)
		curTokens += t
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches, truncated
}

// splitWordsTruncate joins words until the token budget is reached.
func splitWordsTruncate(text string, tok Tokenizer, budget int) string {
	var cur []string
	tokens := 0
	for _, w := range splitFields(text) {
		t := tok.Count(w)
		if tokens+t > budget {
			break
		}
		cur = append(cur, w)
		tokens += t
	}
	return joinFields(cur)
}

// splitFields/joinFields are strings.Fields/Join aliases kept explicit for
// the whitespace-token contract.
func splitFields(s string) []string { return fieldsOf(s) }
func joinFields(xs []string) string { return joinWords(xs) }

// Prefetch is the parallel S3 fetch fan-out (R-9): a book with N shards used
// to pay N sequential get_object round-trips before stitch/chunk/embed could
// start. Any per-shard failure propagates (no swallow, no in-pool retry).
func Prefetch(ctx context.Context, keys []int, fetch func(ctx context.Context, idx int) (string, error), workers int) (map[int]string, error) {
	if len(keys) <= 1 || workers <= 1 {
		out := map[int]string{}
		for _, idx := range keys {
			v, err := fetch(ctx, idx)
			if err != nil {
				return nil, err
			}
			out[idx] = v
		}
		return out, nil
	}
	var mu sync.Mutex
	out := map[int]string{}
	errCh := make(chan error, len(keys))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, idx := range keys {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v, err := fetch(ctx, idx)
			if err != nil {
				errCh <- fmt.Errorf("shard %d: %w", idx, err)
				return
			}
			mu.Lock()
			out[idx] = v
			mu.Unlock()
		}(idx)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
