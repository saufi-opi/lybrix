// cmd/lybrix-server — the unified 2.0 binary.
//
// Subcommands:
//
//	serve       REST API (:8000) + MCP (:8430) + janitor goroutine (core profile)
//	splitter    doc.split consumer (ingest profile)
//	parser      doc.parse consumer (ingest profile; PARSER_RECYCLE_AFTER)
//	embedder    doc.embed consumer (ingest profile)
//	janitor     standalone janitor loop (compose parity)
//	keys        key management (`keys bootstrap` creates the first admin key)
//	migrate     apply deploy/schema.sql once, exit
//
// The anydoc fast path is compile-time: `go build -tags anydoc` links the
// CGO binding when the static lib is present; the default build compiles a
// stub so CI never needs the lib.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saufi-opi/lybrix/internal/api"
	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/logging"
	"github.com/saufi-opi/lybrix/internal/mcp"
	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
	"github.com/saufi-opi/lybrix/internal/worker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	// subcommand-agnostic env config; logging first
	settings, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	logging.Configure(settings.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var runErr error
	switch os.Args[1] {
	case "serve":
		runErr = runServe(ctx, settings)
	case "splitter":
		runErr = runWorker(ctx, settings, queue.StreamSplit, "splitter", 0)
	case "parser":
		runErr = runWorker(ctx, settings, queue.StreamParse, "parser", settings.ParserRecycleAfter)
	case "embedder":
		runErr = runWorker(ctx, settings, queue.StreamEmbed, "embedder", 0)
	case "janitor":
		runErr = runJanitor(ctx, settings)
	case "migrate":
		runErr = runMigrate(settings)
	case "keys":
		runErr = runKeys(settings, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if runErr != nil && runErr != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: lybrix-server <command>

commands:
  serve        REST API (:8000) + MCP (:8430) + janitor goroutine
  splitter     doc.split consumer
  parser       doc.parse consumer (PARSER_RECYCLE_AFTER)
  embedder     doc.embed consumer
  janitor      standalone janitor loop
  keys         key management (keys bootstrap [name])
  migrate      apply schema.sql, exit
`)
}

// infra opens the DB pool + Redis client + S3 client, and seeds the model
// registry from the legacy EMBED_* env vars exactly once (empty-table
// seed, idempotent) — every subcommand rides this so a `keys bootstrap`
// run on a fresh database leaves the default row + seed-dim HNSW behind.
// The rerank registry seeds the same way, gated on RERANK_ENABLED.
func infra(ctx context.Context, settings *config.Settings) (*store.DB, redisClient, *objectstore.Client, error) {
	db, err := store.NewPool(ctx, settings.DatabaseURL)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := db.SeedDefaultEmbeddingModel(ctx, seedFrom(settings)); err != nil {
		db.Close()
		return nil, nil, nil, fmt.Errorf("seed embedding model: %w", err)
	}
	if err := db.SeedDefaultRerankModel(ctx, rerankSeedFrom(settings)); err != nil {
		db.Close()
		return nil, nil, nil, fmt.Errorf("seed rerank model: %w", err)
	}
	r := queue.MustRedis(settings.RedisURL)
	s3c, err := objectstore.New(ctx, settings.S3Endpoint, settings.S3AccessKey, settings.S3SecretKey)
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	return db, r, s3c, nil
}

func runServe(ctx context.Context, settings *config.Settings) error {
	db, r, s3c, err := infra(ctx, settings)
	if err != nil {
		return err
	}
	defer db.Close()

	apiDeps := api.Deps{
		Settings: settings, DB: db, Redis: r, S3: s3c,
		EmbedQuery:      embedQueryFn(db),
		HybridSearch:    db.HybridSearch,
		EmbedForModel:   embedForModelAdapter(),
		MultiSearch:     db.MultiHybridSearch,
		ResolveReranker: rerankResolver(db),
		RerankHits:      rerankHitsFn(db),
	}
	apiServer := api.New(apiDeps)

	mcpDeps := mcp.Deps{
		Settings: settings, DB: db,
		EmbedQuery:      embedQueryFn(db),
		EmbedForModel:   embedForModelAdapter(),
		ResolveReranker: rerankResolver(db),
		RerankHits:      rerankHitsFn(db),
	}

	// janitor goroutine rides along in serve mode (2.0 consolidation)
	jan := &worker.Janitor{
		DB: db, Redis: r,
		Settings: worker.Settings{
			JanitorInterval:  settings.JanitorInterval,
			StuckMinutes:     settings.StuckMinutes,
			MaxShardAttempts: settings.MaxShardAttempts,
		},
	}
	go func() {
		_ = jan.Run(ctx)
	}()

	// Two listeners: REST :8000 and MCP :8430 (never collapse them — the
	// query plane hits MCP, the browser hits the API).
	apiSrv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", settings.APIHost, settings.APIPort),
		Handler:           apiServer.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	mcpSrv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", settings.MCPHost, settings.MCPPort),
		Handler:           mcp.HTTPHandler(mcpDeps), // /mcp + /health
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() { errCh <- apiSrv.ListenAndServe() }()
	go func() { errCh <- mcpSrv.ListenAndServe() }()
	fmt.Printf("lybrix-server serve: api on :%d, mcp on :%d\n", settings.APIPort, settings.MCPPort)

	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), api.GracefulShutdownTimeout)
		defer cancel()
		_ = apiSrv.Shutdown(shCtx)
		return mcpSrv.Shutdown(shCtx)
	case err := <-errCh:
		return err
	}
}

func runWorker(ctx context.Context, settings *config.Settings, stream, name string, recycleAfter int) error {
	db, r, s3c, err := infra(ctx, settings)
	if err != nil {
		return err
	}
	defer db.Close()
	deps := service.Deps{
		Settings: settings, DB: db, Redis: r, S3: s3c,
		Parser: pipeline.NewTwoTierParser(settings.DoclingURL,
			settings.MinYieldCharsPerPage, settings.OCRMinCharsPerPage, settings.AnyDocEnabled),
	}
	var handler func(context.Context, service.Deps, txT, map[string]any) error
	switch name {
	case "splitter":
		handler = service.HandleSplit
	case "parser":
		handler = service.HandleParse
	case "embedder":
		handler = service.HandleEmbed
	}
	consumer := queue.NewConsumerName(name)
	return worker.RunConsumer(ctx, db, r, stream, consumer,
		wrapHandler(handler, deps), wrapOnError(name), settings.WorkerPrefetch, 5000, recycleAfter)
}

func runJanitor(ctx context.Context, settings *config.Settings) error {
	db, r, _, err := infra(ctx, settings)
	if err != nil {
		return err
	}
	defer db.Close()
	jan := &worker.Janitor{
		DB: db, Redis: r,
		Settings: worker.Settings{
			JanitorInterval:  settings.JanitorInterval,
			StuckMinutes:     settings.StuckMinutes,
			MaxShardAttempts: settings.MaxShardAttempts,
		},
	}
	return jan.Run(ctx)
}

func runMigrate(settings *config.Settings) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := store.NewPool(ctx, settings.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.SeedDefaultEmbeddingModel(ctx, seedFrom(settings)); err != nil {
		return fmt.Errorf("seed embedding model: %w", err)
	}
	fmt.Println("schema applied")
	return nil
}

func runKeys(settings *config.Settings, args []string) error {
	if len(args) < 1 || args[0] != "bootstrap" {
		return fmt.Errorf("usage: lybrix-server keys bootstrap [name]")
	}
	name := "admin"
	if len(args) > 1 {
		name = args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.NewPool(ctx, settings.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.SeedDefaultEmbeddingModel(ctx, seedFrom(settings)); err != nil {
		return fmt.Errorf("seed embedding model: %w", err)
	}
	raw, err := bootstrapKey(ctx, db, name)
	if err != nil {
		return err
	}
	fmt.Printf("admin key created (scopes: search, ingest, admin) — shown ONCE:\n%s\n", raw)
	return nil
}

// seedFrom projects the config seed block onto the store seed struct.
func seedFrom(settings *config.Settings) store.EmbeddingSeed {
	return store.EmbeddingSeed{
		Provider:  settings.EmbedSeed.Provider,
		ModelID:   settings.EmbedSeed.ModelID,
		Dim:       settings.EmbedSeed.Dim,
		IngestURL: settings.EmbedSeed.IngestURL,
		QueryURL:  settings.EmbedSeed.QueryURL,
	}
}
