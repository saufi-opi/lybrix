// Package api: the REST control plane (chi router, port 8000).
//
// Contract discipline: every handler is written against the checked-in
// snapshot at services/web/openapi.json (18 paths). Error envelope is
// FastAPI-style {"detail": "..."} — the web SDK runs throwOnError and
// surfaces detail.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Deps carries the shared wiring every handler needs.
type Deps struct {
	Settings *config.Settings
	DB       *store.DB
	Redis    redis.Cmdable
	S3       *objectstore.Client
	// EmbedQuery produces a query-plane embedding for one collection
	// (resolves the bound model via the registry; the query plane NEVER
	// shares the ingest embed server — an 8-token query queued behind a
	// 65k-token ingest batch destroys p99). Returns the vector plus the
	// resolved model so handlers forward its dim into HybridSearch.
	EmbedQuery func(ctx context.Context, collection, text string) (vec []float32, model *store.EmbeddingModel, err error)
	// HybridSearch is the single-collection RRF fusion — production wiring
	// is db.HybridSearch; tests stub it.
	HybridSearch func(ctx context.Context, queryVec []float32, dim int, collection string, collectionScope []string, bm25Query string, limit int, metaFilter string, metaArgs []any) ([]*store.SearchHit, error)
	// EmbedForModel produces a query-plane embedding with a SPECIFIC
	// registered model — the multi-collection grouped search calls it once
	// per unique model (WeKnora multi-KB architecture; no default row).
	EmbedForModel func(ctx context.Context, m *store.EmbeddingModel, text string) ([]float32, error)
	// MultiSearch runs the grouped multi-collection fusion — production
	// wiring is DB.MultiHybridSearch; tests stub it.
	MultiSearch func(ctx context.Context, query string, collections, scope []string, topK int, metaFilter string, metaArgs []any, embedFn func(*store.EmbeddingModel) ([]float32, error)) ([]*store.SearchHit, error)
	// RerankHits is the cross-encoder rerank stage — production wiring
	// composes service.RerankHits with a registry-resolved client; tests
	// stub it. Nil means rerank is entirely unavailable (off).
	RerankHits func(ctx context.Context, rm *store.RerankModel, query string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, service.RerankStatus, error)
	// ResolveReranker implements the rerank resolution rule (explicit
	// override > collection binding > none). Nil treated as never-rerank.
	ResolveReranker func(ctx context.Context, req RerankResolveRequest) (*store.RerankModel, error)
}

// Server is the assembled REST API.
type Server struct {
	deps Deps
}

// New assembles the server.
func New(deps Deps) *Server {
	return &Server{deps: deps}
}

// Router builds the middleware chain + routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(recoveryMiddleware)
	r.Use(requestLogMiddleware)
	r.Use(s.authMiddleware)

	// Documents
	r.Post("/v1/documents/presign", s.handlePresign)
	r.Post("/v1/documents/{doc_id}/commit", s.handleCommit)
	r.Post("/v1/documents/fetch-url", s.handleFetchURL)
	r.Get("/v1/documents", s.handleListDocuments)
	r.Get("/v1/documents/{doc_id}", s.handleGetDocument)
	r.Get("/v1/documents/{doc_id}/shards", s.handleGetShards)
	r.Get("/v1/documents/{doc_id}/chunks", s.handleListDocChunks)
	r.Get("/v1/chunks/{chunk_id}", s.handleGetChunk)
	r.Post("/v1/documents/{doc_id}/retry", s.handleRetry)

	// Connectors (OPDS)
	r.Post("/v1/connectors/opds/browse", s.handleOpdsBrowse)
	r.Post("/v1/connectors/opds/sync", s.handleOpdsSync)

	// Collections
	r.Get("/v1/collections", s.handleListCollections)
	r.Post("/v1/collections", s.handleCreateCollection)
	r.Get("/v1/collections/{collection_id}/stats", s.handleCollectionStats)
	r.Post("/v1/collections/{collection_id}/model", s.handleBindCollectionModel)
	r.Post("/v1/collections/{collection_id}/reranker", s.handleBindCollectionReranker)

	// Model registry
	r.Get("/v1/models", s.handleListModels)
	r.Post("/v1/models", s.handleCreateModel)
	r.Post("/v1/models/test", s.handleTestModel) // dry-run probe, no persist
	r.Post("/v1/models/{model_id}", s.handleUpdateModel)
	r.Delete("/v1/models/{model_id}", s.handleDeleteModel)

	// Reranker registry
	r.Get("/v1/rerank-models", s.handleListRerankModels)
	r.Post("/v1/rerank-models", s.handleCreateRerankModel)
	r.Post("/v1/rerank-models/test", s.handleTestRerankModel) // dry-run probe
	r.Post("/v1/rerank-models/{model_id}", s.handleUpdateRerankModel)
	r.Delete("/v1/rerank-models/{model_id}", s.handleDeleteRerankModel)

	// Search
	r.Post("/v1/search", s.handleSearch)

	// Keys
	r.Post("/v1/keys", s.handleCreateKey)
	r.Get("/v1/keys", s.handleListKeys)
	r.Post("/v1/keys/{key_id}/revoke", s.handleRevokeKey)

	// Usage
	r.Get("/v1/usage/summary", s.handleUsageSummary)

	// Events
	r.Get("/v1/events", s.handleListEvents)
	r.Get("/v1/events/stream", s.handleEventStream)

	// System
	r.Get("/v1/system/health", s.handleHealth)
	r.Get("/v1/system/queues", s.handleQueues)
	r.Get("/v1/system/pipeline", s.handlePipeline)
	r.Get("/v1/system/metrics", s.handleMetrics)

	// OpenAPI snapshot: byte-identical contract keeps
	// `npm run generate-client` deterministic.
	r.Get("/openapi.json", s.handleOpenAPI)

	return r
}

// GracefulShutdownTimeout bounds the drain window.
const GracefulShutdownTimeout = 15 * time.Second
