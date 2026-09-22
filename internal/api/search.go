package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// searchRequest mirrors OpenAPI SearchRequest.
type searchRequest struct {
	Query      string  `json:"query"`
	Collection *string `json:"collection"`
	TopK       int     `json:"top_k"`
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

	collection := ""
	if body.Collection != nil {
		collection = *body.Collection
	}
	// key-level scope is pushed INTO the search SQL (R-14) — no post-filter
	// here: filtering after top_k truncation returned fewer than top_k
	// results for scoped keys.
	var scope []string
	if key := KeyFromContext(r.Context()); key != nil && len(key.Collections) > 0 {
		scope = key.Collections
	}

	vec, model, err := s.deps.EmbedQuery(r.Context(), collection, body.Query)
	if err != nil {
		// query embed unavailable → 503 (search.py parity)
		writeDetail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	bm25 := body.Query
	hits, err := s.deps.DB.HybridSearch(r.Context(), vec, model.VectorDim, collection, scope, bm25, topK)
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
