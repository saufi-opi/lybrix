package store

import (
	"context"
	"fmt"
	"testing"
)

// TestCopyEmbeddingsLargeDocBatches (R-53): a single UPDATE over a doc's
// entire chunk set could exceed the statement_timeout on big books.
// CopyEmbeddings now splits the UPDATE into embedUpdateBatchSize-sized
// batches inside the same transaction — this seeds more rows than one
// batch holds (forcing multiple UPDATE statements) and asserts every row
// still lands correctly.
func TestCopyEmbeddingsLargeDocBatches(t *testing.T) {
	db := mustDB(t, testDBImage)
	ctx := context.Background()
	docID := seedDoc(t, db)

	const nChunks = embedUpdateBatchSize*2 + 200 // forces 3 UPDATE batches
	children := make([]ChildChunk, nChunks)
	for i := range children {
		children[i] = ChildChunk{
			Seq:        i,
			ChunkHash:  hashOf(fmt.Sprintf("copyembed-batch-%s-%d", docID, i)),
			Text:       fmt.Sprintf("copy embeddings batch chunk %d", i),
			TokenCount: 6,
		}
	}
	if err := db.Tx(ctx, func(tx txType) error {
		return db.InsertChunks(ctx, tx, docID, "books", children)
	}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}

	rows := make([]EmbeddingRow, nChunks)
	for i := range rows {
		vec := make([]float32, 1024)
		for j := range vec {
			vec[j] = float32(i%13) + float32(j%7)/8.0
		}
		rows[i] = EmbeddingRow{
			ChunkID: DeterministicChunkID(docID, children[i].ChunkHash),
			Vector:  vec,
		}
	}

	if err := db.Tx(ctx, func(tx txType) error {
		return db.CopyEmbeddings(ctx, tx, rows)
	}); err != nil {
		t.Fatalf("CopyEmbeddings: %v", err)
	}

	var embedded int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM chunks WHERE doc_id = $1 AND embedded_at IS NOT NULL
			AND vector_dims(embedding) = 1024`, docID).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != nChunks {
		t.Fatalf("expected %d embedded chunks across batches, got %d", nChunks, embedded)
	}

	// spot-check correctness on rows that straddle batch boundaries.
	for _, idx := range []int{0, embedUpdateBatchSize - 1, embedUpdateBatchSize,
		embedUpdateBatchSize * 2, nChunks - 1} {
		r := rows[idx]
		var got []float32
		if err := db.Pool.QueryRow(ctx,
			`SELECT embedding::real[] FROM chunks WHERE id = $1`, r.ChunkID).Scan(&got); err != nil {
			t.Fatalf("read back chunk %s (idx %d): %v", r.ChunkID, idx, err)
		}
		if len(got) != len(r.Vector) {
			t.Fatalf("dim drift on chunk %s (idx %d): got %d, want %d",
				r.ChunkID, idx, len(got), len(r.Vector))
		}
		for j := range got {
			if got[j] != r.Vector[j] {
				t.Fatalf("vector drift on chunk %s (idx %d) at [%d]: got %v, want %v",
					r.ChunkID, idx, j, got[j], r.Vector[j])
			}
		}
	}
}
