package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/saufi-opi/lybrix/internal/store"
)

// searchRequest mirrors OpenAPI SearchRequest. collection (single) is the
// legacy field; collections (plural) searches multiple collections at once
// — grouped by their bound embedding model and fused with RRF (2.0.2).
// Both omitted → all collections.
type searchRequest struct {
	Query       string   `json:"query"`
	Collection  *string  `json:"collection"`
	Collections []string `json:"collections"`
	TopK        int      `json:"top_k"`
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
		hits, err = s.deps.HybridSearch(r.Context(), vec, model.VectorDim, targets[0], scope, body.Query, topK)
	} else {
		hits, err = s.deps.MultiSearch(r.Context(), body.Query, targets, scope, topK,
			func(m *store.EmbeddingModel) ([]float32, error) {
				return s.deps.EmbedForModel(r.Context(), m, body.Query)
			})
	}
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}

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
		})
	}
	WriteJSON(w, http.StatusOK, out)
}
