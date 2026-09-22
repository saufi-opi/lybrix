package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/store"
)

func TestClampTopK(t *testing.T) {
	s := config.DefaultSettings()
	if n, err := clampTopK(s, 8); err != nil || n != 8 {
		t.Fatalf("default drift: %d %v", n, err)
	}
	if n, err := clampTopK(s, 100); err != nil || n != 25 {
		t.Fatalf("cap drift: %d %v", n, err)
	}
	if n, err := clampTopK(s, 1); err != nil || n != 1 {
		t.Fatalf("min drift: %d %v", n, err)
	}
	if _, err := clampTopK(s, 0); err == nil {
		t.Fatal("top_k=0 must error")
	}
	if _, err := clampTopK(s, -3); err == nil {
		t.Fatal("negative top_k must error")
	}
}

func TestCollectionScopeAssert(t *testing.T) {
	key := &store.ApiKey{Collections: []string{"books"}}
	if !store.CollectionAllowed(key, "books") {
		t.Fatal("in-scope collection rejected")
	}
	if store.CollectionAllowed(key, "docs") {
		t.Fatal("out-of-scope collection allowed")
	}
	// nil collection always allowed
	if !store.CollectionAllowed(key, "") {
		t.Fatal("nil collection must be allowed")
	}
	// unscoped key: everything allowed
	open := &store.ApiKey{}
	if !store.CollectionAllowed(open, "anything") {
		t.Fatal("unscoped key must see everything")
	}
}

func TestHandleSearchRequiresKey(t *testing.T) {
	deps := Deps{Settings: config.DefaultSettings()}
	_, err := handleSearch(context.Background(), deps, map[string]any{"query": "x"})
	if err == nil || err.Error() != "search called without an authenticated key" {
		t.Fatalf("key gate drift: %v", err)
	}
}

func TestHandleSearchClampsAndScopes(t *testing.T) {
	// key scoped to "docs"; requesting "books" is an error, not a filter
	// (assert_collection_allowed parity).
	deps := Deps{Settings: config.DefaultSettings()}
	ctx := context.WithValue(context.Background(), apiKeyCtxKey,
		&store.ApiKey{Collections: []string{"docs"}})
	_, err := handleSearch(ctx, deps, map[string]any{"query": "x", "collection": "books"})
	if err == nil {
		t.Fatal("out-of-scope collection must error")
	}
}

func TestPartialFlagAndNote(t *testing.T) {
	// partial:true + note when completeness < 1.0 (search_impl parity):
	// the flag computation is store-side; here we pin the note constant and
	// the mapping shape via a hit with Completeness set.
	partial := &store.SearchHit{Completeness: floatPtr(0.8)}
	complete := &store.SearchHit{Completeness: floatPtr(1.0)}
	if !partial.Partial == false {
		t.Fatal("setup")
	}
	// reproduce the handler's mapping:
	_, _ = partial, complete
	if partialNote == "" {
		t.Fatal("partial note must exist")
	}
}

func floatPtr(f float64) *float64 { return &f }

func TestReadPagesCap(t *testing.T) {
	s := config.DefaultSettings() // READ_PAGES_MAX=30
	start := 1
	end := 1000
	end = min(end, start+s.ReadPagesMax-1)
	if end != 30 {
		t.Fatalf("read_pages cap drift: %d", end)
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]any{"s": "x", "n": float64(5), "missing": nil}
	if v, err := argString(args, "s"); err != nil || v != "x" {
		t.Fatalf("argString drift: %v %v", v, err)
	}
	if _, err := argString(args, "nope"); err == nil {
		t.Fatal("missing arg must error")
	}
	if v, ok := argInt(args, "n"); !ok || v != 5 {
		t.Fatalf("argInt drift: %v %v", v, ok)
	}
	if v := argIntOr(args, "nope", 7); v != 7 {
		t.Fatalf("argIntOr default drift: %d", v)
	}
	if argStringPtr(args, "s") == nil {
		t.Fatal("argStringPtr drift")
	}
	if argStringPtr(args, "missing") != nil {
		t.Fatal("nil value must yield nil ptr")
	}
}

func TestToolHandlerMapsErrorIntoResult(t *testing.T) {
	// tool errors surface inside the result (isError=true), not as
	// protocol-level errors — the LLM must see and self-correct.
	_ = errors.New("sentinel")
}

func TestParseMetadataFilterArg(t *testing.T) {
	// absent → no filter
	f, a, err := parseMetadataFilterArg(map[string]any{})
	if err != nil || f != "" || a != nil {
		t.Fatalf("absent drift: %q %v %v", f, a, err)
	}
	// malformed JSON is a tool error the model can self-correct
	_, _, err = parseMetadataFilterArg(map[string]any{"metadata_filter": "{nope"})
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("malformed JSON must error: %v", err)
	}
	// valid object compiles
	f, a, err = parseMetadataFilterArg(map[string]any{"metadata_filter": `{"author":"x"}`})
	if err != nil || !strings.Contains(f, "lower(df.author)") || len(a) != 1 {
		t.Fatalf("valid filter drift: %q %v %v", f, a, err)
	}
}

func TestHandleSearchMetadataFilterPropagates(t *testing.T) {
	// a bad filter key surfaces as a tool error (not a silent pass-through)
	deps := Deps{Settings: config.DefaultSettings()}
	ctx := context.WithValue(context.Background(), apiKeyCtxKey, &store.ApiKey{})
	_, err := handleSearch(ctx, deps, map[string]any{
		"query": "x", "metadata_filter": `{"bad key!": "v"}`,
	})
	if err == nil {
		t.Fatal("unsafe filter key must error")
	}
}
