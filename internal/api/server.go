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
	"github.com/saufi-opi/lybrix/internal/store"
)

// Deps carries the shared wiring every handler needs.
type Deps struct {
	Settings *config.Settings
	DB       *store.DB
	Redis    redis.Cmdable
	S3       *objectstore.Client
	// EmbedQuery produces a query-plane embedding (tei-query, NEVER
	// tei-ingest — an 8-token query queued behind a 65k-token ingest batch
	// destroys p99).
	EmbedQuery func(ctx context.Context, text string) ([]float32, error)
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
	r.Get("/v1/documents", s.handleListDocuments)
	r.Get("/v1/documents/{doc_id}", s.handleGetDocument)
	r.Get("/v1/documents/{doc_id}/shards", s.handleGetShards)
	r.Post("/v1/documents/{doc_id}/retry", s.handleRetry)

	// Collections
	r.Get("/v1/collections", s.handleListCollections)
	r.Post("/v1/collections", s.handleCreateCollection)
	r.Get("/v1/collections/{collection_id}/stats", s.handleCollectionStats)

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
