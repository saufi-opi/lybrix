package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/saufi-opi/lybrix/internal/store"
)

// statusRecorder captures the response status for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// WriteJSON renders one JSON body with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeDetail emits the FastAPI-style error envelope {"detail": "..."} —
// the web SDK runs throwOnError and surfaces detail.
func writeDetail(w http.ResponseWriter, status int, detail string) {
	WriteJSON(w, status, map[string]any{"detail": detail})
}

// recoveryMiddleware converts handler panics into 500 {"detail"} bodies
// instead of connection resets.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				writeDetail(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogMiddleware logs method/path/status/duration.
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type ctxKey string

const apiKeyCtxKey ctxKey = "api_key"

// KeyFromContext returns the authenticated key, or nil when the route runs
// without auth (health/events proxies).
func KeyFromContext(ctx context.Context) *store.ApiKey {
	k, _ := ctx.Value(apiKeyCtxKey).(*store.ApiKey)
	return k
}

// authMiddleware authenticates the bearer key and enforces the route's
// scope. Semantics copied from deps.py require_scope:
//
//	401 missing bearer key / invalid key / revoked / expired
//	403 key lacks required scope
//	usage rows + last_used_at: best-effort, never fail the request
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route-scoped requirement table (matches the 1.0 router wiring).
		scope := routeScope(r.Method, r.URL.Path)
		if scope == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.EqualFold(auth[:min(7, len(auth))], "bearer ") {
			writeDetail(w, http.StatusUnauthorized, "missing bearer key")
			return
		}
		raw := strings.TrimSpace(auth[7:])
		key, err := s.deps.DB.AuthenticateKey(r.Context(), raw)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrKeyExpired):
				writeDetail(w, http.StatusUnauthorized, "key expired")
			default:
				writeDetail(w, http.StatusUnauthorized, "invalid key")
			}
			return
		}
		if err := store.RequireScope(key, scope); err != nil {
			writeDetail(w, http.StatusForbidden, err.Error())
			return
		}
		// Usage bookkeeping: best-effort, never fails the request.
		s.deps.DB.TouchLastUsed(r.Context(), key.ID)
		_ = s.deps.DB.RecordUsage(r.Context(), key.ID, "api", r.Method+" "+r.URL.Path)

		ctx := context.WithValue(r.Context(), apiKeyCtxKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// routeScope maps (method, path) to the required scope; "" means no auth.
// Table copied from the 1.0 router decorators (deps.require_scope calls).
func routeScope(method, path string) string {
	switch {
	// documents: presign/commit/fetch-url ingest; list/get/shards search;
	// retry admin. NOTE: fetch-url must stay ABOVE the matchDocSub(path, "")
	// → "search" case, which would otherwise swallow it (the scope-table
	// test fails loudly if the ordering drifts).
	case method == http.MethodPost && path == "/v1/documents/presign":
		return "ingest"
	case path == "/v1/documents/fetch-url" && method == http.MethodPost:
		return "ingest"
	case matchDocSub(path, "/commit"):
		return "ingest"
	case matchDocSub(path, "/retry"):
		return "admin"
	case matchDocSub(path, "/shards"),
		matchDocSub(path, ""),
		path == "/v1/documents":
		return "search"

	// connectors (OPDS) — ingest scope: they ingest books into collections
	case path == "/v1/connectors/opds/browse" && method == http.MethodPost,
		path == "/v1/connectors/opds/sync" && method == http.MethodPost:
		return "ingest"

	// collections
	case path == "/v1/collections" && method == http.MethodGet,
		strings.HasPrefix(path, "/v1/collections/") && strings.HasSuffix(path, "/stats"):
		return "search"
	case path == "/v1/collections" && method == http.MethodPost,
		matchCollectionModelBind(path):
		return "admin"

	// model registry — admin scope, all six operations
	case path == "/v1/models" && (method == http.MethodGet || method == http.MethodPost),
		path == "/v1/models/test" && method == http.MethodPost,
		matchModelID(path):
		return "admin"

	// search
	case path == "/v1/search":
		return "search"

	// keys
	case path == "/v1/keys":
		return "admin"
	case matchKeyRevoke(path):
		return "admin"

	// usage
	case path == "/v1/usage/summary":
		return "admin"

	default:
		// events (list + stream), system/*, openapi.json — session-gated
		// proxies upstream hit these; no bearer requirement at this layer.
		return ""
	}
}

// matchModelID matches /v1/models/{id} (update + delete).
func matchModelID(path string) bool {
	const prefix = "/v1/models/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	return rest != "" && rest != "test" && !strings.Contains(rest, "/")
}

// matchCollectionModelBind matches /v1/collections/{id}/model.
func matchCollectionModelBind(path string) bool {
	const suffix = "/model"
	if !strings.HasPrefix(path, "/v1/collections/") {
		return false
	}
	return strings.HasSuffix(path, suffix) && len(path) > len("/v1/collections/")+len(suffix)
}

// matchDocSub matches /v1/documents/{id}[/suffix] paths.
func matchDocSub(path, suffix string) bool {
	const prefix = "/v1/documents/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	if suffix == "" {
		// bare {doc_id}: exactly one segment, no further slash
		return rest != "" && !strings.Contains(rest, "/")
	}
	return strings.HasSuffix(rest, suffix) && len(rest) > len(suffix)
}

// matchKeyRevoke matches /v1/keys/{id}/revoke.
func matchKeyRevoke(path string) bool {
	const prefix = "/v1/keys/"
	const suffix = "/revoke"
	return strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix) &&
		len(path) > len(prefix)+len(suffix)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
