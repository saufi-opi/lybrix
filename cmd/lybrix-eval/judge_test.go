package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Judge parity: cases ported from tests/test_eval_judge.py semantics.

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"C++ Crash Course":  "cplusplus crash course",
		"C# in Depth":       "csharp in depth",
		"Go in Action":      "go in action",
		"Learning SQL, 2nd": "learning sql 2nd",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpaceStrippedLen(t *testing.T) {
	if SpaceStrippedLen("Go in Action") != 10 {
		t.Fatalf("len drift: %d", SpaceStrippedLen("Go in Action"))
	}
	if SpaceStrippedLen("Go") != 2 {
		t.Fatalf("short len drift: %d", SpaceStrippedLen("Go"))
	}
}

func TestTitlesMatch(t *testing.T) {
	// exact-equal normal forms
	if !TitlesMatch("Go in Action", "go in action!") {
		t.Fatal("normalized equality must match")
	}
	// space-stripped substring: slug joins title
	if !TitlesMatch("cpluspluscrashcourse", "C++ Crash Course: A Fast-Paced Introduction") {
		t.Fatal("slug must match title via space-stripped substring")
	}
	// the guard: short titles never match by substring
	if TitlesMatch("Go", "Cloud Native Go") {
		t.Fatal("2-char title must not substring-match")
	}
	if !TitlesMatch("Go in Action", "Go in Action") {
		t.Fatal("exact self-match must hold")
	}
}

func TestTitleHit(t *testing.T) {
	results := []map[string]any{
		{"doc_title": "Some Other Book"},
		{"doc_title": nil},
		{"doc_title": "Cloud Native Go"},
	}
	if rank := TitleHit([]string{"Cloud Native Go"}, results); rank == nil || *rank != 3 {
		t.Fatalf("rank drift: %v", rank)
	}
	if rank := TitleHit([]string{"No Such Book"}, results); rank != nil {
		t.Fatalf("miss must be nil, got %v", *rank)
	}
}

func TestHeadingHit(t *testing.T) {
	result := map[string]any{"heading_path": []any{"Chapter 3", "Reciprocal Rank Fusion (RRF)"}}
	if !HeadingHit([]string{"Reciprocal Rank Fusion"}, result) {
		t.Fatal("expected heading hit")
	}
	if HeadingHit([]string{"Totally Different"}, result) {
		t.Fatal("unexpected heading hit")
	}
	if HeadingHit([]string{"anything"}, map[string]any{}) {
		t.Fatal("empty path must not hit")
	}
}

func TestIsJunk(t *testing.T) {
	// TOC trap
	if !IsJunk(map[string]any{"heading_path": []any{"Front Matter", "Table of Contents"}}) {
		t.Fatal("TOC heading must be junk")
	}
	// junk last heading
	if !IsJunk(map[string]any{"heading_path": []any{"Chapter", "Index"}}) {
		t.Fatal("Index last-heading must be junk")
	}
	// clean result
	if IsJunk(map[string]any{"heading_path": []any{"Chapter 1", "Introduction"}}) {
		t.Fatal("clean result flagged as junk")
	}
	// front-matter filename artifacts
	if !IsJunk(map[string]any{"doc_title": "1234-FM-something"}) {
		t.Fatal("front-matter title must be junk")
	}
	if !IsJunk(map[string]any{"doc_title": "book.indd"}) {
		t.Fatal(".indd title must be junk")
	}
}

