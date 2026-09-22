package store

import (
	"context"
	"testing"
)

// Metadata filter acceptance (PLAN.md §Verification, store lane): seed
// documents with MIXED-TYPED metadata — {"year": 2021}, {"year": "2019"},
// {"year": "n/a"}, {"year": null}, {} — then the regex-guarded year cast
// must filter correctly WITHOUT a Postgres cast error, and author/
// custom-key equality must behave per the dialect table.
func TestMetadataFilterMixedTypes(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	m, err := db.InsertEmbeddingModel(ctx, modelFixture("mf", "tei", "d", 1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "mfcol", "MF", m); err != nil {
		t.Fatal(err)
	}
	docs := []struct {
		id       string
		author   *string
		metadata map[string]any
	}{
		{"33333333-1111-1111-1111-000000000001", strPtr("Alpha"), map[string]any{"year": 2021}},
		{"33333333-1111-1111-1111-000000000002", strPtr("alpha"), map[string]any{"year": "2019"}},
		{"33333333-1111-1111-1111-000000000003", strPtr("Beta"), map[string]any{"year": "n/a"}},
		{"33333333-1111-1111-1111-000000000004", strPtr("Beta"), map[string]any{"year": nil}},
		{"33333333-1111-1111-1111-000000000005", strPtr("Gamma"), map[string]any{"custom_tag": "y"}},
	}
	for _, d := range docs {
		doc := &Document{
			ID:            d.id,
			CollectionID:  strPtr("mfcol"),
			Author:        d.author,
			Title:         d.author,
			SourceURI:     "s3://raw/" + d.id,
			ContentSHA256: hashOf(d.id),
			State:         StateReady,
			Metadata:      d.metadata,
		}
		if err := db.InsertDocument(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}

	// year range: numeric + numeric-string rows return; "n/a"/null/absent
	// are excluded WITHOUT a Postgres cast error.
	filter, args, err := BuildMetadataFilter(map[string]any{"year_from": 2018, "year_to": 2022})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := db.HybridSearch(ctx, make([]float32, 1024), 1024, "", nil, "", 10, filter, args)
	if err != nil {
		t.Fatalf("mixed-typed metadata search must not error: %v", err)
	}
	gotDocs := map[string]bool{}
	for _, h := range hits {
		gotDocs[h.DocID] = true
	}
	if len(gotDocs) != 2 || !gotDocs["33333333-1111-1111-1111-000000000001"] ||
		!gotDocs["33333333-1111-1111-1111-000000000002"] {
		t.Fatalf("year range must match exactly the numeric rows: %v", gotDocs)
	}

	// author: case-insensitive
	filter, args, err = BuildMetadataFilter(map[string]any{"author": "ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err = db.HybridSearch(ctx, make([]float32, 1024), 1024, "", nil, "", 10, filter, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("case-insensitive author must match both rows, got %d", len(hits))
	}

	// custom key: exact text equality
	filter, args, err = BuildMetadataFilter(map[string]any{"custom_tag": "y"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err = db.HybridSearch(ctx, make([]float32, 1024), 1024, "", nil, "", 10, filter, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != "33333333-1111-1111-1111-000000000005" {
		t.Fatalf("custom key drift: %+v", hits)
	}

	// combined: BM25 leg carries the same predicate (empty query → BM25
	// leg matches everything is wrong — paradedb.parse('') matches all; use
	// an empty filter to confirm no predicate fires)
	hits, err = db.HybridSearch(ctx, make([]float32, 1024), 1024, "", nil, "", 10, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatalf("empty filter must see every doc, got %d", len(hits))
	}
}
