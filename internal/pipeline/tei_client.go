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

// EmbedClient: embedding HTTP client for the three v1 providers — tei,
// ollama, openai — with batching, jittered exponential backoff, circuit
// breaker (PRD §6.4 client resilience).
//
// If the embed backend is down, the embed job retries later — it must not
// fail the book and throw away the parse work already paid for. Hence:
// jittered exponential backoff on 429/503, and a circuit breaker after 5
// consecutive failures that raises a retryable EMBED_UNAVAILABLE-class
// error instead of hammering a dead endpoint. Port of
// libs/embedding/client.py.
type EmbedClient struct {
	spec              EmbedSpec
	plane             string // "ingest" | "query"
	http              *http.Client
	maxRetries        int
	breakerThreshold  int
	consecutiveFailed int
}

// EmbedSpec is one registry row's embedding-relevant config. The URL used
// per call is picked by plane (two-plane rule §4.3: ingest and query never
// share a server).
type EmbedSpec struct {
	Provider      string // tei | ollama | openai
	ModelID       string
	IngestURL     string
	QueryURL      string
	APIKey        string // openai only
	TruncateChars int
}

// TeiUnavailable is the typed retryable error (EMBED_UNAVAILABLE class).
type TeiUnavailable struct{ Detail string }

func (e *TeiUnavailable) Error() string { return "TEI unavailable: " + e.Detail }

// DimMismatchError is the non-retryable client guard: the backend returned
// a vector of the wrong dimensionality — declared model dim and actual
// output disagree, so writing the vector would poison the index.
type DimMismatchError struct {
	Expected int
	Actual   int
}

func (e *DimMismatchError) Error() string {
	return fmt.Sprintf("embed dim mismatch: model declares %d, backend returned %d", e.Expected, e.Actual)
}

// NewEmbedClient validates provider ∈ {tei, ollama, openai} and picks the
// plane URL. Breaker/backoff parameters are untouched from TeiClient.
func NewEmbedClient(spec EmbedSpec, plane string) (*EmbedClient, error) {
	spec.Provider = strings.ToLower(spec.Provider)
	switch spec.Provider {
	case "tei", "ollama", "openai":
	default:
		return nil, fmt.Errorf("unsupported embed provider: %s", spec.Provider)
	}
	if plane != "ingest" && plane != "query" {
		return nil, fmt.Errorf("unknown embed plane: %s", plane)
	}
	return &EmbedClient{
		spec:             spec,
		plane:            plane,
		http:             &http.Client{Timeout: 60 * time.Second},
		maxRetries:       5,
		breakerThreshold: 5,
	}, nil
}

// baseURL resolves the plane endpoint at call time (the spec carries both).
func (c *EmbedClient) baseURL() string {
	if c.plane == "query" {
		return strings.TrimRight(c.spec.QueryURL, "/")
	}
	return strings.TrimRight(c.spec.IngestURL, "/")
}

// Embed POSTs one batch with the expectedDim guard (0 = unchecked). Ingest
// and query callers always pass the model's declared dim. Transport errors
// and 429/503 retry; any other 4xx is a caller bug and fails fast without
// retry; the breaker aborts mid-ladder.
func (c *EmbedClient) Embed(ctx context.Context, texts []string, expectedDim int) ([][]float32, error) {
	if c.consecutiveFailed >= c.breakerThreshold {
		return nil, &TeiUnavailable{Detail: fmt.Sprintf("circuit open after %d consecutive failures", c.consecutiveFailed)}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if c.spec.TruncateChars > 0 {
		for i, t := range texts {
			// Rune-safe: a byte cut can split a multi-byte rune and emit
			// invalid UTF-8 into the embed request.
			texts[i] = TruncateRunes(t, c.spec.TruncateChars)
		}
	}
	var payload any
	var endpoint string
	switch c.spec.Provider {
	case "ollama":
		endpoint = c.baseURL() + "/api/embed"
		payload = map[string]any{"model": c.spec.ModelID, "input": texts}
	case "openai":
		endpoint = c.baseURL() + "/v1/embeddings"
		payload = map[string]any{"model": c.spec.ModelID, "input": texts}
	default: // tei
		endpoint = c.baseURL() + "/embed"
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
		if c.spec.Provider == "openai" && c.spec.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.spec.APIKey)
		}
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
			out, derr := decodeVectors(data)
			if derr != nil {
				return nil, &TeiUnavailable{Detail: derr.Error()}
			}
			if expectedDim > 0 {
				for _, vec := range out {
					if len(vec) != expectedDim {
						return nil, &DimMismatchError{Expected: expectedDim, Actual: len(vec)}
					}
				}
			}
			return out, nil
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

// decodeVectors normalizes the three response shapes to bare [][]float32:
// tei returns bare [[...]]; ollama wraps as {"embeddings": [[...]]};
// openai wraps as {"data": [{"embedding": [...]}]} with an error body of
// {"error": {"message": ...}}.
func decodeVectors(data any) ([][]float32, error) {
	switch v := data.(type) {
	case []any:
		return toVectors(v)
	case map[string]any:
		if em, ok := v["embeddings"].([]any); ok {
			return toVectors(em)
		}
		if dl, ok := v["data"].([]any); ok {
			out := make([][]float32, 0, len(dl))
			for _, row := range dl {
				obj, ok := row.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("unexpected openai data row shape")
				}
				emb, ok := obj["embedding"].([]any)
				if !ok {
					return nil, fmt.Errorf("unexpected openai embedding shape")
				}
				vec, err := toVectors([]any{emb})
				if err != nil {
					return nil, err
				}
				out = append(out, vec[0])
			}
			return out, nil
		}
		if er, ok := v["error"].(map[string]any); ok {
			if msg, ok := er["message"].(string); ok {
				return nil, fmt.Errorf("provider error: %s", msg)
			}
		}
		msg := fmt.Sprintf("unexpected embedding response shape: %v", v)
		if len(msg) > 100 {
			msg = msg[:100]
		}
		return nil, fmt.Errorf("%s", msg)
	default:
		return nil, fmt.Errorf("unexpected embedding response shape")
	}
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

// Probe checks provider health then one 1-token embed of "ping" for the
// true output dim — powers /v1/models/test and save-time dim confirmation.
// Returns (reachable, latencyMS, detectedDim, detail); detectedDim is 0
// when the embed probe could not run.
func (c *EmbedClient) Probe(ctx context.Context) (bool, int, int, string) {
	healthPath := "/health"
	switch c.spec.Provider {
	case "ollama":
		healthPath = "/api/tags"
	case "openai":
		healthPath = "/v1/models"
	}
	started := time.Now()
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+healthPath, nil)
	if err != nil {
		return false, 0, 0, "probe request build failed"
	}
	if c.spec.Provider == "openai" && c.spec.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.spec.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, 0, "unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, 0, 0, fmt.Sprintf("health returned %d", resp.StatusCode)
	}

	// dim probe: one 1-token embed
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	vecs, err := c.Embed(ctx2, []string{"ping"}, 0)
	if err != nil {
		return true, int(time.Since(started).Milliseconds()), 0, "healthy; dim probe failed: " + err.Error()
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return true, int(time.Since(started).Milliseconds()), 0, "healthy; dim probe returned empty vector"
	}
	return true, int(time.Since(started).Milliseconds()), len(vecs[0]), "ok"
}
