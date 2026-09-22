package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// collectionOut is the /v1/collections item shape (collections.py parity).
type collectionOut struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	EmbeddingModel string    `json:"embedding_model"`
	VectorDim      int       `json:"vector_dim"`
	CreatedAt      time.Time `json:"created_at"`
}

type collectionCreate struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	EmbeddingModel string `json:"embedding_model"`
	VectorDim      int    `json:"vector_dim"`
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.deps.DB.ListCollections(r.Context())
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]collectionOut, 0, len(cols))
	for _, c := range cols {
		out = append(out, collectionOut{
			ID: c.ID, Name: c.Name, EmbeddingModel: c.EmbeddingModel,
			VectorDim: c.VectorDim, CreatedAt: c.CreatedAt,
		})
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
	if body.EmbeddingModel == "" {
		body.EmbeddingModel = "BAAI/bge-m3"
	}
	if body.VectorDim == 0 {
		body.VectorDim = 1024
	}
	if existing, err := s.deps.DB.GetCollection(r.Context(), body.ID); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		writeDetail(w, http.StatusConflict, "collection id already exists")
		return
	}
	col, err := s.deps.DB.InsertCollection(r.Context(), body.ID, body.Name,
		body.EmbeddingModel, body.VectorDim)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, collectionOut{
		ID: col.ID, Name: col.Name, EmbeddingModel: col.EmbeddingModel,
		VectorDim: col.VectorDim, CreatedAt: col.CreatedAt,
	})
}

// collectionStatsOut mirrors collections.py collection_stats.
type collectionStatsOut struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	EmbeddingModel string `json:"embedding_model"`
	DocCount       int    `json:"doc_count"`
	ChunkCount     int    `json:"chunk_count"`
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
	WriteJSON(w, http.StatusOK, collectionStatsOut{
		ID: col.ID, Name: col.Name, EmbeddingModel: col.EmbeddingModel,
		DocCount: docCount, ChunkCount: chunkCount,
	})
}
