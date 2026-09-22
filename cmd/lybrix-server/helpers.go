package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5"

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

// embedQueryFn builds the query-plane embedding fn: it resolves the
// caller's model (bound collection → legacy default), applies the model's
// asymmetric query prefix inside, and returns the model alongside the
// vector so callers can forward its dim into HybridSearch's dense CTE.
func embedQueryFn(db *store.DB) func(ctx context.Context, collection, text string) (vec []float32, model *store.EmbeddingModel, err error) {
	return func(ctx context.Context, collection, text string) ([]float32, *store.EmbeddingModel, error) {
		m, err := db.ResolveCollectionModel(ctx, collection)
		if err != nil {
			return nil, nil, err
		}
		if m == nil {
			return nil, nil, errors.NewPlatformError(errors.CodeEmbedDimMismatch, "no embedding model configured")
		}
		client, err := pipeline.NewEmbedClient(pipeline.EmbedSpec{
			Provider:      m.Provider,
			ModelID:       m.ModelID,
			IngestURL:     m.IngestURL,
			QueryURL:      m.QueryURL,
			TruncateChars: m.TruncateChars,
		}, "query")
		if err != nil {
			return nil, nil, err
		}
		vecs, err := client.Embed(ctx, []string{m.QueryPrefix + text}, m.VectorDim)
		if err != nil {
			return nil, nil, err
		}
		if len(vecs) == 0 {
			return nil, nil, fmt.Errorf("empty embedding response")
		}
		return vecs[0], m, nil
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
