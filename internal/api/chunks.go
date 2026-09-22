package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saufi-opi/lybrix/internal/store"
)

// chunkOut mirrors the OpenAPI ChunkOut — the chunkCols projection the
// chunk inspector renders. Parent rows (is_parent) carry breadcrumb text
// and no embedding; children carry the embedded text.
type chunkOut struct {
	ID               string     `json:"id"`
	Seq              int        `json:"seq"`
	IsParent         bool       `json:"is_parent"`
	ParentID         *string    `json:"parent_id"`
	PageStart        *int       `json:"page_start"`
	PageEnd          *int       `json:"page_end"`
	HeadingPath      []string   `json:"heading_path"`
	HeaderBreadcrumb *string    `json:"header_breadcrumb"`
	Text             string     `json:"text"`
	TokenCount       int        `json:"token_count"`
	EmbeddedAt       *time.Time `json:"embedded_at"`
}

func toChunkOut(c *store.Chunk) chunkOut {
	hp := c.HeadingPath
	if hp == nil {
		hp = []string{}
	}
	return chunkOut{
		ID: c.ID, Seq: c.Seq, IsParent: c.IsParent, ParentID: c.ParentID,
		PageStart: c.PageStart, PageEnd: c.PageEnd, HeadingPath: hp,
		HeaderBreadcrumb: c.HeaderBreadcrumb, Text: c.Text,
		TokenCount: c.TokenCount, EmbeddedAt: c.EmbeddedAt,
	}
}

// chunkListOut is the paged /v1/documents/{doc_id}/chunks payload.
type chunkListOut struct {
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Chunks   []chunkOut `json:"chunks"`
}

// chunkDetailOut is the /v1/chunks/{chunk_id} payload — chunk + parent text
// + neighbour ids (preview_chunk's REST twin).
type chunkDetailOut struct {
	chunkOut
	ParentText string     `json:"parent_text"`
	PrevID     *string    `json:"prev_id"`
	NextID     *string    `json:"next_id"`
	Neighbours []chunkOut `json:"neighbours"`
}

// keyScopeChunks enforces the key's collection allowlist at the handler
// layer (doc → collection → allowlist, mirroring handleGetDocument's
// access pattern). Unscoped keys pass everything.
func (s *Server) keyScopeChunks(w http.ResponseWriter, r *http.Request, docID string) bool {
	key := KeyFromContext(r.Context())
	if key == nil || len(key.Collections) == 0 {
		return true
	}
	doc, err := s.deps.DB.GetDocument(r.Context(), docID)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if doc == nil {
		writeDetail(w, http.StatusNotFound, "document not found")
		return false
	}
	if doc.CollectionID != nil {
		for _, c := range key.Collections {
			if c == *doc.CollectionID {
				return true
			}
		}
	}
	writeDetail(w, http.StatusForbidden, "key is not scoped to this document's collection")
	return false
}

// keyAllowedCollection is the collection-id form of the allowlist guard —
// used by the collection-chunk listing (404 unknown checked separately).
// Unscoped keys pass everything; writes the 403 itself.
func keyAllowedCollection(w http.ResponseWriter, key *store.ApiKey, collectionID *string) bool {
	if key == nil || len(key.Collections) == 0 {
		return true
	}
	if collectionID != nil && store.CollectionAllowed(key, *collectionID) {
		return true
	}
	writeDetail(w, http.StatusForbidden, "key is not scoped to this collection")
	return false
}

func (s *Server) handleListDocChunks(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "doc_id")
	if !s.keyScopeChunks(w, r, docID) {
		return
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	chunks, total, err := s.deps.DB.ListDocChunks(r.Context(), docID, page, pageSize)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	out := make([]chunkOut, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, toChunkOut(c))
	}
	WriteJSON(w, http.StatusOK, chunkListOut{
		Total: total, Page: page, PageSize: pageSize, Chunks: out,
	})
}

func (s *Server) handleGetChunk(w http.ResponseWriter, r *http.Request) {
	chunkID := chi.URLParam(r, "chunk_id")
	chunk, parent, err := s.deps.DB.GetChunkWithParent(r.Context(), chunkID)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if chunk == nil {
		writeDetail(w, http.StatusNotFound, "chunk not found")
		return
	}
	if !s.keyScopeChunks(w, r, chunk.DocID) {
		return
	}
	// ±1 neighbours in document order — the expandable row's prev/next.
	neighbours, err := s.deps.DB.ChunkNeighbours(r.Context(), chunk.DocID, chunk.Seq, 1)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	var prevID, nextID *string
	nb := make([]chunkOut, 0, len(neighbours))
	for _, n := range neighbours {
		nb = append(nb, toChunkOut(n))
		if n.Seq == chunk.Seq-1 {
			id := n.ID
			prevID = &id
		}
		if n.Seq == chunk.Seq+1 {
			id := n.ID
			nextID = &id
		}
	}
	parentText := ""
	if parent != nil {
		parentText = parent.Text
	}
	out := chunkDetailOut{
		chunkOut:   toChunkOut(chunk),
		ParentText: parentText,
		PrevID:     prevID,
		NextID:     nextID,
		Neighbours: nb,
	}
	WriteJSON(w, http.StatusOK, out)
}

// queryInt parses an optional int query param with a default.
func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}
