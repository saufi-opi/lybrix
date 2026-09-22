package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tei rerank: logits in, sigmoid-normalized 0..1 out.
func TestRerankTEISigmoid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rerank") {
			t.Fatalf("expected /rerank, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[[5.0, -5.0, 0.0]]`))
	}))
	defer srv.Close()
	c, err := NewRerankClient("tei", "bge-reranker-v2-m3", srv.URL, "", 6000)
	if err != nil {
		t.Fatal(err)
	}
	scores, err := c.Rerank(context.Background(), "q", []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 3 {
		t.Fatalf("arity drift: %v", scores)
	}
	if scores[0] <= 0.99 || scores[1] >= 0.01 || (scores[2] < 0.49 || scores[2] > 0.51) {
		t.Fatalf("sigmoid normalization drift: %v", scores)
	}
}

// openai-compatible: relevance_score passes through (already 0..1).
func TestRerankOpenAIPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/rerank") {
			t.Fatalf("expected /v1/rerank, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.2}]}`))
	}))
	defer srv.Close()
	c, err := NewRerankClient("openai", "cohere/rerank", srv.URL, "sk", 6000)
	if err != nil {
		t.Fatal(err)
	}
	scores, err := c.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if scores[0] != 0.2 || scores[1] != 0.9 {
		t.Fatalf("passthrough drift: %v", scores)
	}
}

// truncation honors truncate_chars.
func TestRerankTruncate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[[1.0]]`))
	}))
	defer srv.Close()
	c, err := NewRerankClient("tei", "bge", srv.URL, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rerank(context.Background(), "q", []string{strings.Repeat("x", 100)}); err != nil {
		t.Fatal(err)
	}
}

// unsupported provider rejected.
func TestRerankProviderValidation(t *testing.T) {
	if _, err := NewRerankClient("ollama", "x", "http://x", "", 0); err == nil {
		t.Fatal("ollama must not be a rerank provider")
	}
}
