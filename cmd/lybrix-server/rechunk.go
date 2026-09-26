package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// runRechunk rebuilds chunk rows for documents from the parsed shard markdown
// already in S3, without re-splitting or re-parsing.
//
// This is the corpus-migration path for a chunker change (BACKLOG R-26/R-27):
// parse output is never deleted, so re-chunking reuses all OCR and conversion
// work — minutes per book instead of hours. Each doc has its chunks deleted and
// an embed job enqueued; the embedder does the rest.
//
// Usage:
//
//	lybrix-server rechunk [--doc <id>] [--limit N] [--dry-run]
//
// Without --doc every non-archived doc is selected. Only docs whose shards all
// settled can be re-chunked (the embedder needs parsed markdown to exist);
// others are reported and skipped rather than enqueued into a failure loop.
func runRechunk(settings *config.Settings, args []string) error {
	var (
		docID  string
		limit  = 500
		dryRun = false
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--doc":
			if i+1 >= len(args) {
				return fmt.Errorf("--doc requires a value")
			}
			docID = args[i+1]
			i++
		case "--limit":
			if i+1 >= len(args) {
				return fmt.Errorf("--limit requires a value")
			}
			if _, err := fmt.Sscanf(args[i+1], "%d", &limit); err != nil {
				return fmt.Errorf("--limit must be an integer: %w", err)
			}
			i++
		case "--dry-run":
			dryRun = true
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	db, rdb, _, err := infra(ctx, settings)
	if err != nil {
		return err
	}
	defer db.Close()

	docs, err := selectRechunkDocs(ctx, db, docID, limit)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		fmt.Println("no documents to rechunk")
		return nil
	}

	var queued, skipped int
	for _, doc := range docs {
		// A doc without parsed markdown cannot be re-chunked — the embedder
		// would fail with "no parsed shards to embed". Report it and move on
		// rather than enqueueing a guaranteed failure.
		shards, err := db.SettledShards(ctx, doc.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: cannot read shards: %v\n", shortDocID(doc.ID), err)
			skipped++
			continue
		}
		if len(shards) == 0 {
			fmt.Printf("  %s: skip (no settled shards — needs a full re-ingest)\n", shortDocID(doc.ID))
			skipped++
			continue
		}
		if dryRun {
			fmt.Printf("  %s: would rechunk %d shards\n", shortDocID(doc.ID), len(shards))
			queued++
			continue
		}
		if err := db.Tx(ctx, func(tx pgx.Tx) error {
			return db.ResetDocForRechunk(ctx, tx, doc.ID)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: reset failed: %v\n", shortDocID(doc.ID), err)
			skipped++
			continue
		}
		if _, err := queue.XAddJob(ctx, rdb, queue.StreamEmbed, queue.EmbedJob{
			SchemaVersion: queue.SchemaVersion, DocID: doc.ID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: enqueue failed: %v\n", shortDocID(doc.ID), err)
			skipped++
			continue
		}
		fmt.Printf("  %s: requeued (%d shards)\n", shortDocID(doc.ID), len(shards))
		queued++
	}

	verb := "requeued"
	if dryRun {
		verb = "would requeue"
	}
	fmt.Printf("rechunk: %s %d, skipped %d\n", verb, queued, skipped)
	return nil
}

// selectRechunkDocs returns the docs to re-chunk: one by id, else every
// non-archived doc that reached a terminal chunked state.
func selectRechunkDocs(ctx context.Context, db *store.DB, docID string, limit int) ([]*store.Document, error) {
	if docID != "" {
		doc, err := db.GetDocument(ctx, docID)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("document %s not found", docID)
		}
		return []*store.Document{doc}, nil
	}

	// ready/partial are the terminal chunked states; parsing/embedding docs are
	// already in flight and must not be reset underneath a running job.
	var out []*store.Document
	for _, st := range []store.DocState{store.StateReady, store.StatePartial} {
		state := st
		docs, err := db.ListDocuments(ctx, &state, nil, nil, limit, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	return out, nil
}

// shortDocID abbreviates a UUID for log lines.
func shortDocID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
