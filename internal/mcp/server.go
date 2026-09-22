// Package mcp: the six-tool MCP server (mcp-go, streamable HTTP at
// :8430/mcp). Design rules carried from 1.0's FastMCP server (PRD §7.2):
//
//   - every result carries the citation triple (doc_title, page_start, page_end);
//   - every response is bounded — read_pages caps at READ_PAGES_MAX pages,
//     search caps top_k at SEARCH_MAX_TOP_K;
//   - tool descriptions state WHEN to use each one, not just what it does;
//   - search flags partial: true when a matched document has completeness
//     < 1.0, so the agent knows the corpus has a hole there;
//   - per-method key_usage rows ("tools/call:<name>" refinement);
//   - /health is unauthenticated; every other path requires a bearer key
//     and returns a real HTTP 401 on failure.
package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Deps carries the shared wiring.
type Deps struct {
	Settings *config.Settings
	DB       *store.DB
	// EmbedQuery produces a query-plane embedding for one collection
	// (resolves the bound model via the registry) and returns the model
	// alongside the vector so tools forward its dim into HybridSearch.
	EmbedQuery func(ctx context.Context, collection, text string) (vec []float32, model *store.EmbeddingModel, err error)
	// EmbedForModel produces a query-plane embedding with a SPECIFIC
	// registered model — the multi-collection grouped search calls it once
	// per unique model (WeKnora multi-KB architecture; no default row).
	EmbedForModel func(ctx context.Context, m *store.EmbeddingModel, text string) ([]float32, error)
	// ResolveReranker is the rerank resolution rule (service.ResolveRerankModel
	// in production; tests stub). Nil → rerank never runs.
	ResolveReranker func(ctx context.Context, req service.RerankResolveRequest) (*store.RerankModel, error)
	// RerankHits is the cross-encoder stage (service.RerankHits composed
	// with a registry-resolved client in production; tests stub). Nil → off.
	RerankHits func(ctx context.Context, rm *store.RerankModel, query string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, service.RerankStatus, error)
}

type ctxKey string

const apiKeyCtxKey ctxKey = "mcp_api_key"

// KeyFromContext returns the authenticated key for the in-flight request.
func KeyFromContext(ctx context.Context) *store.ApiKey {
	k, _ := ctx.Value(apiKeyCtxKey).(*store.ApiKey)
	return k
}

