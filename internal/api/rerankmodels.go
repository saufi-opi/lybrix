package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/store"
)

// rerankModelOut mirrors the OpenAPI RerankModelOut. api_key is NEVER in
// this shape — has_api_key carries its presence; the secret is write-only.
// No vector_dim: rerankers score text pairs, not vectors.
type rerankModelOut struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	ModelID       string    `json:"model_id"`
	QueryURL      string    `json:"query_url"`
	HasAPIKey     bool      `json:"has_api_key"`
	TruncateChars int       `json:"truncate_chars"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// rerankModelCreate mirrors OpenAPI RerankModelCreate (POST /v1/rerank-models
// and the dry-run POST /v1/rerank-models/test — api_key optional there).
type rerankModelCreate struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	ModelID       string  `json:"model_id"`
	QueryURL      string  `json:"query_url"`
	APIKey        *string `json:"api_key"`
	TruncateChars int     `json:"truncate_chars"`
}

// rerankModelUpdate mirrors OpenAPI RerankModelUpdate — partial; api_key
// null means unchanged.
type rerankModelUpdate struct {
	Name          *string `json:"name"`
	Provider      *string `json:"provider"`
	ModelID       *string `json:"model_id"`
	QueryURL      *string `json:"query_url"`
	APIKey        *string `json:"api_key"`
	TruncateChars *int    `json:"truncate_chars"`
}

// rerankTestResult mirrors OpenAPI RerankTestResult.
type rerankTestResult struct {
	Reachable bool   `json:"reachable"`
	LatencyMS *int   `json:"latency_ms"`
	Detail    string `json:"detail"`
}

func toRerankModelOut(m *store.RerankModel) rerankModelOut {
	return rerankModelOut{
		ID: m.ID, Name: m.Name, Provider: m.Provider, ModelID: m.ModelID,
		QueryURL: m.QueryURL, HasAPIKey: m.HasAPIKey,
		TruncateChars: m.TruncateChars,
		CreatedAt:     m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// validRerankProvider checks the rerank provider vocabulary.
func validRerankProvider(p string) bool {
	return p == "tei" || p == "openai"
}

// validateRerankCreate enforces the 422 contract: name 1-200, provider
// vocabulary, model_id required, query_url parses as http(s), openai
// requires api_key on create.
func validateRerankCreate(b rerankModelCreate) string {
	if b.Name == "" || len(b.Name) > 200 {
		return "name must be 1-200 characters"
	}
	if !validRerankProvider(b.Provider) {
		return "provider must be one of tei|openai"
	}
	if b.ModelID == "" {
		return "field required: model_id"
	}
	if !validModelURL(b.QueryURL) {
		return "query_url must parse as an http(s) URL"
	}
	if b.TruncateChars < 0 {
		return "truncate_chars must be >= 0"
	}
	if b.Provider == "openai" && (b.APIKey == nil || *b.APIKey == "") {
		return "openai provider requires api_key on create"
	}
	return ""
}

// normalizeRerankCreate fills the registry-row default (truncate_chars).
func normalizeRerankCreate(b *rerankModelCreate) {
	if b.TruncateChars == 0 {
		b.TruncateChars = 6000
	}
}

// newRerankClient builds the query-plane client for one reranker row (the
// registry never stores a raw key, so apiKey arrives from the caller —
// create/test pass the request's key; runtime rerank paths resolve it
// through Deps.ResolveReranker's client building in cmd wiring).
func newRerankClient(provider, modelID, queryURL, apiKey string, truncateChars int) (*pipeline.RerankClient, error) {
	return pipeline.NewRerankClient(provider, modelID, queryURL, apiKey, truncateChars)
}

func (s *Server) handleListRerankModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.deps.DB.ListRerankModels(r.Context())
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]rerankModelOut, 0, len(models))
	for _, m := range models {
		out = append(out, toRerankModelOut(m))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRerankModel(w http.ResponseWriter, r *http.Request) {
	var body rerankModelCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if msg := validateRerankCreate(body); msg != "" {
		writeDetail(w, http.StatusUnprocessableEntity, msg)
		return
	}
	normalizeRerankCreate(&body)
	m := &store.RerankModel{
		Name: body.Name, Provider: body.Provider, ModelID: body.ModelID,
		QueryURL: body.QueryURL, TruncateChars: body.TruncateChars,
	}
	var apiKey *string
	if body.APIKey != nil && *body.APIKey != "" {
		apiKey = body.APIKey
	}
	out, err := s.deps.DB.InsertRerankModel(r.Context(), m, apiKey)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, toRerankModelOut(out))
}

func (s *Server) handleTestRerankModel(w http.ResponseWriter, r *http.Request) {
	var body rerankModelCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	// dry-run probe, no persist: same validation minus the api_key-on-create
	// requirement (the test may run before the operator pastes the key).
	if body.Name == "" || len(body.Name) > 200 {
		writeDetail(w, http.StatusUnprocessableEntity, "name must be 1-200 characters")
		return
	}
	if !validRerankProvider(body.Provider) {
		writeDetail(w, http.StatusUnprocessableEntity, "provider must be one of tei|openai")
		return
	}
	if body.ModelID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: model_id")
		return
	}
	if u, err := url.Parse(body.QueryURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "query_url must parse as an http(s) URL")
		return
	}
	client, err := newRerankClient(body.Provider, body.ModelID, body.QueryURL, deref(body.APIKey), body.TruncateChars)
	if err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	reachable, latencyMS, detail := client.Probe(r.Context())
	out := rerankTestResult{Reachable: reachable, Detail: detail}
	if reachable {
		out.LatencyMS = &latencyMS
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateRerankModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "model_id")
	var body rerankModelUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	existing, err := s.deps.DB.GetRerankModel(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeDetail(w, http.StatusNotFound, "rerank model not found")
		return
	}
	// merge partial over existing
	b := rerankModelCreate{
		Name: existing.Name, Provider: existing.Provider, ModelID: existing.ModelID,
		QueryURL: existing.QueryURL, TruncateChars: existing.TruncateChars,
		APIKey: nil, // api_key omitted → unchanged (COALESCE in the store)
	}
	if body.Name != nil {
		b.Name = *body.Name
	}
	if body.Provider != nil {
		b.Provider = *body.Provider
	}
	if body.ModelID != nil {
		b.ModelID = *body.ModelID
	}
	if body.QueryURL != nil {
		b.QueryURL = *body.QueryURL
	}
	if body.TruncateChars != nil {
		b.TruncateChars = *body.TruncateChars
	}
	if msg := validateRerankCreate(b); msg != "" {
		writeDetail(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// openai update with an explicit new api_key satisfies the create-time
	// key requirement; an existing stored key also satisfies it.
	if b.Provider == "openai" && (b.APIKey == nil || *b.APIKey == "") && !existing.HasAPIKey {
		writeDetail(w, http.StatusUnprocessableEntity, "openai provider requires api_key")
		return
	}
	m := &store.RerankModel{
		Name: b.Name, Provider: b.Provider, ModelID: b.ModelID,
		QueryURL: b.QueryURL, TruncateChars: b.TruncateChars,
	}
	var apiKey *string
	if body.APIKey != nil && *body.APIKey != "" {
		apiKey = body.APIKey
	}
	out, err := s.deps.DB.UpdateRerankModel(r.Context(), id, m, apiKey)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		writeDetail(w, http.StatusNotFound, "rerank model not found")
		return
	}
	WriteJSON(w, http.StatusOK, toRerankModelOut(out))
}

func (s *Server) handleDeleteRerankModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "model_id")
	err := s.deps.DB.DeleteRerankModel(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrRerankModelNotFound):
		writeDetail(w, http.StatusNotFound, "rerank model not found")
	case errors.Is(err, store.ErrRerankModelInUse):
		// ErrRerankModelInUse carries the bound-collection count
		writeDetail(w, http.StatusConflict, "rerank model bound to collection(s): "+strings.TrimPrefix(err.Error(), store.ErrRerankModelInUse.Error()))
	default:
		writeDetail(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleBindCollectionReranker(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collection_id")
	var body struct {
		RerankModelID *string `json:"rerank_model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	// null clears the binding; "" is invalid.
	if body.RerankModelID != nil && *body.RerankModelID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "rerank_model_id must be a model id or null")
		return
	}
	modelID := ""
	if body.RerankModelID != nil {
		modelID = *body.RerankModelID
	}
	col, err := s.deps.DB.BindCollectionReranker(r.Context(), collectionID, modelID)
	if err != nil {
		if err == store.ErrRerankModelNotFound {
			writeDetail(w, http.StatusNotFound, "rerank model not found")
			return
		}
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if col == nil {
		writeDetail(w, http.StatusNotFound, "collection not found")
		return
	}
	WriteJSON(w, http.StatusOK, collectionOut{
		ID: col.ID, Name: col.Name, EmbeddingModel: col.EmbeddingModel,
		VectorDim: col.VectorDim, CreatedAt: col.CreatedAt,
	})
}
