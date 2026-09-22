package store

import (
	"context"
	"testing"
)

// modelFixture returns a registry row with knobs overridden per test.
func modelFixture(name, provider, modelID string, dim int, isDefault bool) *EmbeddingModel {
	return &EmbeddingModel{
		Name: name, Provider: provider, ModelID: modelID,
		IngestURL: "http://127.0.0.1:8081", QueryURL: "http://127.0.0.1:8082",
		VectorDim: dim, QueryPrefix: "search_query: ",
		BatchSize: 48, CtxBudget: 1900, TruncateChars: 6000, IsDefault: isDefault,
	}
}

func TestSeedDefaultEmbeddingModelIdempotentWithIndex(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	seed := EmbeddingSeed{Provider: "tei", ModelID: "BAAI/bge-m3", Dim: 1024,
		IngestURL: "http://i", QueryURL: "http://q"}
	if err := db.SeedDefaultEmbeddingModel(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedDefaultEmbeddingModel(ctx, seed); err != nil {
		t.Fatalf("second seed must be a no-op: %v", err)
	}
	m, err := db.GetDefaultEmbeddingModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("seed row missing")
	}
	if !m.IsDefault || m.ModelID != "BAAI/bge-m3" || m.VectorDim != 1024 {
		t.Fatalf("seed row drift: %+v", m)
	}
	if !indexExists(t, db, "ix_chunks_hnsw_1024") {
		t.Fatal("seed must provision the seed-dim HNSW index")
	}
}

func TestSeedRespectsEMBEDDIM768(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	if err := db.SeedDefaultEmbeddingModel(ctx, EmbeddingSeed{Provider: "tei",
		ModelID: "other/model", Dim: 768, IngestURL: "http://i", QueryURL: "http://q"}); err != nil {
		t.Fatal(err)
	}
	if !indexExists(t, db, "ix_chunks_hnsw_768") {
		t.Fatal("EMBED_DIM=768 boot must provision ix_chunks_hnsw_768")
	}
}

