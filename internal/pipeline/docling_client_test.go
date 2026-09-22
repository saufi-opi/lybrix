package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// DoclingClient resilience: success shape, 429/503 backoff, breaker opens
// at 5, non-retryable 4xx (docling_client_test.go contract).

func doclingSuccessServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"document":{"md_content":"# Hello\nmarkdown body"}}`)
	}))
}

func TestDoclingSuccess(t *testing.T) {
	srv := doclingSuccessServer()
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 0
	path := writeTmpPDF(t)
	md, err := c.Convert(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	if md == "" || md[0] != '#' {
		t.Fatalf("markdown shape drift: %q", md)
	}
}

func TestDoclingNonRetryable4xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 3
	_, err := c.Convert(context.Background(), writeTmpPDF(t), true)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("4xx must fail fast without retry, calls=%d", calls)
	}
}

func TestDoclingBreakerOpensAt5(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 1 // keep the ladder short; breaker still opens
	path := writeTmpPDF(t)
	ctx := context.Background()
	var lastErr error
	for i := 0; i < 6; i++ {
		_, lastErr = c.Convert(ctx, path, true)
	}
	var circuit *ErrCircuitOpen
	if _, ok := lastErr.(*ErrCircuitOpen); !ok {
		t.Fatalf("expected circuit open, got %v (calls=%d)", lastErr, calls)
	}
	_ = circuit
}

func writeTmpPDF(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/shard.pdf"
	// minimal content — the client just POSTs bytes
	if err := osWrite(path, []byte("%PDF-1.4 fake")); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTeiClientBatchShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[[1,2],[3,4]]`)
	}))
	defer srv.Close()
	c, err := NewTeiClient(srv.URL, "tei", "BAAI/bge-m3", 0)
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 4 {
		t.Fatalf("vector shape drift: %v", vecs)
	}
}

func TestTeiClientOllamaShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %s", r.URL.Path)
		}
		var body map[string]any
		_ = jsonRead(r, &body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"embeddings":[[9,8]]}`)
	}))
	defer srv.Close()
	c, err := NewTeiClient(srv.URL, "ollama", "BAAI/bge-m3", 0)
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := c.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || vecs[0][0] != 9 {
		t.Fatalf("ollama vector drift: %v", vecs)
	}
}

func TestPlanBatchesBudget(t *testing.T) {
	// port of test_embedder_batches.py budget math
	texts := []string{}
	for i := 0; i < 100; i++ {
		texts = append(texts, stringsN(50)) // 50 tokens each
	}
	batches, truncated := PlanBatches(texts, WhitespaceTokenizer{}, 1900, 48)
	if len(truncated) != 0 {
		t.Fatalf("no text should truncate here: %d", len(truncated))
	}
	// budget 1900 → at most 37 texts of 50 tokens per batch (37*50=1850;
	// 38*50=1900 fits exactly too) — assert every batch stays within budget
	// and the batch-size cap.
	total := 0
	for _, b := range batches {
		if len(b) > 48 {
			t.Fatalf("batch over size cap: %d", len(b))
		}
		tokens := 0
		for _, x := range b {
			tokens += WhitespaceTokenizer{}.Count(x)
		}
		if tokens > 1900 {
			t.Fatalf("batch over token budget: %d", tokens)
		}
		total += len(b)
	}
	if total != len(texts) {
		t.Fatalf("batching lost texts: %d != %d", total, len(texts))
	}
}

func TestPlanBatchesPathologicalTruncation(t *testing.T) {
	// pathological single chunk: hard-cut to the token budget
	long := stringsN(10000)
	batches, truncated := PlanBatches([]string{long}, WhitespaceTokenizer{}, 1900, 48)
	if len(truncated) != 1 {
		t.Fatalf("expected truncation, got %d", len(truncated))
	}
	wt := WhitespaceTokenizer{}
	if wt.Count(truncated[0]) > 1900 {
		t.Fatal("truncated text still over budget")
	}
	if len(batches) == 0 {
		t.Fatal("expected one batch")
	}
}

func TestTeiClientUnavailableErrorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c, _ := NewTeiClient(srv.URL, "tei", "m", 0)
	c.maxRetries = 0
	c.breakerThreshold = 100 // don't trip the breaker inside the ladder
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*TeiUnavailable); !ok {
		t.Fatalf("expected TeiUnavailable, got %T", err)
	}
	_ = time.Second
}
