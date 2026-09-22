package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
	"github.com/saufi-opi/lybrix/internal/worker"
)

// redisClient aliases the go-redis client for the infra helper.
type redisClient = queue.Cmdable2

// txT aliases the pgx tx type for the handler shims.
type txT = pgx.Tx

// embedForModelFn produces a query-plane embedding with one specific
// registered model — the building block both search paths share. It applies
// the model's asymmetric query prefix inside.
func embedForModelFn(ctx context.Context, m *store.EmbeddingModel, text string) ([]float32, error) {
	client, err := pipeline.NewEmbedClient(pipeline.EmbedSpec{
		Provider:      m.Provider,
		ModelID:       m.ModelID,
		IngestURL:     m.IngestURL,
		QueryURL:      m.QueryURL,
		TruncateChars: m.TruncateChars,
	}, "query")
	if err != nil {
		return nil, err
	}
	vecs, err := client.Embed(ctx, []string{m.QueryPrefix + text}, m.VectorDim)
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return vecs[0], nil
}

// embedQueryFn builds the single-collection query-plane embedding fn: it
// resolves the collection's bound model (the default fallback is retired —
// 2.0.2), applies the model's asymmetric query prefix inside, and returns
// the model alongside the vector so callers can forward its dim into
// HybridSearch's dense CTE.
func embedQueryFn(db *store.DB) func(ctx context.Context, collection, text string) (vec []float32, model *store.EmbeddingModel, err error) {
	return func(ctx context.Context, collection, text string) ([]float32, *store.EmbeddingModel, error) {
		m, err := db.ResolveCollectionModel(ctx, collection)
		if err != nil {
			return nil, nil, err
		}
		if m == nil {
			return nil, nil, errors.NewPlatformError(errors.CodeEmbedDimMismatch, "no embedding model configured")
		}
		vec, err := embedForModelFn(ctx, m, text)
		if err != nil {
			return nil, nil, err
		}
		return vec, m, nil
	}
}

// embedForModelAdapter projects embedForModelFn onto the Deps shape both
// api and mcp expect for the multi-collection grouped search.
func embedForModelAdapter() func(ctx context.Context, m *store.EmbeddingModel, text string) ([]float32, error) {
	return embedForModelFn
}

// rerankSeedFrom projects the config rerank seed block onto the store seed
// struct (RERANK_ENABLED=true gates the whole thing).
func rerankSeedFrom(settings *config.Settings) store.RerankSeed {
	return store.RerankSeed{
		Enabled:  settings.RerankSeed.Enabled,
		ModelID:  settings.RerankSeed.ModelID,
		QueryURL: settings.RerankSeed.QueryURL,
	}
}

// rerankResolver is the shared rerank resolution rule (plan §1e) injected
// into both the REST and MCP Deps.
func rerankResolver(db *store.DB) func(ctx context.Context, req service.RerankResolveRequest) (*store.RerankModel, error) {
	return func(ctx context.Context, req service.RerankResolveRequest) (*store.RerankModel, error) {
		return service.ResolveRerankModel(ctx, db, req)
	}
}

// rerankHitsFn builds the cross-encoder stage: fetch the resolved row
// fresh (so an edit/delete converges), build the client with the
// write-only key, and run service.RerankHits. The key never leaves this
// closure — no log, event, or response carries it.
func rerankHitsFn(db *store.DB) func(ctx context.Context, rm *store.RerankModel, query string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, service.RerankStatus, error) {
	return func(ctx context.Context, rm *store.RerankModel, query string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, service.RerankStatus, error) {
		// re-fetch with the key: the resolved row carries has_api_key only
		m, key, err := db.GetRerankModelWithKey(ctx, rm.ID)
		if err != nil {
			return hits, service.RerankStatus{Applied: false, Reason: err.Error()}, nil
		}
		if m == nil {
			return hits, service.RerankStatus{Applied: false, Reason: "rerank model vanished"}, nil
		}
		client, err := pipeline.NewRerankClient(m.Provider, m.ModelID, m.QueryURL, key, m.TruncateChars)
		if err != nil {
			return hits, service.RerankStatus{Applied: false, Reason: err.Error()}, nil
		}
		return service.RerankHits(ctx, client, query, hits, topK)
	}
}

// wrapHandler adapts service handlers onto the worker.Handler shape.
func wrapHandler(h func(context.Context, service.Deps, txT, map[string]any) error, deps service.Deps) worker.Handler {
	return func(ctx context.Context, tx txT, job map[string]any) error {
		return h(ctx, deps, tx, job)
	}
}

// wrapOnError records job failures with the taxonomy code.
func wrapOnError(stage string) worker.OnError {
	return func(ctx context.Context, tx txT, job map[string]any, jobErr error) {
		worker.RecordJobError(ctx, tx, job, jobErr, stage, stage)
	}
}

// bootstrapKey creates the first admin key (scopes: search, ingest, admin).
func bootstrapKey(ctx context.Context, db *store.DB, name string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := "ragk_" + base64.RawURLEncoding.EncodeToString(b)
	_, err := db.CreateKey(ctx, name, store.HashKey(raw),
		[]string{"search", "ingest", "admin"}, nil, nil)
	return raw, err
}
