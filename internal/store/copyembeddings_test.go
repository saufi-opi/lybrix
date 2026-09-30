package store

import (
	"context"
	"fmt"
	"testing"
)

// TestCopyEmbeddingsRoundTrip (BACKLOG R-37): CopyEmbeddings must land
// every vector bit-identical at its declared dimension.
//
// The pre-fix path COPYed pgvector.NewVector values in BINARY COPY into an
// untyped `vector` temp column. The root pgvector-go module implements only
// driver.Valuer (its Value() is the TEXT form "[…]"); with no pgx codec
// registered pgx falls back to encoding the Valuer's string bytes verbatim,
// and Postgres reads them as vector binary — "[0." = 0x5B30 = 23344 dims →
// SQLSTATE 54000 "vector cannot have more than 16000 dimensions" on row 1.
//
// The test seeds real chunks through the store layer, writes 1024-dim
// vectors through CopyEmbeddings inside a real tx, and reads them back via
// SQL: the count must match and every stored element must equal the Go-side
// float32 bit-for-bit (compared as real[], immune to PG text formatting).
func TestCopyEmbeddingsRoundTrip(t *testing.T) {
	db := mustDB(t, testDBImage)
	ctx := context.Background()
	docID := seedDoc(t, db)

	const nChunks = 250
	children := make([]ChildChunk, nChunks)
	for i := range children {
		children[i] = ChildChunk{
			Seq:        i,
			ChunkHash:  hashOf(fmt.Sprintf("copyembed-%s-%d", docID, i)),
			Text:       fmt.Sprintf("copy embeddings round trip chunk %d", i),
			TokenCount: 6,
		}
	}
	if err := db.Tx(ctx, func(tx txType) error {
		return db.InsertChunks(ctx, tx, docID, "books", children)
	}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}

	// Deterministic 1024-dim vectors with edge values mixed in: 0.0,
	// negatives, ±large magnitude, and denormal-ish magnitudes — the values
	// most likely to expose an encoding/decoding drift.
	rows := make([]EmbeddingRow, nChunks)
	for i := range rows {
		vec := make([]float32, 1024)
		for j := range vec {
			switch (i*31 + j) % 8 {
			case 0:
				vec[j] = 0.0
			case 1:
				vec[j] = -1.5e-8 // denormal-ish
			case 2:
				vec[j] = -3.25e6 // large negative
			case 3:
				vec[j] = 2.75e6 // large positive
			case 4:
				vec[j] = -0.3333333
			case 5:
				vec[j] = 1e-30
			case 6:
				vec[j] = -1e-30
			default:
				vec[j] = float32(i%13) + float32(j%7)/8.0
			}
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

	// every chunk embedded, all at 1024 dims
	var embedded int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM chunks WHERE doc_id = $1 AND vector_dims(embedding) = 1024`,
		docID).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != nChunks {
		t.Fatalf("expected %d embedded chunks at 1024 dims, got %d", nChunks, embedded)
	}

	// per-row value equality: compare as PG compares, not as text formats.
	// Cast the stored vector back to float4[] and compare element-wise with
	// the Go-side float32 values — exact bit equality of every element,
	// immune to PG's shortest-round-trip text formatting (e-notation etc.).
	for _, r := range rows {
		var got []float32
		if err := db.Pool.QueryRow(ctx,
			`SELECT embedding::real[] FROM chunks WHERE id = $1`, r.ChunkID).Scan(&got); err != nil {
			t.Fatalf("read back chunk %s: %v", r.ChunkID, err)
		}
		if len(got) != len(r.Vector) {
			t.Fatalf("dim drift on chunk %s: got %d, want %d", r.ChunkID, len(got), len(r.Vector))
		}
		for j := range got {
			if got[j] != r.Vector[j] {
				t.Fatalf("vector drift on chunk %s at [%d]: got %v, want %v",
					r.ChunkID, j, got[j], r.Vector[j])
			}
		}
	}
}
