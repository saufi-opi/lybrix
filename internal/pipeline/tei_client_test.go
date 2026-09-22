package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// openai response shape normalization.
func TestEmbedClientOpenAIShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected /v1/embeddings, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("missing bearer header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[1.5,2.5]},{"embedding":[3.5,4.5]}]}`)
	}))
	defer srv.Close()
	c, err := NewEmbedClient(EmbedSpec{Provider: "openai", ModelID: "text-embedding-3-small",
		IngestURL: srv.URL, QueryURL: srv.URL, APIKey: "sk-test"}, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := c.Embed(context.Background(), []string{"a", "b"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1.5 || vecs[1][1] != 4.5 {
		t.Fatalf("openai vector drift: %v", vecs)
	}
}

func TestEmbedClientOpenAIErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	c, _ := NewEmbedClient(EmbedSpec{Provider: "openai", ModelID: "m",
		IngestURL: srv.URL, QueryURL: srv.URL, APIKey: "sk"}, "ingest")
	c.maxRetries = 0
	_, err := c.Embed(context.Background(), []string{"x"}, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

// plane URL selection: query plane picks QueryURL, not IngestURL.
func TestEmbedClientPlaneURLSelection(t *testing.T) {
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("query call must not hit the ingest URL")
		w.WriteHeader(500)
	}))
	defer ingest.Close()
	query := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("expected /embed, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[[7]]`)
	}))
	defer query.Close()
	c, _ := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "m",
		IngestURL: ingest.URL, QueryURL: query.URL}, "query")
	vecs, err := c.Embed(context.Background(), []string{"q"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if vecs[0][0] != 7 {
		t.Fatalf("query plane drift: %v", vecs)
	}
}

// dim guard: match passes, mismatch is a non-retryable DimMismatchError.
func TestEmbedClientDimGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[[1,2,3]]`)
	}))
	defer srv.Close()
	c, _ := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "m",
		IngestURL: srv.URL, QueryURL: srv.URL}, "ingest")
	if _, err := c.Embed(context.Background(), []string{"x"}, 3); err != nil {
		t.Fatalf("matching dim rejected: %v", err)
	}
	_, err := c.Embed(context.Background(), []string{"x"}, 1024)
	dm, ok := err.(*DimMismatchError)
	if !ok {
		t.Fatalf("expected DimMismatchError, got %T", err)
	}
	if dm.Expected != 1024 || dm.Actual != 3 {
		t.Fatalf("dim mismatch fields drift: %+v", dm)
	}
}

// probe parsing: health endpoint per provider + detected dim.
func TestEmbedClientProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"ok":true}`)
		case "/embed":
			fmt.Fprint(w, `[[1,2,3,4]]`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, _ := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "m",
		IngestURL: srv.URL, QueryURL: srv.URL}, "ingest")
	reachable, _, dim, detail := c.Probe(context.Background())
	if !reachable || dim != 4 || detail != "ok" {
		t.Fatalf("probe drift: reachable=%v dim=%d detail=%s", reachable, dim, detail)
	}

	// unreachable
	c2, _ := NewEmbedClient(EmbedSpec{Provider: "tei", ModelID: "m",
		IngestURL: "http://127.0.0.1:1", QueryURL: "http://127.0.0.1:1"}, "ingest")
	if reachable, _, _, _ := c2.Probe(context.Background()); reachable {
		t.Fatal("unreachable host must not probe true")
	}
}

// provider validation.
func TestNewEmbedClientValidation(t *testing.T) {
	if _, err := NewEmbedClient(EmbedSpec{Provider: "hf"}, "ingest"); err == nil {
		t.Fatal("bad provider accepted")
	}
	if _, err := NewEmbedClient(EmbedSpec{Provider: "tei"}, "warp"); err == nil {
		t.Fatal("bad plane accepted")
	}
}
