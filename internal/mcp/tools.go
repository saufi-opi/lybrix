package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/store"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// clampTopK enforces 1 <= top_k <= SEARCH_MAX_TOP_K (server.py _clamp_top_k).
func clampTopK(s *config.Settings, topK int) (int, error) {
	if topK < 1 {
		return 0, fmt.Errorf("top_k must be >= 1")
	}
	if topK > s.SearchMaxTopK {
		return s.SearchMaxTopK, nil
	}
	return topK, nil
}

// partialNote is the fixed note attached when a matched document has
// completeness < 1.0.
const partialNote = "source document parsed with holes (completeness < 1.0); " +
	"nearby pages may be missing"

// handleSearch is the business logic behind the `search` tool —
// unit-testable against DB fixtures (search_impl parity).
func handleSearch(ctx context.Context, deps Deps, args map[string]any) (any, error) {
	query, err := argString(args, "query")
	if err != nil || strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	topK := 8
	if v, ok := argInt(args, "top_k"); ok {
		topK = v
	}
	limit, err := clampTopK(deps.Settings, topK)
	if err != nil {
		return nil, err
	}
	collection := argStringOrEmpty(args, "collection")

	key := KeyFromContext(ctx)
	if key == nil {
		return nil, fmt.Errorf("search called without an authenticated key")
	}
	// key-scoped collections: passing a collection outside the key's scope
	// is an error, not a filter (assert_collection_allowed).
	if !store.CollectionAllowed(key, collection) {
		return nil, fmt.Errorf("key is not scoped to collection %q", collection)
	}
	var scope []string
	if len(key.Collections) > 0 {
		scope = key.Collections
	}
	vec, model, err := deps.EmbedQuery(ctx, collection, query)
	if err != nil {
		return nil, fmt.Errorf("query embedding unavailable: %s", err.Error())
	}
	hits, err := deps.DB.HybridSearch(ctx, vec, model.VectorDim, collection, scope, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		item := map[string]any{
			"doc_title":    h.DocTitle,
			"page_start":   h.PageStart,
			"page_end":     h.PageEnd,
			"heading_path": h.HeadingPath,
			"text":         h.Text,
			"score":        h.Score,
			"doc_id":       h.DocID,
			"chunk_id":     h.ChunkID,
		}
		if h.Partial {
			item["partial"] = true
			item["note"] = partialNote
		}
		out = append(out, item)
	}
	return out, nil
}

// handleGetChunkContext is the get_chunk_context impl (±window by seq).
func handleGetChunkContext(ctx context.Context, deps Deps, args map[string]any) (any, error) {
	chunkID, err := argString(args, "chunk_id")
	if err != nil {
		return nil, fmt.Errorf("chunk_id is required")
	}
	window := 2
	if v, ok := argInt(args, "window"); ok {
		window = v
	}
	chunk, err := deps.DB.GetChunk(ctx, chunkID)
	if err != nil {
		return nil, err
	}
	if chunk == nil {
		return nil, fmt.Errorf("chunk %s not found", chunkID)
	}
	rows, err := deps.DB.ChunkNeighbours(ctx, chunk.DocID, chunk.Seq, window)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"chunk_id":   r.ID,
			"seq":        r.Seq,
			"page_start": r.PageStart,
			"page_end":   r.PageEnd,
			"text":       r.Text,
		})
	}
	return out, nil
}

// handleReadPages is the read_pages impl: bounded, capped at READ_PAGES_MAX.
func handleReadPages(ctx context.Context, deps Deps, args map[string]any) (any, error) {
	docID, err := argString(args, "doc_id")
	if err != nil {
		return nil, fmt.Errorf("doc_id is required")
	}
	pageStart := argIntOr(args, "page_start", 1)
	pageEnd := argIntOr(args, "page_end", pageStart)
	doc, err := deps.DB.GetDocument(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("document %s not found", docID)
	}
	if pageStart < 1 {
		pageStart = 1
	}
	pageEnd = min(pageEnd, pageStart+deps.Settings.ReadPagesMax-1)
	if doc.PageCount != nil {
		pageEnd = min(pageEnd, *doc.PageCount)
	}
	rows, err := deps.DB.ReadPageChunks(ctx, docID, pageStart, pageEnd)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, r.Text)
	}
	return map[string]any{
		"doc_title":    doc.Title,
		"page_start":   pageStart,
		"page_end":     pageEnd,
		"markdown":     strings.Join(parts, "\n\n"),
		"truncated_to": deps.Settings.ReadPagesMax,
	}, nil
}

// handleListDocuments is the list_documents impl (limit capped at 200).
func handleListDocuments(ctx context.Context, deps Deps, args map[string]any) (any, error) {
	limit := argIntOr(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	collection := argStringPtr(args, "collection")
	state := argStringPtr(args, "state")
	query := argStringPtr(args, "query")
	var statePtr *store.DocState
	if state != nil && *state != "" {
		st := store.DocState(*state)
		statePtr = &st
	}
	docs, err := deps.DB.ListDocuments(ctx, statePtr, collection, query, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, map[string]any{
			"id":           d.ID,
			"title":        d.Title,
			"author":       d.Author,
			"page_count":   d.PageCount,
			"state":        string(d.State),
			"completeness": d.Completeness,
		})
	}
	return out, nil
}

// handleGetDocument is the get_document impl (metadata + chunk_count).
func handleGetDocument(ctx context.Context, deps Deps, args map[string]any) (any, error) {
	docID, err := argString(args, "doc_id")
	if err != nil {
		return nil, fmt.Errorf("doc_id is required")
	}
	doc, err := deps.DB.GetDocument(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, nil
	}
	chunkCount, err := deps.DB.CountDocChunks(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":           doc.ID,
		"title":        doc.Title,
		"author":       doc.Author,
		"state":        string(doc.State),
		"page_count":   doc.PageCount,
		"completeness": doc.Completeness,
		"chunk_count":  chunkCount,
		"metadata":     doc.Metadata,
	}, nil
}

// handleListCollections is the list_collections impl (doc_count included).
func handleListCollections(ctx context.Context, deps Deps, _ map[string]any) (any, error) {
	cols, err := deps.DB.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := deps.DB.MCPDocCountPerCollection(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(cols))
	for _, c := range cols {
		out = append(out, map[string]any{
			"id":              c.ID,
			"name":            c.Name,
			"embedding_model": c.EmbeddingModel,
			"doc_count":       counts[c.ID],
		})
	}
	return out, nil
}

// --- argument helpers ------------------------------------------------------

func argString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing argument: %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}
	return s, nil
}

func argStringPtr(args map[string]any, key string) *string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

func argStringOrEmpty(args map[string]any, key string) string {
	if s := argStringPtr(args, key); s != nil {
		return *s
	}
	return ""
}

func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

func argIntOr(args map[string]any, key string, def int) int {
	if v, ok := argInt(args, key); ok {
		return v
	}
	return def
}

var (
	_ = sort.Strings
	_ = fmt.Sprintf
)
