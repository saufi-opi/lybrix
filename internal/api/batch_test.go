package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saufi-opi/lybrix/internal/store"
)

// The fail-closed contract surfaces of Phase 1: batch ops resolve every
// doc_id and check every collection BEFORE mutating anything, and the
// collection-chunk listing checks the allowlist before querying. Handler
// legs that need a live DB live in the testcontainers store lane; the pure
// guards are asserted here.

// keyAllowedCollection: unscoped keys pass, scoped keys pass only on their
// collections, and the 403 writes the detail envelope itself.
func TestKeyAllowedCollection(t *testing.T) {
	scoped := &store.ApiKey{Collections: []string{"books"}}
	rec := httptest.NewRecorder()
	if !keyAllowedCollection(rec, nil, strPtr("anything")) {
		t.Fatal("nil key must pass")
	}
	if !keyAllowedCollection(rec, scoped, strPtr("books")) {
		t.Fatal("in-scope collection rejected")
	}
	rec2 := httptest.NewRecorder()
	if keyAllowedCollection(rec2, scoped, strPtr("other")) {
		t.Fatal("out-of-scope collection passed")
	}
	if rec2.Code != 403 || !strings.Contains(rec2.Body.String(), "not scoped") {
		t.Fatalf("403 envelope drift: %d %s", rec2.Code, rec2.Body.String())
	}
}
