package store

import (
	"strings"
	"testing"
)

// TestBumpShardsFailedSQL pins the pure VALUES builder behind the batched
// escalate counter update (R-36): 0 docs → empty (caller no-ops), 1 doc →
// one VALUES row, N docs → deterministic (sorted) rows with positional
// placeholders, documents joined by id.
func TestBumpShardsFailedSQL(t *testing.T) {
	// empty map → empty SQL, no args
	sql, args := bumpShardsFailedSQL(map[string]int{})
	if sql != "" || args != nil {
		t.Fatalf("empty deltas must yield empty SQL: %q %v", sql, args)
	}

	// single doc
	sql, args = bumpShardsFailedSQL(map[string]int{"aaaaaaaa-0000-0000-0000-000000000000": 3})
	if !strings.Contains(sql, "UPDATE documents SET shards_failed = shards_failed + d.n") {
		t.Fatalf("update clause drift: %q", sql)
	}
	if !strings.Contains(sql, "FROM (VALUES ($1::uuid, $2::int)) AS d(id, n)") {
		t.Fatalf("single-row VALUES drift: %q", sql)
	}
	if !strings.Contains(sql, "WHERE documents.id = d.id") {
		t.Fatalf("join drift: %q", sql)
	}
	if len(args) != 2 || args[0] != "aaaaaaaa-0000-0000-0000-000000000000" || args[1] != 3 {
		t.Fatalf("single-doc args drift: %v", args)
	}

	// N docs: sorted deterministic order, positional placeholders
	a := "aaaaaaaa-0000-0000-0000-000000000000"
	b := "bbbbbbbb-0000-0000-0000-000000000000"
	c := "cccccccc-0000-0000-0000-000000000000"
	sql, args = bumpShardsFailedSQL(map[string]int{c: 3, a: 1, b: 2})
	if got := strings.Count(sql, "($"); got != 3 {
		t.Fatalf("expected 3 VALUES rows, got %d: %q", got, sql)
	}
	// sorted: a, b, c
	wantOrder := []string{a, b, c}
	for i, id := range wantOrder {
		if args[i*2] != id {
			t.Fatalf("row %d id drift: %v, want %s (sql %q)", i, args[i*2], id, sql)
		}
	}
	if args[1] != 1 || args[3] != 2 || args[5] != 3 {
		t.Fatalf("deltas drift: %v", args)
	}
}
