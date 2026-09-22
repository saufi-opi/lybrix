package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/store"
)

// modelOut mirrors OpenAPI ModelOut. api_key is NEVER in this shape —
// has_api_key carries its presence; the secret is write-only. There is no
// is_default — the registry is a pure catalog (2.0.2).
type modelOut struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	ModelID       string    `json:"model_id"`
	IngestURL     string    `json:"ingest_url"`
	QueryURL      string    `json:"query_url"`
	HasAPIKey     bool      `json:"has_api_key"`
	VectorDim     int       `json:"vector_dim"`
	QueryPrefix   string    `json:"query_prefix"`
	BatchSize     int       `json:"batch_size"`
	CtxBudget     int       `json:"ctx_budget"`
	TruncateChars int       `json:"truncate_chars"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// modelCreate mirrors OpenAPI ModelCreate (POST /v1/models and the
// dry-run POST /v1/models/test — api_key optional there).
type modelCreate struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	ModelID       string  `json:"model_id"`
	IngestURL     string  `json:"ingest_url"`
	QueryURL      string  `json:"query_url"`
	APIKey        *string `json:"api_key"`
	VectorDim     int     `json:"vector_dim"`
	QueryPrefix   string  `json:"query_prefix"`
	BatchSize     int     `json:"batch_size"`
	CtxBudget     int     `json:"ctx_budget"`
	TruncateChars int     `json:"truncate_chars"`
}

// modelUpdate mirrors OpenAPI ModelUpdate — partial; api_key null means
// unchanged.
type modelUpdate struct {
	Name          *string `json:"name"`
	Provider      *string `json:"provider"`
	ModelID       *string `json:"model_id"`
	IngestURL     *string `json:"ingest_url"`
	QueryURL      *string `json:"query_url"`
	APIKey        *string `json:"api_key"`
	VectorDim     *int    `json:"vector_dim"`
	QueryPrefix   *string `json:"query_prefix"`
	BatchSize     *int    `json:"batch_size"`
	CtxBudget     *int    `json:"ctx_budget"`
	TruncateChars *int    `json:"truncate_chars"`
}

// modelTestResult mirrors OpenAPI ModelTestResult.
type modelTestResult struct {
	Reachable bool   `json:"reachable"`
	LatencyMS *int   `json:"latency_ms"`
	Detail    string `json:"detail"`
	VectorDim *int   `json:"vector_dim"`
}

func toModelOut(m *store.EmbeddingModel) modelOut {
	return modelOut{
		ID: m.ID, Name: m.Name, Provider: m.Provider, ModelID: m.ModelID,
		IngestURL: m.IngestURL, QueryURL: m.QueryURL, HasAPIKey: m.HasAPIKey,
		VectorDim: m.VectorDim, QueryPrefix: m.QueryPrefix,
		BatchSize: m.BatchSize, CtxBudget: m.CtxBudget,
		TruncateChars: m.TruncateChars,
		CreatedAt:     m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// validModelProvider checks the v1 provider vocabulary.
func validModelProvider(p string) bool {
	return p == "tei" || p == "ollama" || p == "openai"
}

// validModelURL requires an http(s) URL that parses.
func validModelURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validateModelCreate enforces the 422 contract: name 1-200, provider
// vocabulary, model_id required, URLs parse as http(s), vector_dim 1-2000,
// openai requires api_key on create.
func validateModelCreate(b modelCreate) string {
	if b.Name == "" || len(b.Name) > 200 {
		return "name must be 1-200 characters"
	}
	if !validModelProvider(b.Provider) {
		return "provider must be one of tei|ollama|openai"
	}
	if b.ModelID == "" {
		return "field required: model_id"
	}
	if !validModelURL(b.IngestURL) {
		return "ingest_url must parse as an http(s) URL"
	}
	if !validModelURL(b.QueryURL) {
		return "query_url must parse as an http(s) URL"
	}
	if b.VectorDim < 1 || b.VectorDim > 2000 {
		return "vector_dim must be between 1 and 2000"
	}
	if b.Provider == "openai" && (b.APIKey == nil || *b.APIKey == "") {
		return "openai provider requires api_key on create"
	}
	return ""
}

// normalizeCreate fills the registry-row defaults (query_prefix / batch /
// budget / truncate live on the model row since the env retirement).
func normalizeCreate(b *modelCreate) {
	if b.QueryPrefix == "" {
		b.QueryPrefix = "search_query: "
	}
	if b.BatchSize == 0 {
		b.BatchSize = 48
	}
	if b.CtxBudget == 0 {
		b.CtxBudget = 1900
	}
	if b.TruncateChars == 0 {
		b.TruncateChars = 6000
	}
}

// confirmDim is the save-time dim confirmation: when the probe succeeds and
// detects a different true output dim than declared, save is refused with
// 422 — the operator must either correct vector_dim or run
// POST /v1/models/test to see the real dim. Probe unreachable → proceed
// (the runtime expectedDim guard catches it later).
func confirmDim(ctx context.Context, b modelCreate) (string, *int) {
	client, err := pipeline.NewEmbedClient(pipeline.EmbedSpec{
		Provider: b.Provider, ModelID: b.ModelID,
		IngestURL: b.IngestURL, QueryURL: b.QueryURL,
		APIKey: deref(b.APIKey),
	}, "query")
	if err != nil {
		return "", nil
	}
	reachable, _, detected, detail := client.Probe(ctx)
	if !reachable {
		return "", nil
	}
	if detected > 0 && detected != b.VectorDim {
		return fmt.Sprintf("declared vector_dim (%d) does not match the model's actual output dimension (%d); confirm with POST /v1/models/test", b.VectorDim, detected), &detected
	}
	_ = detail
	return "", nil
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.deps.DB.ListEmbeddingModels(r.Context())
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]modelOut, 0, len(models))
	for _, m := range models {
		out = append(out, toModelOut(m))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var body modelCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if msg := validateModelCreate(body); msg != "" {
		writeDetail(w, http.StatusUnprocessableEntity, msg)
		return
	}
	normalizeCreate(&body)
	if detail, detected := confirmDim(r.Context(), body); detail != "" {
		writeDetail(w, http.StatusUnprocessableEntity, detail)
		_ = detected
		return
	}
	m := &store.EmbeddingModel{
		Name: body.Name, Provider: body.Provider, ModelID: body.ModelID,
		IngestURL: body.IngestURL, QueryURL: body.QueryURL,
		VectorDim: body.VectorDim, QueryPrefix: body.QueryPrefix,
		BatchSize: body.BatchSize, CtxBudget: body.CtxBudget,
		TruncateChars: body.TruncateChars,
	}
	var apiKey *string
	if body.APIKey != nil && *body.APIKey != "" {
		apiKey = body.APIKey
	}
	out, err := s.deps.DB.InsertEmbeddingModel(r.Context(), m, apiKey)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.deps.DB.WriteEventPool(r.Context(), "info", "models",
		"embedding model '"+body.Name+"' registered ("+body.Provider+"/"+body.ModelID+", dim "+
			fmt.Sprint(out.VectorDim)+")",
		nil, nil, strPtr("created"), nil, map[string]any{"model_id": out.ID, "name": body.Name})
	WriteJSON(w, http.StatusCreated, toModelOut(out))
}

func (s *Server) handleTestModel(w http.ResponseWriter, r *http.Request) {
	var body modelCreate
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
	if !validModelProvider(body.Provider) {
		writeDetail(w, http.StatusUnprocessableEntity, "provider must be one of tei|ollama|openai")
		return
	}
	if body.ModelID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: model_id")
		return
	}
	client, err := pipeline.NewEmbedClient(pipeline.EmbedSpec{
		Provider: body.Provider, ModelID: body.ModelID,
		IngestURL: body.IngestURL, QueryURL: body.QueryURL,
		APIKey: deref(body.APIKey),
	}, "query")
	if err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	reachable, latencyMS, detected, detail := client.Probe(r.Context())
	out := modelTestResult{Reachable: reachable, Detail: detail}
	if reachable {
		out.LatencyMS = &latencyMS
	}
	if detected > 0 {
		d := detected
		out.VectorDim = &d
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "model_id")
	var body modelUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	existing, err := s.deps.DB.GetEmbeddingModel(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeDetail(w, http.StatusNotFound, "model not found")
		return
	}
	// merge partial over existing
	b := modelCreate{
		Name: existing.Name, Provider: existing.Provider, ModelID: existing.ModelID,
		IngestURL: existing.IngestURL, QueryURL: existing.QueryURL,
		VectorDim: existing.VectorDim, QueryPrefix: existing.QueryPrefix,
		BatchSize: existing.BatchSize, CtxBudget: existing.CtxBudget,
		TruncateChars: existing.TruncateChars,
		APIKey:        nil, // api_key omitted → unchanged (COALESCE in the store)
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
	if body.IngestURL != nil {
		b.IngestURL = *body.IngestURL
	}
	if body.QueryURL != nil {
		b.QueryURL = *body.QueryURL
	}
	if body.VectorDim != nil {
		b.VectorDim = *body.VectorDim
	}
	if body.QueryPrefix != nil {
		b.QueryPrefix = *body.QueryPrefix
	}
	if body.BatchSize != nil {
		b.BatchSize = *body.BatchSize
	}
	if body.CtxBudget != nil {
		b.CtxBudget = *body.CtxBudget
	}
	if body.TruncateChars != nil {
		b.TruncateChars = *body.TruncateChars
	}
	if msg := validateModelCreate(b); msg != "" {
		writeDetail(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// openai update with an explicit new api_key satisfies the create-time
	// key requirement; an existing stored key also satisfies it.
	if b.Provider == "openai" && (b.APIKey == nil || *b.APIKey == "") && !existing.HasAPIKey {
		writeDetail(w, http.StatusUnprocessableEntity, "openai provider requires api_key")
		return
	}
	if detail, detected := confirmDim(r.Context(), b); detail != "" {
		writeDetail(w, http.StatusUnprocessableEntity, detail)
		_ = detected
		return
	}
	m := &store.EmbeddingModel{
		Name: b.Name, Provider: b.Provider, ModelID: b.ModelID,
		IngestURL: b.IngestURL, QueryURL: b.QueryURL,
		VectorDim: b.VectorDim, QueryPrefix: b.QueryPrefix,
		BatchSize: b.BatchSize, CtxBudget: b.CtxBudget,
		TruncateChars: b.TruncateChars,
	}
	var apiKey *string
	if body.APIKey != nil && *body.APIKey != "" {
		apiKey = body.APIKey
	}
	out, err := s.deps.DB.UpdateEmbeddingModel(r.Context(), id, m, apiKey)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		writeDetail(w, http.StatusNotFound, "model not found")
		return
	}
	WriteJSON(w, http.StatusOK, toModelOut(out))
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "model_id")
	err := s.deps.DB.DeleteEmbeddingModel(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrModelNotFound):
		writeDetail(w, http.StatusNotFound, "model not found")
	case errors.Is(err, store.ErrModelInUse):
		// ErrModelInUse carries the bound-collection count (fmt-wrapped)
		writeDetail(w, http.StatusConflict, "model bound to collection(s): "+strings.TrimPrefix(err.Error(), store.ErrModelInUse.Error()))
	default:
		writeDetail(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleBindCollectionModel(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collection_id")
	var body struct {
		EmbeddingModelID string `json:"embedding_model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.EmbeddingModelID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: embedding_model_id")
		return
	}
	col, err := s.deps.DB.BindCollectionModel(r.Context(), collectionID, body.EmbeddingModelID)
	if err != nil {
		if err == store.ErrModelNotFound {
			writeDetail(w, http.StatusNotFound, "embedding model not found")
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