// Dataset loader contract (dataset.py).
func TestLoadDatasetValidation(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(dir, "ok.jsonl")
		content := `{"id":"exact-01","query":"q1","expected_doc_titles":["Developing Apps with GPT-4 and ChatGPT"],"category":"exact","notes":"n"}
{"id":"concept-01","query":"q2","expected_doc_titles":["Kanban in Action"],"expected_headings":["Board"],"category":"conceptual"}

`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		cases, err := LoadDataset(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cases) != 2 {
			t.Fatalf("case count drift: %d", len(cases))
		}
	})

	t.Run("duplicate id", func(t *testing.T) {
		path := filepath.Join(dir, "dup.jsonl")
		row := `{"id":"a-1","query":"q","expected_doc_titles":["Long Enough Title Here"],"category":"exact"}`
		if err := os.WriteFile(path, []byte(row+"\n"+row), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDataset(path)
		if err == nil || !strings.Contains(err.Error(), "duplicate id") {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})

	t.Run("unknown category", func(t *testing.T) {
		path := filepath.Join(dir, "cat.jsonl")
		row := `{"id":"a-1","query":"q","expected_doc_titles":["Long Enough Title Here"],"category":"weird"}`
		if err := os.WriteFile(path, []byte(row), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDataset(path)
		if err == nil || !strings.Contains(err.Error(), "unknown category") {
			t.Fatalf("expected category error, got %v", err)
		}
	})

	t.Run("short title rejected", func(t *testing.T) {
		path := filepath.Join(dir, "short.jsonl")
		row := `{"id":"a-1","query":"q","expected_doc_titles":["Go"],"category":"exact"}`
		if err := os.WriteFile(path, []byte(row), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDataset(path)
		if err == nil || !strings.Contains(err.Error(), "8 space-stripped chars") {
			t.Fatalf("expected short-title error, got %v", err)
		}
	})

	t.Run("missing titles", func(t *testing.T) {
		path := filepath.Join(dir, "notitles.jsonl")
		row := `{"id":"a-1","query":"q","category":"exact"}`
		if err := os.WriteFile(path, []byte(row), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDataset(path)
		if err == nil || !strings.Contains(err.Error(), "expected_doc_titles") {
			t.Fatalf("expected titles error, got %v", err)
		}
	})
}

// Seed dataset loads clean (the golden set is unchanged in 2.0).
func TestSeedDatasetLoads(t *testing.T) {
	cases, err := LoadDataset("../../scripts/eval/datasets/seed.jsonl")
	if err != nil {
		t.Fatalf("seed dataset must load: %v", err)
	}
	if len(cases) != 15 {
		t.Fatalf("seed count drift: %d", len(cases))
	}
	cats := map[string]int{}
	for _, c := range cases {
		cats[c.Category]++
	}
	if cats["exact"] != 5 || cats["conceptual"] != 5 || cats["drift"] != 5 {
		t.Fatalf("seed stratification drift: %v", cats)
	}
}

// Evaluate: hit@k / MRR aggregation + junk impact.
func TestEvaluateMetrics(t *testing.T) {
	casesByID := map[string]QueryCase{
		"q1": {ID: "q1", Category: "exact", ExpectedDocTitles: []string{"Cloud Native Go"}},
		"q2": {ID: "q2", Category: "exact", ExpectedDocTitles: []string{"Kanban in Action"}},
		"q3": {ID: "q3", Category: "drift", ExpectedDocTitles: []string{"Nothing Matches This"}},
	}
	rows := []map[string]any{
		{"id": "q1", "category": "exact", "results": []map[string]any{
			{"doc_title": "Cloud Native Go"}, // rank 1
		}},
		{"id": "q2", "category": "exact", "results": []map[string]any{
			{"doc_title": "Other"}, {"doc_title": "Kanban in Action"}, // rank 2
		}},
		// RunQuery stores the FILTERED list in `results`; raw_results keeps
		// junk for the impact metric. q3's only hit was junk → empty.
		{"id": "q3", "category": "drift", "results": []map[string]any{},
			"raw_results": []map[string]any{{"doc_title": "Junk One"}}},
	}
	summary := Evaluate(rows, casesByID, IsJunk)
	if summary.TotalQueries != 3 || summary.Evaluated != 2 {
		// `evaluated` counts only non-error, non-empty searches (judge.py:
		// q3 is an empty response, not an evaluated query).
		t.Fatalf("counts drift: total=%d evaluated=%d", summary.TotalQueries, summary.Evaluated)
	}
	if summary.Overall.HitsAt1 != 1 || summary.Overall.HitsAt3 != 2 {
		t.Fatalf("hit counts drift: %+v", summary.Overall)
	}
	if mrr := summary.Overall.MRR(); mrr < 0.74 || mrr > 0.75 {
		t.Fatalf("mrr drift: %.3f", mrr)
	}
	if summary.EmptyResponses != 1 {
		t.Fatalf("empty response drift: %d", summary.EmptyResponses)
	}
	// empty responses short-circuit before junk counting (judge.py order),
	// so q3's junk hit is NOT counted here — junk impact only accumulates
	// on rows with results. Pinned via a junk row with surviving results:
	junkRow := map[string]any{
		"id": "q4", "category": "drift",
		"results":     []map[string]any{{"doc_title": "Cloud Native Go"}},
		"raw_results": []map[string]any{{"heading_path": []any{"Chapter", "Index"}}, {"doc_title": "Cloud Native Go"}},
	}
	summary2 := Evaluate([]map[string]any{junkRow}, map[string]QueryCase{
		"q4": {ID: "q4", Category: "drift", ExpectedDocTitles: []string{"Cloud Native Go"}},
	}, IsJunk)
	if summary2.JunkHitsTop8Raw != 1 || summary2.JunkSlotsTop8Raw != 2 {
		t.Fatalf("junk raw drift: hits=%d slots=%d", summary2.JunkHitsTop8Raw, summary2.JunkSlotsTop8Raw)
	}
	// 1.0 semantics: empty responses count as a miss in their CATEGORY only,
	// never in overall — so overall sees q1 (rank1) + q2 (rank2) = 2/2.
	if h := summary.Overall.HitAtTopK(); h != 1.0 {
		t.Fatalf("hit_at_top_k drift: %.3f", h)
	}
	// category-level: drift sees its empty miss
	if st := summary.ByCategory["drift"]; st.Queries != 1 || st.HitAtTopK() != 0.0 {
		t.Fatalf("category miss drift: %+v", st)
	}
}

func TestAnyResultsRate(t *testing.T) {
	rows := []map[string]any{
		{"results": []map[string]any{{}}},
		{"results": []map[string]any{}},
		{"error": "boom"},
	}
	if r := AnyResultsRate(rows); r != 0.5 {
		t.Fatalf("rate drift: %.2f", r)
	}
}
