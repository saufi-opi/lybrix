package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/store"
)

// runRepairCounters recounts shards_done/shards_failed from the shards
// table (ground truth) for every doc whose documents-table counters
// disagree, and recomputes chunk_count/completeness/state when the doc
// already has real chunks. Fixes 1.0-era double-bumped counters (R-53) that
// can overflow documents.completeness NUMERIC(5,4) and abort the whole
// embed write tx with SQLSTATE 22003.
//
// This is a pure counter fix: it only opens the DB pool (no Redis/S3, unlike
// infra()), and it never enqueues an embed job — a doc that still needs
// chunks rebuilt is `rechunk`'s job, not this one's.
//
// Usage:
//
//	lybrix-server repair-counters [--doc <id>] [--limit N] [--dry-run]
func runRepairCounters(settings *config.Settings, args []string) error {
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

	db, err := store.NewPool(ctx, settings.DatabaseURL, store.TimeoutsFrom(settings))
	if err != nil {
		return err
	}
	defer db.Close()

	docIDs, err := selectDriftedDocs(ctx, db, docID, limit)
	if err != nil {
		return err
	}
	if len(docIDs) == 0 {
		fmt.Println("no drifted counters found")
		return nil
	}

	var repaired, failedCount int
	for _, id := range docIDs {
		if dryRun {
			done, failed, err := previewRecount(ctx, db, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: cannot recount: %v\n", shortDocID(id), err)
				failedCount++
				continue
			}
			fmt.Printf("  %s: would set shards_done=%d shards_failed=%d\n", shortDocID(id), done, failed)
			repaired++
			continue
		}
		var done, failed int
		if err := db.Tx(ctx, func(tx pgx.Tx) error {
			var repairErr error
			done, failed, repairErr = db.RepairDocCounters(ctx, tx, id)
			return repairErr
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: repair failed: %v\n", shortDocID(id), err)
			failedCount++
			continue
		}
		fmt.Printf("  %s: shards_done=%d shards_failed=%d\n", shortDocID(id), done, failed)
		repaired++
	}

	verb := "repaired"
	if dryRun {
		verb = "would repair"
	}
	fmt.Printf("repair-counters: %s %d, failed %d\n", verb, repaired, failedCount)
	return nil
}

// selectDriftedDocs returns the doc IDs to repair: one by id (no drift
// check — an explicit --doc is always processed), else every doc whose
// counters disagree with the shards table, up to limit.
func selectDriftedDocs(ctx context.Context, db *store.DB, docID string, limit int) ([]string, error) {
	if docID != "" {
		doc, err := db.GetDocument(ctx, docID)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("document %s not found", docID)
		}
		return []string{docID}, nil
	}
	return db.DriftedDocs(ctx, limit)
}

// previewRecount reports what RepairDocCounters would set, without writing
// — the --dry-run path.
func previewRecount(ctx context.Context, db *store.DB, docID string) (done, failed int, err error) {
	err = db.Pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE state = 'done'),
			count(*) FILTER (WHERE state = 'failed')
		FROM shards WHERE doc_id = $1`, docID).Scan(&done, &failed)
	return done, failed, err
}
