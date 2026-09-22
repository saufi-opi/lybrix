package store

import (
	"context"
	"fmt"
	"testing"
)

// multiSearchDB sets up two collections bound to different-dimension models
// (A: 768, B: 1024), one doc + one embedded child chunk each. Returns the
// db, both docs' ids, and the per-dim unit vectors used at embed time.
func multiSearchDB(t *testing.T) (*DB, string, string, []float32, []float32) {
	t.Helper()
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m768, err := db.InsertEmbeddingModel(ctx, modelFixture("g768", "tei", "seven", 768), nil)
	if err != nil {
		t.Fatal(err)
	}
	m1024, err := db.InsertEmbeddingModel(ctx, modelFixture("g1024", "tei", "ten", 1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "col768", "c7", m768); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "col1024", "c10", m1024); err != nil {
		t.Fatal(err)
	}
	vec768 := make([]float32, 768)
	vec1024 := make([]float32, 1024)
	for i := range vec768 {
		vec768[i] = 0.001
	}
	for i := range vec1024 {
		vec1024[i] = 0.002
	}
	d768 := seedDocWithDim(t, db, "col768")
	d1024 := seedDocWithDim(t, db, "col1024")
	if err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertChunks(ctx, tx, d768, "col768", []ChildChunk{{
			Seq: 0, ChunkHash: hashOf(d768 + "a"), Text: "seven", TokenCount: 1}}); err != nil {
			return err
		}
		if err := db.InsertChunks(ctx, tx, d1024, "col1024", []ChildChunk{{
			Seq: 0, ChunkHash: hashOf(d1024 + "a"), Text: "ten", TokenCount: 1}}); err != nil {
			return err
		}
		if err := db.UpdateEmbeddingTx(ctx, tx, DeterministicChunkID(d768, hashOf(d768+"a")), vec768); err != nil {
			return err
		}
		return db.UpdateEmbeddingTx(ctx, tx, DeterministicChunkID(d1024, hashOf(d1024+"a")), vec1024)
	}); err != nil {
		t.Fatal(err)
	}
	return db, d768, d1024, vec768, vec1024
}

// embedCounter wraps the per-model embed fn and counts unique-model calls —
// the query must be embedded ONCE per unique model, never per collection.
func embedCounter(vec768, vec1024 []float32) (func(m *EmbeddingModel) ([]float32, error), *int) {
	calls := new(int)
	return func(m *EmbeddingModel) ([]float32, error) {
		*calls++
		switch m.VectorDim {
		case 768:
			return vec768, nil
		case 1024:
			return vec1024, nil
		}
		return nil, fmt.Errorf("unexpected model dim %d", m.VectorDim)
	}, calls
}

// Cross-model search: collections with differing dimensions (768 + 1024)
// group correctly, each dense leg queries its own partial HNSW, and the RRF
// fusion returns both docs in one unified list.
func TestMultiHybridSearchCrossDims(t *testing.T) {
	db, d768, d1024, vec768, vec1024 := multiSearchDB(t)
	ctx := context.Background()
	embedFn, calls := embedCounter(vec768, vec1024)

	// explicit multi-collection target set
	hits, err := db.MultiHybridSearch(ctx, "seven", []string{"col768", "col1024"}, nil, 8, embedFn)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("cross-dim search must return both docs, got %d: %+v", len(hits), hits)
	}
	if *calls != 2 {
		t.Fatalf("query must embed once per unique model (2), got %d", *calls)
	}
	byDoc := map[string]float64{}
	for _, h := range hits {
		byDoc[h.DocID] = h.Score
	}
	if _, ok := byDoc[d768]; !ok {
		t.Fatal("768 doc missing from fused results")
	}
	if _, ok := byDoc[d1024]; !ok {
		t.Fatal("1024 doc missing from fused results")
	}
	// the query string "seven" BM25-matches only the 768 doc, and its dense
	// leg matches too (both legs) — it must outscore the 1024 doc (dense leg
	// only, and an orthogonal-ish vector).
	if byDoc[d768] <= byDoc[d1024] {
		t.Fatalf("dual-leg hit must outrank single-leg: %v < %v", byDoc[d768], byDoc[d1024])
	}
}

// Search without a collection parameter discovers ALL collections and
// executes across their differing dimensions.
func TestMultiHybridSearchAllCollections(t *testing.T) {
	db, d768, d1024, vec768, vec1024 := multiSearchDB(t)
	ctx := context.Background()
	embedFn, calls := embedCounter(vec768, vec1024)

	hits, err := db.MultiHybridSearch(ctx, "ten", nil, nil, 8, embedFn)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("all-collections search must return both docs, got %d", len(hits))
	}
	if *calls != 2 {
		t.Fatalf("all-collections search embeds once per unique model, got %d", *calls)
	}
	if hits[0].DocID != d1024 {
		// "ten" matches both legs of the 1024 doc
		t.Fatalf("1024 doc must win its own query: %+v", hits)
	}
	_ = d768
}

// A scoped key (scope filter) restricts both legs: searching all
// collections with scope ["col768"] must not return the 1024 doc.
func TestMultiHybridSearchScopeFilter(t *testing.T) {
	db, d768, _, vec768, vec1024 := multiSearchDB(t)
	ctx := context.Background()
	embedFn, _ := embedCounter(vec768, vec1024)

	hits, err := db.MultiHybridSearch(ctx, "seven ten", nil, []string{"col768"}, 8, embedFn)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != d768 {
		t.Fatalf("scope filter must pin results to col768: %+v", hits)
	}
}

// An embedder failure for one model propagates — a grouped search never
// silently degrades to BM25-only.
func TestMultiHybridSearchEmbedErrorPropagates(t *testing.T) {
	db, _, _, _, _ := multiSearchDB(t)
	ctx := context.Background()
	_, err := db.MultiHybridSearch(ctx, "x", []string{"col768", "col1024"}, nil, 8,
		func(m *EmbeddingModel) ([]float32, error) {
			return nil, fmt.Errorf("embed backend down")
		})
	if err == nil || err.Error() != "embed backend down" {
		t.Fatalf("embed failure must propagate, got %v", err)
	}
}

// RRF score arithmetic: a hit appearing in BOTH legs (rank 1 dense, rank 1
// bm25) scores 1/(60+1) + 0.3/(60+1); a single-leg rank-1 hit scores
// 1/(60+1). The dual-leg hit must be exactly the sum.
func TestMultiHybridSearchRRFArithmetic(t *testing.T) {
	db, _, _, vec768, vec1024 := multiSearchDB(t)
	ctx := context.Background()
	embedFn, _ := embedCounter(vec768, vec1024)

	// "seven" is BM25 rank-1 AND dense rank-1 in the 768 group; "ten" is
	// dense rank-1 in the 1024 group only (BM25 leg finds no "seven" match).
	hits, err := db.MultiHybridSearch(ctx, "seven", []string{"col768", "col1024"}, nil, 8, embedFn)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected both docs, got %+v", hits)
	}
	var dual, single float64
	for _, h := range hits {
		if h.Text == "seven" {
			dual = h.Score
		} else {
			single = h.Score
		}
	}
	wantDual := 1.0/(60.0+1.0) + 0.3/(60.0+1.0)
	if diff := dual - wantDual; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("dual-leg RRF drift: got %f want %f", dual, wantDual)
	}
	wantSingle := 1.0 / (60.0 + 1.0)
	if diff := single - wantSingle; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("single-leg RRF drift: got %f want %f", single, wantSingle)
	}
}
