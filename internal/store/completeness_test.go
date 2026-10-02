package store

import (
	"context"
	"testing"
)

// TestSetChunkCountAndCompletenessClamp (R-53 incident): 1.0-era
// double-bumped counters left some docs with shards_failed >> total_shards
// (e.g. total_shards=290, shards_failed=39224). The old unclamped formula
// computed a ratio around -134.6, overflowing documents.completeness
// NUMERIC(5,4) with SQLSTATE 22003 and aborting the whole embed write tx.
// completeness must now clamp to [0,1] no matter how corrupted the
// counters are.
func TestSetChunkCountAndCompletenessClamp(t *testing.T) {
	db := mustDB(t, testDBImage)
	ctx := context.Background()
	docID := seedDoc(t, db)

	if _, err := db.Pool.Exec(ctx,
		`UPDATE documents SET total_shards = 290, shards_failed = 39224 WHERE id = $1`,
		docID); err != nil {
		t.Fatalf("seed poisoned counters: %v", err)
	}

	if err := db.Tx(ctx, func(tx txType) error {
		return db.SetChunkCountAndCompleteness(ctx, tx, docID, 0)
	}); err != nil {
		t.Fatalf("clamp must prevent SQLSTATE 22003, got: %v", err)
	}

	doc, err := db.GetDocument(ctx, docID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Completeness == nil {
		t.Fatal("completeness is nil, expected a clamped value")
	}
	if *doc.Completeness < 0 || *doc.Completeness > 1 {
		t.Fatalf("completeness out of [0,1]: %v", *doc.Completeness)
	}
	if *doc.Completeness != 0 {
		t.Fatalf("shards_failed > total_shards should clamp to 0, got %v", *doc.Completeness)
	}
}

// TestSetChunkCountAndCompletenessZeroTotalShards guards the
// division-by-zero edge: total_shards = 0 must yield NULL, not an error.
func TestSetChunkCountAndCompletenessZeroTotalShards(t *testing.T) {
	db := mustDB(t, testDBImage)
	ctx := context.Background()
	docID := seedDoc(t, db)

	if _, err := db.Pool.Exec(ctx,
		`UPDATE documents SET total_shards = 0, shards_failed = 0 WHERE id = $1`,
		docID); err != nil {
		t.Fatalf("seed zero total_shards: %v", err)
	}

	if err := db.Tx(ctx, func(tx txType) error {
		return db.SetChunkCountAndCompleteness(ctx, tx, docID, 0)
	}); err != nil {
		t.Fatalf("total_shards=0 must not error, got: %v", err)
	}

	doc, err := db.GetDocument(ctx, docID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Completeness != nil {
		t.Fatalf("total_shards=0 should yield NULL completeness, got %v", *doc.Completeness)
	}
}
