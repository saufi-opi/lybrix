package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
)

// rerankDeps wires a stubbed rerank pipeline: one bound reranker for
// collection "a", none elsewhere.
func rerankDeps(rerankErr error, reranked func([]*store.SearchHit) []*store.SearchHit) Deps {
	rr := &store.RerankModel{ID: "rr1", Provider: "tei", ModelID: "bge"}
	deps := Deps{
		Settings: config.DefaultSettings(),
		EmbedQuery: func(context.Context, string, string) ([]float32, *store.EmbeddingModel, error) {
			return make([]float32, 1024), &store.EmbeddingModel{VectorDim: 1024}, nil
		},
		HybridSearch: func(_ context.Context, _ []float32, _ int, _ string, _ []string, _ string, limit int, _ string, _ []any) ([]*store.SearchHit, error) {
			// the pool must be at least RERANK_CANDIDATES when reranking
			hits := []*store.SearchHit{
				{ChunkID: "c1", Text: "one", Score: 0.5},
				{ChunkID: "c2", Text: "two", Score: 0.4},
			}
			if limit < 30 {
				hits = hits[:1]
			}
			return hits, nil
		},
		ResolveReranker: func(_ context.Context, req service.RerankResolveRequest) (*store.RerankModel, error) {
			if req.Rerank != nil && !*req.Rerank {
				return nil, nil
			}
			if req.RerankModelID == "unknown" {
				return nil, service.ErrRerankModelUnknown
			}
			if req.SingleCollection == "a" || req.RerankModelID == "rr1" {
				return rr, nil
			}
			if req.Rerank != nil && *req.Rerank {
				return nil, service.ErrRerankNotConfigured
			}
			return nil, nil
		},
	}
	deps.RerankHits = func(_ context.Context, rm *store.RerankModel, _ string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, service.RerankStatus, error) {
		if rerankErr != nil {
			// mirror the graceful degradation contract: RRF pool truncated
			return hits[:min(topK, len(hits))], service.RerankStatus{Applied: false, Reason: rerankErr.Error()}, nil
		}
		out := hits
		if reranked != nil {
			out = reranked(hits)
		}
		for _, h := range out {
			h.Reranked = true
			h.Score = 0.99
		}
		return out[:min(topK, len(out))], service.RerankStatus{Applied: true}, nil
	}
	return deps
}

// A bound reranker reranks the single-collection path: hits carry
// reranked:true, the header says applied, and the pool widened.
func TestSearchRerankApplied(t *testing.T) {
	deps := rerankDeps(nil, func(h []*store.SearchHit) []*store.SearchHit {
		// reverse to prove reordering
		return []*store.SearchHit{h[len(h)-1], h[0]}
	})
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"x","collection":"a"}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Lybrix-Rerank"); got != "applied" {
		t.Fatalf("rerank header drift: %s", got)
	}
	if !strings.Contains(rec.Body.String(), `"reranked":true`) {
		t.Fatalf("reranked flag drift: %s", rec.Body.String())
	}
}

// rerank failure degrades: 200 with RRF order kept and header degraded.
func TestSearchRerankDegrades(t *testing.T) {
	deps := rerankDeps(errors.New("reranker down"), nil)
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"x","collection":"a"}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rerank failure must never 500: got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Lybrix-Rerank"); got != "degraded" {
		t.Fatalf("degraded header drift: %s", got)
	}
	if strings.Contains(rec.Body.String(), `"reranked":true`) {
		t.Fatalf("degraded hits must not carry reranked:true: %s", rec.Body.String())
	}
}

// rerank:false disables the collection binding (header off).
func TestSearchRerankExplicitOff(t *testing.T) {
	deps := rerankDeps(nil, nil)
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"x","collection":"a","rerank":false}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if got := rec.Header().Get("X-Lybrix-Rerank"); got != "off" {
		t.Fatalf("explicit off header drift: %s", got)
	}
}

// rerank:true with no bound reranker → 422 "no reranker configured".
func TestSearchRerankNotConfigured(t *testing.T) {
	deps := rerankDeps(nil, nil)
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"x","collection":"b","rerank":true}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no reranker configured") {
		t.Fatalf("detail drift: %s", rec.Body.String())
	}
}

// unknown explicit rerank_model_id → 404.
func TestSearchRerankModelUnknown(t *testing.T) {
	deps := rerankDeps(nil, nil)
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"x","collection":"a","rerank_model_id":"unknown"}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// metadata_filter validation errors surface as 422.
func TestSearchMetadataFilterValidation(t *testing.T) {
	deps := rerankDeps(nil, nil)
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x","metadata_filter":{"bad key!":"v"}}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}
