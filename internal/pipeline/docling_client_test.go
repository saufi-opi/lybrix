package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	// after opening, further calls must be rejected without touching the
	// server (R-38: the breaker actually gates).
	before := calls
	_, err := c.Convert(ctx, path, true)
	if err == nil {
		t.Fatal("call after open must be rejected")
	}
	if _, ok := err.(*ErrCircuitOpen); !ok {
		t.Fatalf("expected ErrCircuitOpen after open, got %v", err)
	}
	if calls != before {
		t.Fatalf("open breaker must not reach the server: %d -> %d", before, calls)
	}
}

// TestDoclingBreakerCooldownRecovers (R-38): after opening, once the
// cooldown elapses against a now-healthy server the next Convert succeeds
// and the breaker is closed (success resets the counter).
func TestDoclingBreakerCooldownRecovers(t *testing.T) {
	fail := true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bad := fail
		mu.Unlock()
		if bad {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"document":{"md_content":"# recovered"}}`)
	}))
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 0
	c.breakerCooldown = 10 * time.Millisecond
	path := writeTmpPDF(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := c.Convert(ctx, path, true); err == nil {
			t.Fatal("expected failure while unhealthy")
		}
	}
	if _, err := c.Convert(ctx, path, true); err == nil {
		t.Fatal("open breaker must reject inside the cooldown")
	}
	time.Sleep(30 * time.Millisecond) // let the cooldown elapse
	mu.Lock()
	fail = false
	mu.Unlock()
	md, err := c.Convert(ctx, path, true)
	if err != nil {
		t.Fatalf("probe against healthy server must succeed: %v", err)
	}
	if md == "" {
		t.Fatal("empty markdown from recovery probe")
	}
	c.mu.Lock()
	closed := c.consecutiveFailed == 0 && !c.probing
	c.mu.Unlock()
	if !closed {
		t.Fatal("successful probe must close the breaker")
	}
}

// TestDoclingBreakerProbeRetrips (R-38): cooldown elapses against a
// still-failing server → the probe fails, the breaker re-opens with a fresh
// cooldown, and an immediate next call is rejected without a server hit.
func TestDoclingBreakerProbeRetrips(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 0
	c.breakerCooldown = 10 * time.Millisecond
	path := writeTmpPDF(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = c.Convert(ctx, path, true)
	}
	if _, err := c.Convert(ctx, path, true); err == nil {
		t.Fatal("open breaker must reject inside the cooldown")
	}
	time.Sleep(30 * time.Millisecond) // cooldown elapses → probe runs
	if _, err := c.Convert(ctx, path, true); err == nil {
		t.Fatal("probe against failing server must fail")
	}
	before := calls
	_, err := c.Convert(ctx, path, true)
	if err == nil {
		t.Fatal("re-opened breaker must reject immediately")
	}
	if _, ok := err.(*ErrCircuitOpen); !ok {
		t.Fatalf("expected ErrCircuitOpen after probe re-trip, got %v", err)
	}
	if calls != before {
		t.Fatalf("re-opened breaker must not reach the server: %d -> %d", before, calls)
	}
}

// TestDoclingBreakerSingleProbe (R-38): with the cooldown elapsed and a slow
// server, exactly one concurrent Convert reaches the server; the other gets
// ErrCircuitOpen without a request.
func TestDoclingBreakerSingleProbe(t *testing.T) {
	requests := new(atomic.Int32)
	held := new(atomic.Int32) // requests that reached the probe window
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// only the post-cooldown probe is held; the 5 warmup failures must
		// answer 503 immediately or the warmup loop itself would block
		if requests.Add(1) <= 5 {
			w.WriteHeader(503)
			return
		}
		held.Add(1)
		<-release // hold the probe in flight
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"document":{"md_content":"# ok"}}`)
	}))
	defer srv.Close()
	c := NewDoclingClient(srv.URL)
	c.maxRetries = 0
	c.breakerCooldown = 10 * time.Millisecond
	path := writeTmpPDF(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = c.Convert(ctx, path, true)
	}
	time.Sleep(30 * time.Millisecond) // cooldown elapses
	type result struct {
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := c.Convert(ctx, path, true)
			results <- result{err: err}
		}()
	}
	// the probe is now blocked inside the server handler; the second call
	// must already have been rejected. Collect the first result, then let
	// the probe through so the goroutines can finish.
	var circuitOpen, succeeded, failed int
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err == nil:
			succeeded++
		default:
			var eo *ErrCircuitOpen
			if errors.As(r.err, &eo) {
				circuitOpen++
			} else {
				failed++
			}
		}
		if i == 0 {
			close(release)
		}
	}
	if succeeded != 1 || circuitOpen != 1 || failed != 0 {
		t.Fatalf("want exactly 1 success + 1 circuit-open, got %d/%d/%d (err mix)", succeeded, circuitOpen, failed)
	}
	if got := held.Load(); got != 1 {
		t.Fatalf("exactly one probe request must reach the server, got %d", got)
	}
	if got := requests.Load(); got != 6 {
		t.Fatalf("want 5 warmup failures + 1 probe, got %d requests", got)
	}
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
	c, err := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "BAAI/bge-m3", IngestURL: srv.URL, QueryURL: srv.URL}, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := c.Embed(context.Background(), []string{"a", "b"}, 0)
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
	c, err := NewEmbedClient(EmbedSpec{Provider: "ollama", ModelID: "BAAI/bge-m3", IngestURL: srv.URL, QueryURL: srv.URL}, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := c.Embed(context.Background(), []string{"hello"}, 0)
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
	c, _ := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "m", IngestURL: srv.URL, QueryURL: srv.URL}, "ingest")
	c.maxRetries = 0
	c.breakerThreshold = 100 // don't trip the breaker inside the ladder
	_, err := c.Embed(context.Background(), []string{"x"}, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*TeiUnavailable); !ok {
		t.Fatalf("expected TeiUnavailable, got %T", err)
	}
	_ = time.Second
}