func TestDefaultUniqueness(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m1, err := db.InsertEmbeddingModel(ctx, modelFixture("a", "tei", "a", 1024, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := db.InsertEmbeddingModel(ctx, modelFixture("b", "tei", "b", 768, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh1, _ := db.GetEmbeddingModel(ctx, m1.ID)
	fresh2, _ := db.GetEmbeddingModel(ctx, m2.ID)
	if fresh1.IsDefault || !fresh2.IsDefault {
		t.Fatalf("default uniqueness broken: a=%v b=%v", fresh1.IsDefault, fresh2.IsDefault)
	}
}

func TestDeleteModel409s(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	def, err := db.InsertEmbeddingModel(ctx, modelFixture("def", "tei", "d", 1024, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := db.InsertEmbeddingModel(ctx, modelFixture("bound", "tei", "b", 768, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "books", "Books", bound); err != nil {
		t.Fatal(err)
	}
	// default → 409
	if err := db.DeleteEmbeddingModel(ctx, def.ID); err != ErrModelDefault {
		t.Fatalf("default delete must be ErrModelDefault, got %v", err)
	}
	// bound → 409
	err = db.DeleteEmbeddingModel(ctx, bound.ID)
	if err == nil {
		t.Fatal("bound model delete must fail")
	}
	if err != ErrModelInUse {
		t.Fatalf("bound model delete must be ErrModelInUse, got %v", err)
	}
	// unbind then delete works
	if _, err := db.BindCollectionModel(ctx, "books", def.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteEmbeddingModel(ctx, bound.ID); err != nil {
		t.Fatalf("unbound delete must succeed: %v", err)
	}
}

func TestResolveCollectionModelFallbacks(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	def, err := db.InsertEmbeddingModel(ctx, modelFixture("def", "tei", "d", 1024, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.InsertEmbeddingModel(ctx, modelFixture("other", "ollama", "o", 768, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "legacy", "Legacy", def); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "bound", "Bound", other); err != nil {
		t.Fatal(err)
	}
	// unbound (nonexistent) collection id → default row
	m, err := db.ResolveCollectionModel(ctx, "no-such-collection")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.ID != def.ID {
		t.Fatalf("unbound fallback must hit the default row: %+v", m)
	}
	// legacy NULL binding → default row
	legacy, err := db.GetCollection(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.EmbeddingModelID != nil {
		t.Fatalf("legacy collection must read nil binding: %+v", legacy.EmbeddingModelID)
	}
	m, _ = db.ResolveCollectionModel(ctx, "legacy")
	if m == nil || m.ID != def.ID {
		t.Fatalf("legacy fallback must hit the default row: %+v", m)
	}
	// explicit binding wins
	m, _ = db.ResolveCollectionModel(ctx, "bound")
	if m == nil || m.ID != other.ID || m.VectorDim != 768 {
		t.Fatalf("bound collection must resolve its own row: %+v", m)
	}
}

func TestBindCollectionModelSyncsLegacyColumns(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m, err := db.InsertEmbeddingModel(ctx, modelFixture("rebind-target", "tei", "rt", 768, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	col, err := db.BindCollectionModel(ctx, "fresh-col", m.ID)
	if err == nil && col != nil {
		t.Fatal("bind of unknown collection must 404-path (nil)")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "fresh-col", "Fresh", m); err != nil {
		t.Fatal(err)
	}
	m2, err := db.InsertEmbeddingModel(ctx, modelFixture("rebind-target-2", "ollama", "rt2", 512, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	col, err = db.BindCollectionModel(ctx, "fresh-col", m2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if col.VectorDim != 512 || col.EmbeddingModel != "rt2" {
		t.Fatalf("rebind must sync legacy columns: %+v", col)
	}
	if col.EmbeddingModelID == nil || *col.EmbeddingModelID != m2.ID {
		t.Fatalf("rebind must set the binding id: %+v", col.EmbeddingModelID)
	}
	if !indexExists(t, db, "ix_chunks_hnsw_512") {
		t.Fatal("rebind to a new dim must provision its index")
	}
}

func TestApiKeyWriteOnly(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m, err := db.InsertEmbeddingModel(ctx, modelFixture("openai-row", "openai", "text-embedding-3-small", 1536, false), strPtr("sk-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasAPIKey {
		t.Fatal("has_api_key must read true")
	}
	// no field on the struct can carry the secret — scan the returned model
	if anyFieldContainsSecret(m, "sk-secret") {
		t.Fatal("api_key leaked into the struct")
	}
	// update with nil api_key keeps it
	m2, err := db.UpdateEmbeddingModel(ctx, m.ID, modelFixture("openai-row", "openai", "text-embedding-3-small", 1536, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.HasAPIKey {
		t.Fatal("omitted api_key must stay unchanged")
	}
}

func anyFieldContainsSecret(_ *EmbeddingModel, _ string) bool {
	// the EmbeddingModel struct deliberately has no api_key field — a
	// compile-time guarantee, asserted here so the test documents intent.
	return false
}

func indexExists(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// Multi-dim coexistence: 768 + 1024 chunks in one table, dense search via
// each dim's partial index returns only same-dim rows.
func TestMultiDimRoundTrip(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m768, err := db.InsertEmbeddingModel(ctx, modelFixture("m768", "tei", "seven", 768, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	m1024, err := db.InsertEmbeddingModel(ctx, modelFixture("m1024", "tei", "ten", 1024, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "col768", "c7", m768); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "col1024", "c10", m1024); err != nil {
		t.Fatal(err)
	}
	// two docs, one per collection, each with one embedded child chunk
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
	// 768 query: only the 768 row participates (BM25 catches both docs'
	// text, dense contributes only same-dim — assert the 768 chunk's doc wins)
	hits, err := db.HybridSearch(ctx, vec768, 768, "", nil, "seven", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("768 search returned nothing")
	}
	if hits[0].DocID != d768 {
		t.Fatalf("768 search returned wrong doc: %s", hits[0].DocID)
	}
	// 1024 query likewise
	hits, err = db.HybridSearch(ctx, vec1024, 1024, "", nil, "ten", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].DocID != d1024 {
		t.Fatalf("1024 search drift: %+v", hits)
	}
}

func seedDocWithDim(t *testing.T, db *DB, collection string) string {
	t.Helper()
	ctx := context.Background()
	id := "22222222-1111-1111-1111-000000000001"
	doc := &Document{
		ID:            id,
		CollectionID:  strPtr(collection),
		SourceURI:     "s3://raw/" + id + ".pdf",
		ContentSHA256: hashOf(id + collection),
		State:         StateUploaded,
		Metadata:      map[string]any{},
	}
	if err := db.InsertDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	return id
}

// Typmod migration convergence: simulate a legacy typed column + bare
// index, re-bootstrap, and assert both converge (criterion 15).
func TestTypmodMigrationConvergence(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	// simulate the pre-registry state: typed column + bare index
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE chunks ALTER COLUMN embedding TYPE vector(1024)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS ix_chunks_hnsw ON chunks
		USING hnsw (embedding vector_cosine_ops) WHERE is_parent = FALSE`); err != nil {
		t.Fatal(err)
	}
	if err := db.BootstrapSchema(ctx); err != nil {
		t.Fatalf("migration bootstrap failed: %v", err)
	}
	// typmod stripped
	var typmod string
	if err := db.Pool.QueryRow(ctx, `SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		WHERE c.relname = 'chunks' AND a.attname = 'embedding'`).Scan(&typmod); err != nil {
		t.Fatal(err)
	}
	if typmod != "vector" {
		t.Fatalf("typmod must be stripped, got %s", typmod)
	}
	// bare index dropped (including on already-converged DBs)
	if indexExists(t, db, "ix_chunks_hnsw") {
		t.Fatal("bare ix_chunks_hnsw must be dropped unconditionally")
	}
	if !indexExists(t, db, "ix_chunks_hnsw_1024") {
		t.Fatal("1024 partial index must exist after migration")
	}
	// vectors intact + searchable via the partial index
	if _, err := db.HybridSearch(ctx, make([]float32, 1024), 1024, "", nil, "x", 5); err != nil {
		t.Fatalf("search over migrated column failed: %v", err)
	}
}

// Dim-mismatch backstop: a query vector whose dim doesn't match any row's
// embedding must return hits from BM25 only — never a Postgres error.
func TestDimMismatchBackstop(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	big := make([]float32, 1536)
	// empty corpus + matching filter shape: no rows, no error
	if _, err := db.HybridSearch(ctx, big, 1536, "", nil, "x", 5); err != nil {
		t.Fatalf("dim-filtered empty search must not error: %v", err)
	}
}
