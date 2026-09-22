package store

import (
	"context"
	"testing"
)

// rerankModelFixture returns a rerank registry row with knobs per test.
func rerankModelFixture(name, provider, modelID string) *RerankModel {
	return &RerankModel{
		Name: name, Provider: provider, ModelID: modelID,
		QueryURL: "http://127.0.0.1:8083", TruncateChars: 6000,
	}
}

func TestRerankModelCRUD(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m, err := db.InsertRerankModel(ctx, rerankModelFixture("rr", "tei", "bge-reranker-v2-m3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.TruncateChars != 6000 || m.HasAPIKey {
		t.Fatalf("insert drift: %+v", m)
	}
	// openai key is write-only
	m2, err := db.InsertRerankModel(ctx, rerankModelFixture("rr-key", "openai", "cohere/rerank"), strPtr("sk-rk"))
	if err != nil {
		t.Fatal(err)
	}
	if !m2.HasAPIKey {
		t.Fatal("has_api_key must read true with a key")
	}
	// update keeps the key when omitted
	m3, err := db.UpdateRerankModel(ctx, m2.ID, rerankModelFixture("rr-key", "openai", "cohere/rerank-v2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m3 == nil || m3.ModelID != "cohere/rerank-v2" || !m3.HasAPIKey {
		t.Fatalf("update drift: %+v", m3)
	}
	// delete the unbound row
	if err := db.DeleteRerankModel(ctx, m.ID); err != nil {
		t.Fatalf("unbound reranker delete must succeed: %v", err)
	}
	// unknown id → typed not-found
	if err := db.DeleteRerankModel(ctx, m.ID); err != ErrRerankModelNotFound {
		t.Fatalf("repeat delete must be ErrRerankModelNotFound, got %v", err)
	}
}

func TestRerankModelInUseRefusesDelete(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m, err := db.InsertRerankModel(ctx, rerankModelFixture("bound-rr", "tei", "bge"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindCollectionReranker(ctx, "books", m.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRerankModel(ctx, m.ID); err == nil || err.Error()[:17] != ErrRerankModelInUse.Error()[:17] {
		t.Fatalf("bound reranker delete must refuse: %v", err)
	}
	// clear the binding, then delete succeeds
	if _, err := db.BindCollectionReranker(ctx, "books", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRerankModel(ctx, m.ID); err != nil {
		t.Fatalf("cleared binding must allow delete: %v", err)
	}
}

func TestRerankSeedOnlyWhenEnabled(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	// disabled → no row
	if err := db.SeedDefaultRerankModel(ctx, RerankSeed{Enabled: false, ModelID: "bge-reranker-v2-m3", QueryURL: "http://q"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListRerankModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("disabled seed must leave the table empty, got %d", len(rows))
	}
	// enabled → exactly one idempotent row
	seed := RerankSeed{Enabled: true, ModelID: "bge-reranker-v2-m3", QueryURL: "http://q"}
	if err := db.SeedDefaultRerankModel(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedDefaultRerankModel(ctx, seed); err != nil {
		t.Fatalf("second seed must be a no-op: %v", err)
	}
	rows, err = db.ListRerankModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("enabled seed must create exactly one row, got %d", len(rows))
	}
	m := rows[0]
	if m.Provider != "tei" || m.ModelID != "bge-reranker-v2-m3" || m.QueryURL != "http://q" {
		t.Fatalf("seed row drift: %+v", m)
	}
	// the seed row resolves as a collection's bound reranker
	if _, err := db.BindCollectionReranker(ctx, "seedcol", m.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.ResolveCollectionReranker(ctx, "seedcol")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != m.ID {
		t.Fatalf("collection resolution drift: %+v", got)
	}
}
