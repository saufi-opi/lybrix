package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/store"
)

// multiSearchDeps fakes the search wiring: 768-dim model for collection
// "a", 1024-dim for "b" — with call counters so the grouped path must embed
// once per unique model, not per collection.
func multiSearchDeps() (Deps, *int, *int) {
	singleCalls, groupCalls := new(int), new(int)
	m768 := &store.EmbeddingModel{ID: "m768", VectorDim: 768}
	m1024 := &store.EmbeddingModel{ID: "m1024", VectorDim: 1024}
	deps := Deps{
		Settings: config.DefaultSettings(),
		EmbedQuery: func(_ context.Context, collection, _ string) ([]float32, *store.EmbeddingModel, error) {
			*singleCalls++
			if collection == "a" {
				return make([]float32, 768), m768, nil
			}
			return make([]float32, 1024), m1024, nil
		},
		EmbedForModel: func(_ context.Context, m *store.EmbeddingModel, _ string) ([]float32, error) {
			*groupCalls++
			if m.VectorDim == 768 {
				return make([]float32, 768), nil
			}
			return make([]float32, 1024), nil
		},
		HybridSearch: func(_ context.Context, _ []float32, _ int, _ string, _ []string, _ string, _ int) ([]*store.SearchHit, error) {
			return []*store.SearchHit{{ChunkID: "c-single", Score: 1.0}}, nil
		},
		MultiSearch: func(_ context.Context, _ string, collections, _ []string, _ int,
			embedFn func(*store.EmbeddingModel) ([]float32, error)) ([]*store.SearchHit, error) {
			// replicate the grouped contract: one embed call per unique model;
			// empty collections = all (the two fixture collections)
			dims := []int{768, 1024}
			if len(collections) == 1 && collections[0] == "a" {
				dims = []int{768}
			}
			for _, dim := range dims {
				if _, err := embedFn(&store.EmbeddingModel{VectorDim: dim}); err != nil {
					return nil, err
				}
			}
			return []*store.SearchHit{{ChunkID: "c1", Score: 1.0}}, nil
		},
	}
	return deps, singleCalls, groupCalls
}

// The multi-collection request routes to the grouped path: EmbedForModel is
// the embed entry point (once per unique model — collections a and b bind
// different-dimension models), and the fused results flow out the standard
// hit mapping.
func TestSearchRoutesMultiCollectionToGroupedPath(t *testing.T) {
	deps, single, group := multiSearchDeps()
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x","collections":["a","b"]}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if *single != 0 {
		t.Fatalf("multi-collection search must not use the single-model embedder")
	}
	if *group != 2 {
		t.Fatalf("grouped search must embed once per unique model (2), got %d", *group)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"chunk_id":"c1"`) {
		t.Fatalf("fused hit mapping drift: %s", rec.Body.String())
	}
}

// An embedder failure inside the grouped path surfaces as a 500 detail
// envelope — never silently degrades to BM25-only.
func TestSearchMultiCollectionEmbedError(t *testing.T) {
	deps, _, _ := multiSearchDeps()
	deps.EmbedForModel = func(context.Context, *store.EmbeddingModel, string) ([]float32, error) {
		return nil, errors.New("embed backend down")
	}
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x","collections":["a","b"]}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "embed backend down") {
		t.Fatalf("detail drift: %s", rec.Body.String())
	}
}

// No collection at all — the DEFAULT request shape — routes to the grouped
// multi-collection fusion across all collections (WeKnora multi-KB: the
// corpus, not one default model, is the target).
func TestSearchUnspecifiedRoutesToGroupedPath(t *testing.T) {
	deps, single, group := multiSearchDeps()
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x"}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if *single != 0 {
		t.Fatalf("unspecified search must not use the single-model embedder")
	}
	if *group != 2 {
		t.Fatalf("unspecified search groups all collections (2 unique models), got %d", *group)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A single-collection request keeps the legacy single-model path exactly.
func TestSearchSingleCollectionKeepsSinglePath(t *testing.T) {
	deps, single, group := multiSearchDeps()
	s := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x","collection":"a"}`))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if *single != 1 || *group != 0 {
		t.Fatalf("single-collection must ride the single-model path: single=%d group=%d", *single, *group)
	}
}
