package store

import (
	"strings"
	"testing"
)

// BuildMetadataFilter unit tests (no DB): the dialect table, parameterized
// values, and the regex-guarded year cast.
func TestBuildMetadataFilterEmpty(t *testing.T) {
	sql, args, err := BuildMetadataFilter(nil)
	if err != nil || sql != "" || args != nil {
		t.Fatalf("empty filter drift: %q %v %v", sql, args, err)
	}
	sql, args, err = BuildMetadataFilter(map[string]any{})
	if err != nil || sql != "" || args != nil {
		t.Fatalf("empty map drift: %q %v %v", sql, args, err)
	}
}

func TestBuildMetadataFilterAuthor(t *testing.T) {
	sql, args, err := BuildMetadataFilter(map[string]any{"author": "Tolkien"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "lower(df.author) = lower($1)") {
		t.Fatalf("author predicate drift: %s", sql)
	}
	if !strings.HasPrefix(sql, " AND EXISTS (SELECT 1 FROM documents df WHERE df.id = chunks.doc_id") {
		t.Fatalf("EXISTS wrapper drift: %s", sql)
	}
	if len(args) != 1 || args[0] != "Tolkien" {
		t.Fatalf("args drift: %v", args)
	}
}

func TestBuildMetadataFilterYearRangeGuarded(t *testing.T) {
	sql, args, err := BuildMetadataFilter(map[string]any{"year_from": 2018, "year_to": 2022})
	if err != nil {
		t.Fatal(err)
	}
	// the regex guard MUST precede the ::numeric cast (one "year": "n/a"
	// row would otherwise 500 every filtered search)
	guard := strings.Index(sql, "~ '^-?[0-9]+(\\.[0-9]+)?$'")
	cast := strings.Index(sql, "::numeric")
	if guard == -1 || cast == -1 || guard > cast {
		t.Fatalf("regex guard must precede the numeric cast: %s", sql)
	}
	if !strings.Contains(sql, "BETWEEN") == false && !strings.Contains(sql, ">= $") {
		t.Fatalf("range comparison drift: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("args drift: %v", args)
	}
}

func TestBuildMetadataFilterCustomKey(t *testing.T) {
	sql, args, err := BuildMetadataFilter(map[string]any{"custom_tag": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "df.metadata->>'custom_tag' = $1") {
		t.Fatalf("custom key predicate drift: %s", sql)
	}
	if len(args) != 1 || args[0] != "x" {
		t.Fatalf("args drift: %v", args)
	}
}

func TestBuildMetadataFilterRejects(t *testing.T) {
	bad := []map[string]any{
		{"bad key!": "x"},     // unsafe key characters
		{"author": 42},        // non-string author
		{"year_from": "soon"}, // non-numeric range
		{"k": 5},              // non-string custom value
	}
	for _, f := range bad {
		if _, _, err := BuildMetadataFilter(f); err == nil {
			t.Fatalf("filter %v must be rejected", f)
		}
	}
}
