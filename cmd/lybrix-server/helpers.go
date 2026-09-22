package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/config"
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

// embedQueryFn builds the query-plane embedding fn (tei-query, with the
// asymmetric prefix applied by the caller).
func embedQueryFn(settings *config.Settings) func(ctx context.Context, text string) ([]float32, error) {
	return func(ctx context.Context, text string) ([]float32, error) {
		tei, err := pipeline.NewTeiClient(settings.TEIQueryURL, settings.EmbedBackend, settings.EmbedModel, settings.EmbedTruncateChars)
		if err != nil {
			return nil, err
		}
		vecs, err := tei.Embed(ctx, []string{text})
		if err != nil {
			return nil, err
		}
		if len(vecs) == 0 {
			return nil, fmt.Errorf("empty embedding response")
		}
		return vecs[0], nil
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
