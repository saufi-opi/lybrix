package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// TeiClient: TEI/Ollama HTTP client — batching, jittered exponential
// backoff, circuit breaker (PRD §6.4 client resilience).
//
// If TEI is down, the embed job retries later — it must not fail the book
// and throw away the parse work already paid for. Hence: jittered
// exponential backoff on 429/503, and a circuit breaker after 5 consecutive
// failures that raises a retryable EMBED_UNAVAILABLE-class error instead of
// hammering a dead endpoint. Port of libs/embedding/client.py.
type TeiClient struct {
	baseURL           string
	backend           string // tei | ollama
	model             string
	truncateChars     int
	http              *http.Client
	maxRetries        int
	breakerThreshold  int
	consecutiveFailed int
}

// TeiUnavailable is the typed retryable error (EMBED_UNAVAILABLE class).
type TeiUnavailable struct{ Detail string }

func (e *TeiUnavailable) Error() string { return "TEI unavailable: " + e.Detail }

// NewTeiClient validates backend ∈ {tei, ollama}.
func NewTeiClient(baseURL, backend, model string, truncateChars int) (*TeiClient, error) {
	backend = strings.ToLower(backend)
	if backend != "tei" && backend != "ollama" {
		return nil, fmt.Errorf("unsupported EMBED_BACKEND: %s", backend)
	}
	return &TeiClient{
		baseURL:          strings.TrimRight(baseURL, "/"),
		backend:          backend,
		model:            model,
		truncateChars:    truncateChars,
		http:             &http.Client{Timeout: 60 * time.Second},
		maxRetries:       5,
		breakerThreshold: 5,
	}, nil
}

// Embed POSTs one batch. Transport errors and 429/503 retry; any other 4xx
// is a caller bug and fails fast without retry; the breaker aborts
// mid-ladder.
func (c *TeiClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c.consecutiveFailed >= c.breakerThreshold {
		return nil, &TeiUnavailable{Detail: fmt.Sprintf("circuit open after %d consecutive failures", c.consecutiveFailed)}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if c.truncateChars > 0 {
		for i, t := range texts {
			if len(t) > c.truncateChars {
				texts[i] = t[:c.truncateChars]
			}
		}
	}
	var payload any
	var endpoint string
	if c.backend == "ollama" {
		endpoint = c.baseURL + "/api/embed"
		payload = map[string]any{"model": c.model, "input": texts}
	} else {
		endpoint = c.baseURL + "/embed"
		payload = map[string]any{"inputs": texts}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	delay := 500 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			c.consecutiveFailed++
			lastErr = &TeiUnavailable{Detail: "transport error: " + err.Error()}
		case resp.StatusCode == 429 || resp.StatusCode == 503:
			resp.Body.Close()
			c.consecutiveFailed++
			lastErr = &TeiUnavailable{Detail: fmt.Sprintf("TEI returned %d", resp.StatusCode)}
		case resp.StatusCode >= 400:
			buf, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			resp.Body.Close()
			// caller bug: fail fast, no retry
			return nil, &TeiUnavailable{Detail: fmt.Sprintf("TEI returned %d: %s", resp.StatusCode, string(buf))}
		default:
			var data any
			err := json.NewDecoder(resp.Body).Decode(&data)
			resp.Body.Close()
			if err != nil {
				c.consecutiveFailed++
				lastErr = &TeiUnavailable{Detail: "bad response body: " + err.Error()}
				break
			}
			c.consecutiveFailed = 0
			// ollama wraps vectors as {"embeddings": [[...]]}; TEI returns
			// bare [[...]]. Normalize both to bare [[...]] for callers.
			switch v := data.(type) {
			case []any:
				out, err := toVectors(v)
				if err != nil {
					return nil, &TeiUnavailable{Detail: err.Error()}
				}
				return out, nil
			case map[string]any:
				if em, ok := v["embeddings"].([]any); ok {
					out, err := toVectors(em)
					if err != nil {
						return nil, &TeiUnavailable{Detail: err.Error()}
					}
					return out, nil
				}
				msg := fmt.Sprintf("unexpected embedding response shape: %v", v)
				if len(msg) > 100 {
					msg = msg[:100]
				}
				return nil, &TeiUnavailable{Detail: msg}
			default:
				return nil, &TeiUnavailable{Detail: "unexpected embedding response shape"}
			}
		}
		// breaker: cross the threshold → abort the ladder immediately
		if c.consecutiveFailed >= c.breakerThreshold {
			return nil, &TeiUnavailable{Detail: fmt.Sprintf("circuit open after %d consecutive failures", c.consecutiveFailed)}
		}
		if attempt < c.maxRetries {
			jitter := time.Duration(rand.Int63n(int64(delay / 2)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay + jitter):
			}
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return nil, lastErr
}

func toVectors(rows []any) ([][]float32, error) {
	out := make([][]float32, 0, len(rows))
	for _, row := range rows {
		arr, ok := row.([]any)
		if !ok {
			return nil, fmt.Errorf("unexpected embedding row shape")
		}
		vec := make([]float32, 0, len(arr))
		for _, x := range arr {
			f, ok := x.(float64)
			if !ok {
				return nil, fmt.Errorf("unexpected embedding value shape")
			}
			vec = append(vec, float32(f))
		}
		out = append(out, vec)
	}
	return out, nil
}

// Health probes the backend: TEI /health, ollama GET / (no /health route).
func (c *TeiClient) Health(ctx context.Context) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(c.baseURL + "/health"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	resp, err := client.Get(c.baseURL + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}
