package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/saufi-opi/lybrix/internal/service"
	"github.com/saufi-opi/lybrix/internal/store"
)

// searchRequest mirrors OpenAPI SearchRequest. collection (single) is the
// legacy field; collections (plural) searches multiple collections at once
// — grouped by their bound embedding model and fused with RRF (2.0.2).
// Both omitted → all collections. rerank/rerank_model_id drive the optional
// cross-encoder stage (Workstream 1); metadata_filter drives the document
// predicate pushdown (Workstream 4).
type searchRequest struct {
	Query          string         `json:"query"`
	Collection     *string        `json:"collection"`
	Collections    []string       `json:"collections"`
	TopK           int            `json:"top_k"`
	Rerank         *bool          `json:"rerank"`
	RerankModelID  *string        `json:"rerank_model_id"`
	MetadataFilter map[string]any `json:"metadata_filter"`
}

// searchHit is one /v1/search response item (search.py mapping).
type searchHit struct {
	ChunkID     string   `json:"chunk_id"`
	DocID       string   `json:"doc_id"`
	DocTitle    *string  `json:"doc_title"`
	PageStart   *int     `json:"page_start"`
	PageEnd     *int     `json:"page_end"`
	HeadingPath []string `json:"heading_path"`
	Text        string   `json:"text"`
	Score       float64  `json:"score"`
	Partial     bool     `json:"partial"`
	Reranked    bool     `json:"reranked,omitempty"`
}

// RerankResolveRequest is the rerank resolution rule's input (shared by the
// REST and MCP surfaces): which collections are in play, and what the
// request explicitly asked for. Type lives in internal/service.
type RerankResolveRequest = service.RerankResolveRequest

// ResolveRerankModel applies the plan §1e resolution rule against the DB
// (service.ResolveRerankModel): explicit rerank_model_id > single-
// collection binding > none; rerank:false disables, rerank:true without a
// resolved model errors. Shared by the REST and MCP surfaces — production
// wiring injects it as Deps.ResolveReranker; tests stub it.
var ResolveRerankModel = service.ResolveRerankModel

var errRerankModelUnknown = service.ErrRerankModelUnknown

// singleTarget extracts the single-collection target when exactly one is
// named ("" otherwise) — the rerank resolution rule's input.
func singleTarget(targets []string) string {
	if len(targets) == 1 {
		return targets[0]
	}
	return ""
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body searchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: query")
		return
	}
	topK := body.TopK
	if topK == 0 {
		topK = s.deps.Settings.SearchDefaultTopK
	}
	if topK < 1 {
		writeDetail(w, http.StatusUnprocessableEntity, "top_k must be >= 1")
		return
	}
	if topK > s.deps.Settings.SearchMaxTopK {
		topK = s.deps.Settings.SearchMaxTopK
	}

	// Workstream 4: compile the metadata filter dialect here (422 on bad
	// keys/values) — the compiled SQL + args are pushed INTO every CTE by
	// the store layer (R-14: never post-filtered).
	metaFilter, metaArgs, ferr := store.BuildMetadataFilter(body.MetadataFilter)
	if ferr != nil {
		writeDetail(w, http.StatusUnprocessableEntity, ferr.Error())
		return
	}

	// key-level scope is pushed INTO the search SQL (R-14) — no post-filter
	// here: filtering after top_k truncation returned fewer than top_k
	// results for scoped keys.
	var scope []string
	if key := KeyFromContext(r.Context()); key != nil && len(key.Collections) > 0 {
		scope = key.Collections
	}

	// Target set: explicit collections > single collection > all. A named
	// single collection keeps the exact single-model path (one dense leg +
	// BM25, the pre-multi-search shape); an empty target set — the default
	// — routes to the grouped multi-model fusion across ALL collections
	// (WeKnora multi-KB: the corpus, not one default model, is the target).
	targets := body.Collections
	if len(targets) == 0 && body.Collection != nil && *body.Collection != "" {
		targets = []string{*body.Collection}
	}

	// Workstream 1e: resolve the rerank stage ONCE up front (explicit
	// override > collection binding > none). A resolved reranker widens the
	// candidate pool to RerankCandidates; the stage runs after fusion.
	var reranker *store.RerankModel
	if s.deps.ResolveReranker != nil {
		rm, rerr := s.deps.ResolveReranker(r.Context(), RerankResolveRequest{
			SingleCollection: singleTarget(targets),
			Rerank:           body.Rerank,
			RerankModelID:    deref(body.RerankModelID),
		})
		switch {
		case rerr == errRerankModelUnknown:
			writeDetail(w, http.StatusNotFound, "rerank model not found")
			return
		case rerr != nil:
			writeDetail(w, http.StatusUnprocessableEntity, rerr.Error())
			return
		case rm != nil:
			reranker = rm
		}
	}

	var (
		hits []*store.SearchHit
		err  error
	)
	if len(targets) == 1 {
		var vec []float32
		var model *store.EmbeddingModel
		vec, model, err = s.deps.EmbedQuery(r.Context(), targets[0], body.Query)
		if err != nil {
			// query embed unavailable → 503 (search.py parity)
			writeDetail(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		poolSize := topK
		if reranker != nil && poolSize < s.deps.Settings.RerankCandidates {
			poolSize = s.deps.Settings.RerankCandidates
		}
		hits, err = s.deps.HybridSearch(r.Context(), vec, model.VectorDim, targets[0], scope, body.Query, poolSize, metaFilter, metaArgs)
	} else {
		hits, err = s.deps.MultiSearch(r.Context(), body.Query, targets, scope, topK, metaFilter, metaArgs,
			func(m *store.EmbeddingModel) ([]float32, error) {
				return s.deps.EmbedForModel(r.Context(), m, body.Query)
			})
	}
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Workstream 1: rerank stage — degrade gracefully (RRF order kept) on
	// any rerank failure; the outcome rides the X-Lybrix-Rerank header.
	rerankState := "off"
	if reranker != nil && s.deps.RerankHits != nil {
		reranked, status, rerr := s.deps.RerankHits(r.Context(), reranker, body.Query, hits, topK)
		switch {
		case rerr != nil:
			// resolution raced a delete: keep RRF order, mark degraded
			rerankState = "degraded"
		default:
			hits = reranked
			if status.Applied {
				rerankState = "applied"
			} else {
				rerankState = "degraded"
			}
		}
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}

	w.Header().Set("X-Lybrix-Rerank", rerankState)
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		hp := h.HeadingPath
		if hp == nil {
			hp = []string{}
		}
		out = append(out, searchHit{
			ChunkID: h.ChunkID, DocID: h.DocID, DocTitle: h.DocTitle,
			PageStart: h.PageStart, PageEnd: h.PageEnd,
			HeadingPath: hp, Text: h.Text, Score: h.Score, Partial: h.Partial,
			Reranked: h.Reranked,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}
