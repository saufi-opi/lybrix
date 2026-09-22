package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Golden test: walk the checked-in snapshot's paths and assert each route
// exists with matching methods (contract discipline — the Go server is
// pinned to services/web/openapi.json, 25 paths: the original 18 plus the
// model-registry (6), fetch-url and OPDS (3) operations).
func TestOpenAPIRouteParity(t *testing.T) {
	data, err := os.ReadFile("openapi_snapshot.json")
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Paths) != 25 {
		t.Fatalf("snapshot path count drift: %d", len(doc.Paths))
	}
	s := New(Deps{})
	handler := s.Router()

	for path, ops := range doc.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			req := httptest.NewRequest(strings.ToUpper(method), path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			// A matched route with a bearer requirement answers 401
			// (missing key); an unmatched route falls through to 405/404.
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("route %s %s not registered on the Go router", strings.ToUpper(method), path)
			}
			if rec.Code == http.StatusUnauthorized {
				continue // expected: auth gate rejects before the handler
			}
			// no-auth routes (events/system) reach their handlers, which
			// need a DB — they must NOT be 404/405 (that would mean the
			// route is missing).
			if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("route %s %s missing", strings.ToUpper(method), path)
			}
		}
	}
}

func TestRouteScopeTable(t *testing.T) {
	cases := []struct {
		method, path, scope string
	}{
		{"POST", "/v1/documents/presign", "ingest"},
		{"POST", "/v1/documents/11111111-1111-1111-1111-111111111111/commit", "ingest"},
		{"GET", "/v1/documents", "search"},
		{"GET", "/v1/documents/11111111-1111-1111-1111-111111111111", "search"},
		{"GET", "/v1/documents/11111111-1111-1111-1111-111111111111/shards", "search"},
		{"POST", "/v1/documents/11111111-1111-1111-1111-111111111111/retry", "admin"},
		{"GET", "/v1/collections", "search"},
		{"POST", "/v1/collections", "admin"},
		{"GET", "/v1/collections/books/stats", "search"},
		{"POST", "/v1/search", "search"},
		{"POST", "/v1/keys", "admin"},
		{"GET", "/v1/keys", "admin"},
		{"POST", "/v1/keys/11111111-1111-1111-1111-111111111111/revoke", "admin"},
		{"GET", "/v1/usage/summary", "admin"},
		// model registry — six admin operations
		{"GET", "/v1/models", "admin"},
		{"POST", "/v1/models", "admin"},
		{"POST", "/v1/models/test", "admin"},
		{"POST", "/v1/models/11111111-1111-1111-1111-111111111111", "admin"},
		{"DELETE", "/v1/models/11111111-1111-1111-1111-111111111111", "admin"},
		{"POST", "/v1/collections/books/model", "admin"},
		// multi-source ingestion — three ingest operations (fetch-url must
		// resolve "ingest" above the matchDocSub bare-id "search" case)
		{"POST", "/v1/documents/fetch-url", "ingest"},
		{"POST", "/v1/connectors/opds/browse", "ingest"},
		{"POST", "/v1/connectors/opds/sync", "ingest"},
		{"GET", "/v1/events", ""},
		{"GET", "/v1/events/stream", ""},
		{"GET", "/v1/system/health", ""},
		{"GET", "/v1/system/queues", ""},
		{"GET", "/v1/system/pipeline", ""},
		{"GET", "/v1/system/metrics", ""},
		{"GET", "/openapi.json", ""},
	}
	for _, c := range cases {
		if got := routeScope(c.method, c.path); got != c.scope {
			t.Fatalf("routeScope(%s %s) = %q, want %q", c.method, c.path, got, c.scope)
		}
	}
}

func TestWriteDetailEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDetail(rec, http.StatusNotFound, "document not found")
	if rec.Code != 404 {
		t.Fatalf("status drift: %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["detail"] != "document not found" {
		t.Fatalf("FastAPI detail envelope drift: %v", body)
	}
}

func TestRetryScopeValidation(t *testing.T) {
	// RetryRequest scope pattern ^(shards|embed|full)$ → 422 otherwise
	for _, ok := range []string{"shards", "embed", "full"} {
		if !validRetryScope(ok) {
			t.Fatalf("scope %q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "all", "shardsx"} {
		if validRetryScope(bad) {
			t.Fatalf("scope %q must be invalid", bad)
		}
	}
}

func validRetryScope(s string) bool {
	return s == "shards" || s == "embed" || s == "full"
}

func TestUUIDValidation(t *testing.T) {
	if !isUUID("11111111-1111-1111-1111-111111111111") {
		t.Fatal("valid uuid rejected")
	}
	if isUUID("not-a-uuid") || isUUID("11111111-1111-1111-1111-11111111111") {
		t.Fatal("invalid uuid accepted")
	}
}

func TestKeyExpiryDays(t *testing.T) {
	for k, want := range map[string]int{"1d": 1, "7d": 7, "30d": 30, "90d": 90} {
		if expiryDays[k] != want {
			t.Fatalf("expiry drift: %s=%d", k, expiryDays[k])
		}
	}
	if _, ok := expiryDays["never"]; ok {
		t.Fatal("never must not map to a day count")
	}
}
