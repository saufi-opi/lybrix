package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/store"
)

// RerankStatus is the rerank stage's outcome, carried to callers (REST
// header / MCP note / events): applied = hits reordered by the reranker;
// off = no reranker resolved; degraded = rerank attempted but failed (RRF
// order kept — a rerank failure must never 500 a search).
type RerankStatus struct {
	Applied bool
	Reason  string // "" when applied; "off" or the error detail otherwise
}

// Degraded reports whether the stage ran and failed.
func (s RerankStatus) Degraded() bool { return !s.Applied && s.Reason != "" && s.Reason != "off" }

// Typed resolution-rule failures: the API maps them to 404 / 422; MCP
// surfaces them as tool errors the model can self-correct.
var (
	ErrRerankModelUnknown  = errors.New("rerank model not found")
	ErrRerankNotConfigured = errors.New("no reranker configured")
)

// RerankResolveRequest is the rerank resolution rule's input (shared by the
// REST and MCP surfaces): which collections are in play, and what the
// request explicitly asked for.
type RerankResolveRequest struct {
	SingleCollection string // non-empty for the single-collection path
	Rerank           *bool  // nil = unspecified
	RerankModelID    string // explicit override
}

// ResolveRerankModel applies the plan §1e resolution rule against the DB:
//  1. explicit rerank_model_id → that model (unknown → ErrRerankModelUnknown)
//  2. single-collection: the collection's bound reranker
//  3. multi-collection: binding is ambiguous — only an explicit override
//  4. rerank:false disables; rerank:true with no resolved model →
//     ErrRerankNotConfigured
func ResolveRerankModel(ctx context.Context, db *store.DB, req RerankResolveRequest) (*store.RerankModel, error) {
	if req.Rerank != nil && !*req.Rerank {
		return nil, nil // explicit off wins over any binding
	}
	if req.RerankModelID != "" {
		m, err := db.GetRerankModel(ctx, req.RerankModelID)
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, ErrRerankModelUnknown
		}
		return m, nil
	}
	if req.SingleCollection != "" {
		m, err := db.ResolveCollectionReranker(ctx, req.SingleCollection)
		if err != nil {
			return nil, err
		}
		if m == nil {
			if req.Rerank != nil && *req.Rerank {
				return nil, ErrRerankNotConfigured
			}
			return nil, nil
		}
		return m, nil
	}
	// multi-collection (or unspecified-all): only an explicit override
	// reranks — an inherited binding is ambiguous across collections.
	if req.Rerank != nil && *req.Rerank {
		return nil, ErrRerankNotConfigured
	}
	return nil, nil
}

// RerankHits scores query×text pairs and returns the reordered top hits.
// hits are the candidate pool (already RRF-ordered); returns up to topK.
// Reranked hits carry the normalized rerank score (0..1) in Score with
// Reranked=true so callers/UI can distinguish them from RRF scores.
func RerankHits(ctx context.Context, rc *pipeline.RerankClient, query string, hits []*store.SearchHit, topK int) ([]*store.SearchHit, RerankStatus, error) {
	if len(hits) == 0 || topK < 1 {
		return hits, RerankStatus{Applied: false, Reason: "off"}, nil
	}
	if topK > len(hits) {
		topK = len(hits)
	}
	texts := make([]string, len(hits))
	for i, h := range hits {
		texts[i] = h.Text
	}
	scores, err := rc.Rerank(ctx, query, texts)
	if err != nil {
		// graceful degradation (WeKnora behavior): keep the RRF-ordered
		// pool truncated to topK — never fail the search.
		return hits[:topK], RerankStatus{Applied: false, Reason: err.Error()}, nil
	}
	type scored struct {
		hit   *store.SearchHit
		score float64
	}
	rows := make([]scored, len(hits))
	for i, h := range hits {
		s := 0.0
		if i < len(scores) {
			s = scores[i]
		}
		rows[i] = scored{hit: h, score: s}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	if len(rows) > topK {
		rows = rows[:topK]
	}
	out := make([]*store.SearchHit, 0, len(rows))
	for _, r := range rows {
		r.hit.Score = r.score
		r.hit.Reranked = true
		out = append(out, r.hit)
	}
	return out, RerankStatus{Applied: true}, nil
}

// RerankNote appends the degraded-rerank note to an MCP result payload.
func RerankNote(reason string) string {
	return fmt.Sprintf("rerank not applied: %s (results are RRF order)", reason)
}
