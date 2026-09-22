package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// RerankClient scores query×text pairs against a registered cross-encoder
// (query plane only — the two-plane rule means rerank never runs at ingest).
// Two provider shapes:
//
//	tei:    POST {url}/rerank     {"query","texts","raw_scores":true,"truncate":true}
//	         → [[s0, s1, …]] (logits — normalized with sigmoid)
//	openai: POST {url}/v1/rerank  {"model","query","documents","top_n","return_documents":false}
//	         → {"results":[{"index","relevance_score"}]} (already 0..1)
//
// Shape follows tei_client.go: timeout client + typed retryable error; no
// breaker at query-plane scale (one call per search, bounded by RERANK_TIMEOUT).
type RerankClient struct {
	provider      string
	modelID       string
	queryURL      string
	apiKey        string
	truncateChars int
	http          *http.Client
}

// RerankUnavailable is the typed retryable-style failure — callers degrade
// to RRF order instead of failing the search (graceful degradation).
type RerankUnavailable struct{ Detail string }

func (e *RerankUnavailable) Error() string { return "rerank unavailable: " + e.Detail }

// NewRerankClient validates the provider vocabulary.
func NewRerankClient(provider, modelID, queryURL, apiKey string, truncateChars int) (*RerankClient, error) {
	switch provider {
	case "tei", "openai":
	default:
		return nil, fmt.Errorf("unsupported rerank provider: %s", provider)
	}
	if truncateChars < 0 {
		truncateChars = 6000
	}
	return &RerankClient{
		provider:      provider,
		modelID:       modelID,
		queryURL:      queryURL,
		apiKey:        apiKey,
		truncateChars: truncateChars,
		http:          &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// rerankResponse is the openai-compatible body (tei decodes rows directly).
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank scores the query against texts in order and returns one normalized
// score per text (0..1). Order of scores matches the input order.
func (c *RerankClient) Rerank(ctx context.Context, query string, texts []string) ([]float64, error) {
	if len(texts) == 0 {
		return []float64{}, nil
	}
	if c.truncateChars > 0 {
		clipped := make([]string, len(texts))
		for i, t := range texts {
			if len(t) > c.truncateChars {
				t = t[:c.truncateChars]
			}
			clipped[i] = t
		}
		texts = clipped
	}

	var endpoint string
	var payload any
	switch c.provider {
	case "openai":
		endpoint = c.queryURL + "/v1/rerank"
		payload = map[string]any{
			"model":            c.modelID,
			"query":            query,
			"documents":        texts,
			"top_n":            len(texts),
			"return_documents": false,
		}
	default: // tei
		endpoint = c.queryURL + "/rerank"
		payload = map[string]any{
			"query":      query,
			"texts":      texts,
			"raw_scores": true,
			"truncate":   true,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &RerankUnavailable{Detail: "transport error: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, &RerankUnavailable{Detail: fmt.Sprintf("rerank returned %d: %s", resp.StatusCode, string(buf))}
	}

	if c.provider == "openai" {
		var out rerankResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, &RerankUnavailable{Detail: "bad response body: " + err.Error()}
		}
		scores := make([]float64, len(texts))
		for _, r := range out.Results {
			if r.Index >= 0 && r.Index < len(scores) {
				scores[r.Index] = r.RelevanceScore
			}
		}
		return scores, nil
	}
	// tei: one row per query — [[s0, s1, …]]
	var rows [][]float64
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, &RerankUnavailable{Detail: "bad response body: " + err.Error()}
	}
	if len(rows) == 0 {
		return nil, &RerankUnavailable{Detail: "empty rerank response"}
	}
	out := make([]float64, len(texts))
	for i, s := range rows[0] {
		if i < len(out) {
			out[i] = sigmoid(s)
		}
	}
	return out, nil
}

// sigmoid maps TEI logits to 0..1.
func sigmoid(x float64) float64 { return 1.0 / (1.0 + math.Exp(-x)) }

// Probe checks the reranker endpoint's reachability — powers the rerank
// test-connection endpoint. tei → GET /health; openai → a minimal
// 2-document rerank call (the /v1/rerank family has no uniform health path).
func (c *RerankClient) Probe(ctx context.Context) (bool, int, string) {
	started := time.Now()
	if c.provider == "tei" {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(c.queryURL + "/health")
		if err != nil {
			return false, 0, "unreachable: " + err.Error()
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, 0, fmt.Sprintf("health returned %d", resp.StatusCode)
		}
		// still prove the rerank route answers
		scores, err := c.Rerank(ctx, "ping", []string{"pong", "unrelated text"})
		if err != nil {
			return true, int(time.Since(started).Milliseconds()), "healthy; rerank probe failed: " + err.Error()
		}
		if len(scores) != 2 {
			return true, int(time.Since(started).Milliseconds()), "healthy; rerank probe returned wrong arity"
		}
		return true, int(time.Since(started).Milliseconds()), "ok"
	}
	// openai-compatible: a minimal 2-document rerank IS the probe
	if _, err := c.Rerank(ctx, "ping", []string{"pong", "unrelated text"}); err != nil {
		return false, 0, err.Error()
	}
	return true, int(time.Since(started).Milliseconds()), "ok"
}
