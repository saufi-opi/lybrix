package store

import (
	"context"
	"fmt"
	"testing"
)

// TestRepairDocCounters (R-53): the repair-counters CLI recounts
// shards_done/shards_failed from the shards table (ground truth) rather
// than trusting the documents-table counters, which 1.0-era double-bumping
// could poison. Covers both the common case (doc has no chunks yet — the
// repair must not fabricate a 'ready'/'partial' state) and the case where
// the doc already has real chunks (metadata should refresh).
func TestRepairDocCounters(t *testing.T) {
	db := mustDB(t, testDBImage)
	ctx := context.Background()
	docID := seedDoc(t, db)

	// 5 shards: 3 done, 1 failed, 1 still pending.
	if err := db.Tx(ctx, func(tx txType) error {
		_, err := db.InsertShards(ctx, tx, docID, [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 5}}, 0)
		return err
	}); err != nil {
		t.Fatalf("seed shards: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE shards SET state = 'done' WHERE doc_id = $1 AND idx IN (0,1,2)`, docID); err != nil {
		t.Fatalf("mark shards done: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE shards SET state = 'failed' WHERE doc_id = $1 AND idx = 3`, docID); err != nil {
		t.Fatalf("mark shard failed: %v", err)
	}

	// Poison the documents-table counters so they disagree with the shards
	// table truth (done=3, failed=1).
	if _, err := db.Pool.Exec(ctx,
		`UPDATE documents SET total_shards = 5, shards_done = 999, shards_failed = 777 WHERE id = $1`,
		docID); err != nil {
		t.Fatalf("poison counters: %v", err)
	}

	drifted, err := db.DriftedDocs(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(drifted, docID) {
		t.Fatalf("expected %s in DriftedDocs before repair, got %v", docID, drifted)
	}

	// No chunks exist yet — repair must fix the counters but leave
	// chunk_count/completeness/state untouched (no fabricated 'ready' doc).
	var done, failed int
	if err := db.Tx(ctx, func(tx txType) error {
		var repairErr error
		done, failed, repairErr = db.RepairDocCounters(ctx, tx, docID)
		return repairErr
	}); err != nil {
		t.Fatalf("RepairDocCounters: %v", err)
	}
	if done != 3 || failed != 1 {
		t.Fatalf("recounted (done,failed) = (%d,%d), want (3,1)", done, failed)
	}

	doc, err := db.GetDocument(ctx, docID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ShardsDone != 3 || doc.ShardsFailed != 1 {
		t.Fatalf("documents counters after repair: done=%d failed=%d, want done=3 failed=1",
			doc.ShardsDone, doc.ShardsFailed)
	}
	if doc.ChunkCount != nil || doc.Completeness != nil {
		t.Fatalf("no chunks exist yet: chunk_count/completeness must stay NULL, got %v/%v",
			doc.ChunkCount, doc.Completeness)
	}
	if doc.State == StateReady || doc.State == StatePartial {
		t.Fatalf("doc with zero chunks must not be fabricated into %q", doc.State)
	}

	drifted, err = db.DriftedDocs(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(drifted, docID) {
		t.Fatalf("expected %s absent from DriftedDocs after repair, got %v", docID, drifted)
	}

	// Now give the doc real chunks and re-poison the counters: repair
	// should this time also refresh chunk_count/completeness/state.
	children := []ChildChunk{
		{Seq: 0, ChunkHash: hashOf(fmt.Sprintf("repair-%s-0", docID)), Text: "a", TokenCount: 1},
		{Seq: 1, ChunkHash: hashOf(fmt.Sprintf("repair-%s-1", docID)), Text: "b", TokenCount: 1},
	}
	if err := db.Tx(ctx, func(tx txType) error {
		return db.InsertChunks(ctx, tx, docID, "books", children)
	}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE documents SET shards_done = 0, shards_failed = 0 WHERE id = $1`, docID); err != nil {
		t.Fatalf("re-poison counters: %v", err)
	}

	if err := db.Tx(ctx, func(tx txType) error {
		var repairErr error
		done, failed, repairErr = db.RepairDocCounters(ctx, tx, docID)
		return repairErr
	}); err != nil {
		t.Fatalf("RepairDocCounters (with chunks): %v", err)
	}
	if done != 3 || failed != 1 {
		t.Fatalf("recounted (done,failed) = (%d,%d), want (3,1)", done, failed)
	}

	doc, err = db.GetDocument(ctx, docID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ChunkCount == nil || *doc.ChunkCount != 2 {
		t.Fatalf("chunk_count after repair = %v, want 2", doc.ChunkCount)
	}
	if doc.Completeness == nil || *doc.Completeness < 0 || *doc.Completeness > 1 {
		t.Fatalf("completeness after repair out of range: %v", doc.Completeness)
	}
	if doc.State != StatePartial {
		t.Fatalf("state after repair = %q, want %q (shards_failed=1)", doc.State, StatePartial)
	}
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
