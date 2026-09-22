package api

import (
	"encoding/json"

	"fmt"
	"github.com/saufi-opi/lybrix/internal/store"
	"net/http"
	"strconv"
	"time"
)

// eventOut is the /v1/events list item (events.py row serialization).
type eventOut struct {
	ID        int64          `json:"id"`
	DocID     *string        `json:"doc_id"`
	ShardIdx  *int           `json:"shard_idx"`
	Level     string         `json:"level"`
	Stage     *string        `json:"stage"`
	Code      *string        `json:"code"`
	Message   string         `json:"message"`
	Context   map[string]any `json:"context"`
	WorkerID  *string        `json:"worker_id"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.deps.DB.ListEvents(r.Context(), store.EventFilter{
		DocID: q.Get("doc_id"), Level: q.Get("level"), Stage: q.Get("stage"),
		Limit: limit,
	})
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]eventOut, 0, len(rows))
	for _, e := range rows {
		out = append(out, eventOut{
			ID: e.ID, DocID: e.DocID, ShardIdx: e.ShardIdx, Level: e.Level,
			Stage: e.Stage, Code: e.Code, Message: e.Message, Context: e.Context,
			WorkerID: e.WorkerID, CreatedAt: e.CreatedAt,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// handleEventStream is the SSE feed of recent events, polled from Postgres.
//
// 2.0 keeps this deliberately dumb like 1.0: one query per poll tick, no
// broker fan-out. At 5–50 concurrent UI clients this is a rounding error on
// Postgres, and it removes a pub/sub dependency from the UI path.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeDetail(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	pollS := 2.0
	if v := r.URL.Query().Get("poll_s"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			pollS = f
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	var lastID int64
	tick := time.NewTicker(time.Duration(pollS * float64(time.Second)))
	defer tick.Stop()
	// first poll immediately so a fresh subscriber sees history at once
	for {
		rows, err := s.deps.DB.ListEvents(ctx, store.EventFilter{
			Limit: 100, LastID: lastID, Asc: true,
		})
		if err == nil {
			for _, e := range rows {
				if e.ID > lastID {
					lastID = e.ID
				}
				p := store.NewSSEPayload(e)
				b, _ := json.Marshal(p)
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			flusher.Flush()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
