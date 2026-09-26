package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// insertParentChild is a small helper for the read-path tests.
func insertParentChild(t *testing.T, db *DB, doc string, parents []ParentChunk, children []ChildChunk) {
	t.Helper()
	ctx := context.Background()
	if err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertParents(ctx, tx, doc, "books", parents); err != nil {
			return err
		}
		return db.InsertChunks(ctx, tx, doc, "books", children)
	}); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
}

// uniqHash returns a distinct 64-char hex hash per input. The package's hashOf
// keys on length alone, which would collide across these fixtures.
func uniqHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestReadPageChunksReturnsParentsOnly is the regression test for BACKLOG R-27.
//
// Once children carry page ranges, a filter on page_start alone would return
// parents AND children — and a child's text is a slice of its parent's, so the
// read_pages "markdown" field would be heavily duplicated. Parents only.
func TestReadPageChunksReturnsParentsOnly(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)

	ps, pe := 1, 5
	cs, ce := 1, 5
	insertParentChild(t, db, doc,
		[]ParentChunk{{
			Seq: 0, ChunkHash: uniqHash("parent-a"), Text: "# Chapter\n\nParent body covering pages 1-5.",
			TokenCount: 8, PageStart: &ps, PageEnd: &pe,
		}},
		[]ChildChunk{{
			Seq: 1, ChunkHash: uniqHash("child-a"), Text: "Parent body covering pages 1-5.",
			TokenCount: 6, PageStart: &cs, PageEnd: &ce,
		}},
	)

	rows, err := db.ReadPageChunks(ctx, doc, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("read_pages must return the parent only, got %d rows", len(rows))
	}
	if !rows[0].IsParent {
		t.Fatalf("returned a child row: %+v", rows[0])
	}
}

// TestReadPageChunksOverlapAware pins the range test: a parent that starts before
// the requested range but covers part of it must be included. The previous
// page_start-only filter silently dropped those.
func TestReadPageChunksOverlapAware(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)

	// Parent covers pages 1-10; the caller asks for pages 8-12.
	ps, pe := 1, 10
	insertParentChild(t, db, doc,
		[]ParentChunk{{
			Seq: 0, ChunkHash: uniqHash("straddle"), Text: "Covers pages 1 through 10.",
			TokenCount: 5, PageStart: &ps, PageEnd: &pe,
		}},
		nil,
	)

	rows, err := db.ReadPageChunks(ctx, doc, 8, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("a parent overlapping the requested range must be returned, got %d rows", len(rows))
	}

	// A range wholly outside must not match.
	rows, err = db.ReadPageChunks(ctx, doc, 20, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a non-overlapping range must return nothing, got %d rows", len(rows))
	}
}

// TestChunkNeighboursStaysWithinKind pins that a neighbour window does not splice
// a parent into a child's context now that seq is globally unique (R-28).
func TestChunkNeighboursStaysWithinKind(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)

	insertParentChild(t, db, doc,
		[]ParentChunk{
			{Seq: 0, ChunkHash: uniqHash("p0"), Text: "parent zero", TokenCount: 2},
			{Seq: 1, ChunkHash: uniqHash("p1"), Text: "parent one", TokenCount: 2},
		},
		[]ChildChunk{
			{Seq: 2, ChunkHash: uniqHash("c0"), Text: "child zero", TokenCount: 2},
			{Seq: 3, ChunkHash: uniqHash("c1"), Text: "child one", TokenCount: 2},
		},
	)

	// Anchor on child seq=2 with a window wide enough to reach the parents.
	rows, err := db.ChunkNeighbours(ctx, doc, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected neighbour rows")
	}
	for _, r := range rows {
		if r.IsParent {
			t.Fatalf("neighbour window leaked a parent into a child context: seq=%d", r.Seq)
		}
	}

	// And anchoring on a parent must return parents only.
	rows, err = db.ChunkNeighbours(ctx, doc, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !r.IsParent {
			t.Fatalf("parent window leaked a child: seq=%d", r.Seq)
		}
	}
}
