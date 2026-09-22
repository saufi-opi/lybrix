package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"time"
)

// DoclingClient is the docling-serve HTTP fallback (blueprint §4.2):
// POST {DOCLING_URL}/v1/convert/file — multipart: file + `body` JSON
// options; response {"document": {"md_content": ...}}.
//
// Resilience identical to 1.0's libs/embedding client: jittered exponential
// backoff on 429/503/transport, circuit breaker after 5 consecutive
// failures → typed retryable error; non-retryable 4xx fail-fast. Expected
// shard latency 15–20s → client timeout 120s, context-cancellable.
type DoclingClient struct {
	baseURL    string
	http       *http.Client
	maxRetries int
	// breaker
	breakerThreshold  int
	consecutiveFailed int
}

// NewDoclingClient builds a client against the docling-serve base URL.
func NewDoclingClient(baseURL string) *DoclingClient {
	return &DoclingClient{
		baseURL:          baseURL,
		http:             &http.Client{Timeout: 120 * time.Second},
		maxRetries:       5,
		breakerThreshold: 5,
	}
}

// DoclingUnavailable is the typed retryable error (OCR_FAILED class).
type DoclingUnavailable struct{ Detail string }

func (e *DoclingUnavailable) Error() string { return "docling unavailable: " + e.Detail }

// ErrCircuitOpen reports the breaker state.
type ErrCircuitOpen struct{ Failures int }

func (e *ErrCircuitOpen) Error() string {
	return fmt.Sprintf("circuit open after %d consecutive failures", e.Failures)
}

// convertOptions is the `body` JSON field docling-serve expects.
type convertOptions struct {
	ToFormats        []string `json:"to_formats"`
	DoOCR            bool     `json:"do_ocr"`
	DoTableStructure bool     `json:"do_table_structure"`
}

// Convert posts one whole shard PDF and returns its markdown.
func (c *DoclingClient) Convert(ctx context.Context, pdfPath string, doTableStructure bool) (string, error) {
	return c.ConvertNamed(ctx, pdfPath, "shard.pdf", doTableStructure)
}

// ConvertNamed is Convert with an explicit upload filename — EPUB shards
// must arrive as *.epub or docling-serve's format sniffing fights the
// extension.
func (c *DoclingClient) ConvertNamed(ctx context.Context, path, filename string, doTableStructure bool) (string, error) {
	if c.consecutiveFailed >= c.breakerThreshold {
		return "", &ErrCircuitOpen{Failures: c.consecutiveFailed}
	}
	pdfBytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	opts := convertOptions{
		ToFormats:        []string{"md"},
		DoOCR:            true,
		DoTableStructure: doTableStructure,
	}
	optsJSON, _ := json.Marshal(opts)

	delay := 500 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		// docling-serve multipart: the file part first, then the options
		// JSON as a "body" form field — the documented shape for
		// /v1/convert/file.
		fw, err := mw.CreateFormFile("files", filename)
		if err != nil {
			return "", err
		}
		if _, err := fw.Write(pdfBytes); err != nil {
			return "", err
		}
		if err := mw.WriteField("body", string(optsJSON)); err != nil {
			return "", err
		}
		if err := mw.Close(); err != nil {
			return "", err
		}
		contentType := mw.FormDataContentType()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/v1/convert/file", &body)
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := c.http.Do(req)
		if err != nil {
			c.recordFailure()
			lastErr = &DoclingUnavailable{Detail: "transport error: " + err.Error()}
		} else if resp.StatusCode == 429 || resp.StatusCode == 503 {
			resp.Body.Close()
			c.recordFailure()
			lastErr = &DoclingUnavailable{Detail: fmt.Sprintf("docling returned %d", resp.StatusCode)}
		} else if resp.StatusCode >= 400 {
			// caller bug / bad request: fail fast, no retry
			buf := make([]byte, 200)
			n, _ := resp.Body.Read(buf)
			resp.Body.Close()
			return "", &DoclingUnavailable{Detail: fmt.Sprintf("docling returned %d: %s", resp.StatusCode, string(buf[:n]))}
		} else {
			var payload struct {
				Document struct {
					MDContent string `json:"md_content"`
				} `json:"document"`
			}
			err := json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			if err != nil {
				c.recordFailure()
				lastErr = &DoclingUnavailable{Detail: "bad response body: " + err.Error()}
			} else {
				c.consecutiveFailed = 0
				return payload.Document.MDContent, nil
			}
		}
		if attempt < c.maxRetries {
			jitter := time.Duration(rand.Int63n(int64(delay / 2)))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay + jitter):
			}
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return "", lastErr
}

func (c *DoclingClient) recordFailure() {
	c.consecutiveFailed++
	if c.consecutiveFailed >= c.breakerThreshold {
		// the next call aborts immediately with ErrCircuitOpen
		_ = c.consecutiveFailed
	}
}

// Health probes docling-serve (best-effort).
func (c *DoclingClient) Health(ctx context.Context) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(c.baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