// New builds the mcp-go MCPServer with the six tools and hooks up the
// per-method usage recorder.
func New(deps Deps) *server.MCPServer {
	s := server.NewMCPServer("lybrix", "2.0.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	s.AddTool(searchTool(), toolHandler(deps, handleSearch))
	s.AddTool(getChunkContextTool(), toolHandler(deps, handleGetChunkContext))
	s.AddTool(readPagesTool(), toolHandler(deps, handleReadPages))
	s.AddTool(listDocumentsTool(), toolHandler(deps, handleListDocuments))
	s.AddTool(getDocumentTool(), toolHandler(deps, handleGetDocument))
	s.AddTool(listCollectionsTool(), toolHandler(deps, handleListCollections))
	s.AddTool(listChunksTool(), toolHandler(deps, handleListChunks))
	s.AddTool(previewChunkTool(), toolHandler(deps, handlePreviewChunk))

	return s
}

// toolHandler wraps a tool impl with the usage row ("tools/call:<name>")
// and error mapping. Usage writes are best-effort: an analytics write must
// never fail a search.
func toolHandler(deps Deps, fn func(ctx context.Context, deps Deps, args map[string]any) (any, error)) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.Params.Name
		if key := KeyFromContext(ctx); key != nil {
			_ = deps.DB.RecordUsage(ctx, key.ID, "mcp", "tools/call:"+name)
		}
		out, err := fn(ctx, deps, request.GetArguments())
		if err != nil {
			// tool errors surface inside the result (isError=true) so the
			// LLM can see and self-correct — not as protocol errors.
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, jerr := jsonMarshal(out)
		if jerr != nil {
			return mcp.NewToolResultError("marshal result: " + jerr.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// --- tool definitions (descriptions copy 1.0 strings verbatim) ------------

func searchTool() mcp.Tool {
	return mcp.NewTool("search",
		mcp.WithDescription(`Primary tool. Hybrid (dense + BM25) semantic search over the
ingested corpus with page-number citations. Use for "what does the
library say about X" questions; returns top_k chunks with doc
title, page range, and heading path for verification.`),
		mcp.WithString("query", mcp.Required(), mcp.Description("the search query")),
		mcp.WithString("collection", mcp.Description("optional collection id to scope the search; omitted searches ALL collections — grouped by each collection's bound embedding model, one query embedding per unique model, fused with RRF")),
		mcp.WithNumber("top_k", mcp.Description("max results (default 8, capped at 25)")),
		mcp.WithBoolean("rerank", mcp.Description("re-score the candidate pool with the configured cross-encoder reranker (needs a bound or explicitly named rerank model; failure degrades to RRF order, never an error)")),
		mcp.WithString("rerank_model_id", mcp.Description("explicit reranker registry id — overrides the collection's bound reranker (required for multi-collection searches)")),
		mcp.WithString("metadata_filter", mcp.Description(`optional document-level filter as a JSON object string, e.g. {"author": "Tolkien", "year_from": 2018, "custom_tag": "x"}; author matches case-insensitively, year_from/year_to bound the metadata "year" field, any other key matches metadata exactly`)),
	)
}

func getChunkContextTool() mcp.Tool {
	return mcp.NewTool("get_chunk_context",
		mcp.WithDescription(`Fetch ±window neighbouring chunks in document order. Use after
a search hit that cut mid-argument.`),
		mcp.WithString("chunk_id", mcp.Required(), mcp.Description("the chunk id from a search hit")),
		mcp.WithNumber("window", mcp.Description("neighbours on each side (default 2)")),
	)
}

func readPagesTool() mcp.Tool {
	return mcp.NewTool("read_pages",
		mcp.WithDescription(`Read a bounded page range as markdown (max 30 pages/call). Use
after search/get_document when a citation needs surrounding
context; never to scan whole books.`),
		mcp.WithString("doc_id", mcp.Required(), mcp.Description("document id")),
		mcp.WithNumber("page_start", mcp.Required(), mcp.Description("first page (1-based)")),
		mcp.WithNumber("page_end", mcp.Required(), mcp.Description("last page (1-based, inclusive)")),
	)
}

func listDocumentsTool() mcp.Tool {
	return mcp.NewTool("list_documents",
		mcp.WithDescription(`Browse/verify: list documents with state and completeness. Use
to check whether a specific book is ingested before deep reads.`),
		mcp.WithString("collection", mcp.Description("optional collection filter")),
		mcp.WithString("state", mcp.Description("optional state filter")),
		mcp.WithString("query", mcp.Description("optional title substring filter")),
		mcp.WithNumber("limit", mcp.Description("max rows (default 50, capped at 200)")),
	)
}

func getDocumentTool() mcp.Tool {
	return mcp.NewTool("get_document",
		mcp.WithDescription(`Orientation before deep read: metadata, completeness, chunk
count for one document.`),
		mcp.WithString("doc_id", mcp.Required(), mcp.Description("document id")),
	)
}

func listCollectionsTool() mcp.Tool {
	return mcp.NewTool("list_collections",
		mcp.WithDescription(`Enumerate collections (id, name, model, doc count). Call this
first to scope later searches to a subject area.`),
	)
}

func listChunksTool() mcp.Tool {
	return mcp.NewTool("list_chunks",
		mcp.WithDescription(`Chunk-level debugging: list a document's chunks in document
order (seq, pages, token count, heading path, text preview). Use when a
citation seems truncated or mis-attributed and you need to see how the
document was segmented.`),
		mcp.WithString("doc_id", mcp.Required(), mcp.Description("document id")),
		mcp.WithNumber("page", mcp.Description("1-based page of the listing (default 1)")),
		mcp.WithNumber("page_size", mcp.Description("rows per page (default 50, capped at 200)")),
	)
}

func previewChunkTool() mcp.Tool {
	return mcp.NewTool("preview_chunk",
		mcp.WithDescription(`Full detail for one chunk: complete text, token count,
breadcrumb, parent text, and seq neighbours. Use after list_chunks (or a
suspicious search hit) to inspect exactly what was embedded.`),
		mcp.WithString("chunk_id", mcp.Required(), mcp.Description("the chunk id to preview")),
	)
}

// HTTPHandler assembles the streamable-HTTP transport behind the bearer
// gate. /health stays open (compose healthchecks, load balancers); every
// other path requires a valid key and answers a real HTTP 401 otherwise.
func HTTPHandler(deps Deps) http.Handler {
	mcpServer := New(deps)
	streamable := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// /health bypasses auth middleware; return nothing but status.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/mcp", authGate(deps, streamable))
	return mux
}

// authGate is the ASGI-gate equivalent: every path except /health requires
// a valid bearer key; on success the ApiKey travels to tools through the
// request context.
func authGate(deps Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.EqualFold(auth[:min(7, len(auth))], "bearer ") {
			unauthorized(w, "missing bearer key")
			return
		}
		key, err := deps.DB.AuthenticateKey(r.Context(), strings.TrimSpace(auth[7:]))
		if err != nil {
			unauthorized(w, err.Error())
			return
		}
		// gate-level usage row + last_used_at bump; best-effort
		deps.DB.TouchLastUsed(r.Context(), key.ID)
		_ = deps.DB.RecordUsage(r.Context(), key.ID, "mcp", "http")
		ctx := context.WithValue(r.Context(), apiKeyCtxKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized","detail":"` + jsonEscape(detail) + `"}`))
}

func jsonEscape(s string) string {
	b, _ := jsonMarshal(s)
	return strings.Trim(string(b), `"`)
}

// Serve runs the MCP HTTP server (blocking).
func Serve(deps Deps) error {
	addr := deps.Settings.MCPHost + ":" + itoa(deps.Settings.MCPPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           HTTPHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("mcp server listening", "addr", addr)
	return srv.ListenAndServe()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
