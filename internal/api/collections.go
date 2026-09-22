package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saufi-opi/lybrix/internal/store"
)

// collectionOut is the /v1/collections item shape (collections.py parity).
// embedding_model_id carries the registry binding (nil on legacy rows);
// rerank_model_id carries the optional reranker binding (Workstream 1).
type collectionOut struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	EmbeddingModel   string    `json:"embedding_model"`
	VectorDim        int       `json:"vector_dim"`
	EmbeddingModelID *string   `json:"embedding_model_id"`
	RerankModelID    *string   `json:"rerank_model_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// collectionCreate mirrors OpenAPI CollectionCreate — embedding_model_id is
// REQUIRED and names the registry row the collection binds to. The legacy
// free-text embedding_model / vector_dim literals are gone: the collection
// inherits both from the bound model row.
type collectionCreate struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	EmbeddingModelID string `json:"embedding_model_id"`
}

func toCollectionOut(c *store.Collection) collectionOut {
	return collectionOut{
		ID: c.ID, Name: c.Name, EmbeddingModel: c.EmbeddingModel,
		VectorDim: c.VectorDim, EmbeddingModelID: c.EmbeddingModelID,
		RerankModelID: c.RerankModelID,
		CreatedAt:     c.CreatedAt,
	}
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.deps.DB.ListCollections(r.Context())
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]collectionOut, 0, len(cols))
	for _, c := range cols {
		out = append(out, toCollectionOut(c))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var body collectionCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.ID == "" || len(body.ID) > 64 {
		writeDetail(w, http.StatusUnprocessableEntity,
			"id must be 1-64 characters")
		return
	}
	if body.Name == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: name")
		return
	}
	// MANDATORY binding at creation — no default-model substitution. Absent
	// field → 422; unknown model id → 404 (decision 2).
	if body.EmbeddingModelID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: embedding_model_id")
		return
	}
	m, err := s.deps.DB.GetEmbeddingModel(r.Context(), body.EmbeddingModelID)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeDetail(w, http.StatusNotFound, "embedding model not found")
		return
	}
	if existing, err := s.deps.DB.GetCollection(r.Context(), body.ID); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		writeDetail(w, http.StatusConflict, "collection id already exists")
		return
	}
	// Legacy columns inherit FROM the bound model row; the dim's HNSW
	// index is provisioned inside InsertCollection.
	col, err := s.deps.DB.InsertCollection(r.Context(), body.ID, body.Name, m)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, toCollectionOut(col))
}

// collectionStatsOut mirrors collections.py collection_stats. byte_size
// (SUM(documents.byte_size)) and total_shards (SUM(total_shards)) extend the
// readout for the KB gallery cards and the pipeline-load strip (Phase 1).
type collectionStatsOut struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	EmbeddingModel string `json:"embedding_model"`
	DocCount       int    `json:"doc_count"`
	ChunkCount     int    `json:"chunk_count"`
	ByteSize       int64  `json:"byte_size"`
	TotalShards    int64  `json:"total_shards"`
}

func (s *Server) handleCollectionStats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "collection_id")
	col, err := s.deps.DB.GetCollection(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if col == nil {
		writeDetail(w, http.StatusNotFound, "collection not found")
		return
	}
	docCount, chunkCount, err := s.deps.DB.CollectionCounts(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	byteSize, totalShards, err := s.deps.DB.CollectionStatsAgg(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, collectionStatsOut{
		ID: col.ID, Name: col.Name, EmbeddingModel: col.EmbeddingModel,
		DocCount: docCount, ChunkCount: chunkCount,
		ByteSize: byteSize, TotalShards: totalShards,
	})
}

// handleListCollectionChunks: KB-wide chunk browsing (Phase 3 Tab 2).
// Key-scope rule: check collection_id against the key's allowlist BEFORE
// querying — 403 if out of scope, 404 if unknown.
func (s *Server) handleListCollectionChunks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "collection_id")
	col, err := s.deps.DB.GetCollection(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if col == nil {
		writeDetail(w, http.StatusNotFound, "collection not found")
		return
	}
	if !keyAllowedCollection(w, KeyFromContext(r.Context()), &col.ID) {
		return
	}
	var docID *string
	if v := r.URL.Query().Get("doc_id"); v != "" {
		docID = &v
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	chunks, total, err := s.deps.DB.ListCollectionChunks(r.Context(), id, docID, page, pageSize)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]chunkOut, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, toChunkOut(c))
	}
	WriteJSON(w, http.StatusOK, chunkListOut{
		Total: total, Page: page, PageSize: pageSize, Chunks: out,
	})
}
