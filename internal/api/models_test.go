package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// validateModelCreate: the 422 contract surface.
func TestValidateModelCreate(t *testing.T) {
	base := modelCreate{
		Name: "bge-m3 @ ingest", Provider: "tei", ModelID: "BAAI/bge-m3",
		IngestURL: "http://host:8081", QueryURL: "http://host:8082", VectorDim: 1024,
	}
	if msg := validateModelCreate(base); msg != "" {
		t.Fatalf("valid create rejected: %s", msg)
	}
	cases := []struct {
		mutate func(*modelCreate)
		want   string
	}{
		{func(b *modelCreate) { b.Name = "" }, "name must be 1-200 characters"},
		{func(b *modelCreate) { b.Name = strings.Repeat("x", 201) }, "name must be 1-200 characters"},
		{func(b *modelCreate) { b.Provider = "hf" }, "provider must be one of tei|ollama|openai"},
		{func(b *modelCreate) { b.ModelID = "" }, "field required: model_id"},
		{func(b *modelCreate) { b.IngestURL = "ftp://x" }, "ingest_url must parse as an http(s) URL"},
		{func(b *modelCreate) { b.QueryURL = "not a url" }, "query_url must parse as an http(s) URL"},
		{func(b *modelCreate) { b.VectorDim = 0 }, "vector_dim must be between 1 and 2000"},
		{func(b *modelCreate) { b.VectorDim = 2001 }, "vector_dim must be between 1 and 2000"},
		{func(b *modelCreate) { b.Provider = "openai" }, "openai provider requires api_key on create"},
	}
	for _, c := range cases {
		b := base
		c.mutate(&b)
		if got := validateModelCreate(b); got != c.want {
			t.Fatalf("validate mismatch: got %q want %q", got, c.want)
		}
	}
	// openai with key passes
	b := base
	b.Provider = "openai"
	b.APIKey = strPtr("sk-x")
	if msg := validateModelCreate(b); msg != "" {
		t.Fatalf("openai with key rejected: %s", msg)
	}
}

// normalizeCreate fills the registry defaults.
func TestNormalizeCreate(t *testing.T) {
	b := modelCreate{Name: "m", Provider: "tei", ModelID: "x",
		IngestURL: "http://a", QueryURL: "http://b", VectorDim: 8}
	normalizeCreate(&b)
	if b.QueryPrefix != "search_query: " || b.BatchSize != 48 || b.CtxBudget != 1900 || b.TruncateChars != 6000 {
		t.Fatalf("normalize drift: %+v", b)
	}
}

// confirmDim with an unreachable probe proceeds (no refusal).
func TestConfirmDimUnreachableProceeds(t *testing.T) {
	b := modelCreate{Name: "m", Provider: "tei", ModelID: "x",
		IngestURL: "http://127.0.0.1:1", QueryURL: "http://127.0.0.1:1", VectorDim: 1024}
	detail, _ := confirmDim(context.Background(), b)
	if detail != "" {
		// unreachable probes must NOT refuse the save
		t.Fatalf("unreachable probe must proceed, got %q", detail)
	}
}

// handleTestModel against a live probe server: reachable + detected dim.
func TestHandleTestModelProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/embed" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[[1,2,3,4,5,6,7,8]]`))
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	}))
	defer srv.Close()

	s := New(Deps{})
	body := `{"name":"m","provider":"tei","model_id":"x","ingest_url":"` + srv.URL +
		`","query_url":"` + srv.URL + `","vector_dim":8}`
	req := httptest.NewRequest(http.MethodPost, "/v1/models/test", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleTestModel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("test probe status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"reachable":true`) || !strings.Contains(rec.Body.String(), `"vector_dim":8`) {
		t.Fatalf("probe payload drift: %s", rec.Body.String())
	}
}

// handleTestModel with a mismatched dim still returns 200 with the DETECTED
// dim (test is a dry run — the 422 refusal lives on save).
func TestHandleTestModelDetectsMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embed" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[[1,2,3]]`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := New(Deps{})
	body := `{"name":"m","provider":"tei","model_id":"x","ingest_url":"` + srv.URL +
		`","query_url":"` + srv.URL + `","vector_dim":1024}`
	req := httptest.NewRequest(http.MethodPost, "/v1/models/test", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleTestModel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("test probe status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"vector_dim":3`) {
		t.Fatalf("detected dim drift: %s", rec.Body.String())
	}
}

// validateModelCreate rejections must surface as 422 via the handler.
func TestHandleCreateModelValidation422(t *testing.T) {
	s := New(Deps{})
	req := httptest.NewRequest(http.MethodPost, "/v1/models",
		strings.NewReader(`{"name":"m","provider":"tei","model_id":"","ingest_url":"http://a","query_url":"http://b","vector_dim":1024}`))
	rec := httptest.NewRecorder()
	s.handleCreateModel(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "field required: model_id") {
		t.Fatalf("detail drift: %s", rec.Body.String())
	}
}
