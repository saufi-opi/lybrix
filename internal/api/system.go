package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// healthPayload mirrors SystemHealth semantics: status ok|degraded, plus
// per-component strings. Qdrant's slot is gone; the embed backend probe
// (tei-ingest / ollama) is exposed at /pipeline, not here (system.py kept
// the health body to postgres/redis/qdrant/tei_query — 2.0 swaps qdrant
// for nothing at this level; the store is Postgres itself).
type healthPayload struct {
	Status   string `json:"status"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
	TeiQuery string `json:"tei_query"`
}

// checkURL probes {url}/health with a 2s timeout; "ok" only on HTTP 200.
func checkURL(ctx context.Context, url string) string {
	if url == "" {
		return "down"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return "down"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "ok"
	}
	return "down"
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	pg := "ok"
	if err := s.deps.DB.Ping(r.Context()); err != nil {
		pg = "down"
	}
	redisStatus := "ok"
	if err := s.deps.Redis.Ping(r.Context()).Err(); err != nil {
		redisStatus = "down"
	}
	tei := "down"
	if def, err := s.deps.DB.GetDefaultEmbeddingModel(r.Context()); err == nil && def != nil {
		tei = checkURL(r.Context(), def.QueryURL)
	}
	status := "ok"
	if pg != "ok" {
		status = "degraded"
	}
	WriteJSON(w, http.StatusOK, healthPayload{
		Status: status, Postgres: pg, Redis: redisStatus, TeiQuery: tei,
	})
}

func (s *Server) handleQueues(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for _, name := range queue.AllStreams {
		length, err := s.deps.Redis.XLen(r.Context(), name).Result()
		if err != nil {
			// a dead Redis must not read as an empty queue — propagate (500)
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		pending, err := s.deps.Redis.XPending(r.Context(), name, queue.ConsumerGroup).Result()
		if err != nil {
			pending = nil
		}
		var pcount int64
		if pending != nil {
			pcount = pending.Count
		}
		undelivered, err := queue.UndeliveredCount(r.Context(), s.deps.Redis, name)
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		out[name] = map[string]any{
			// "length" is XLEN (every entry ever written — streams are never
			// trimmed), NOT the live backlog. Kept for history.
			"length":      length,
			"pending":     pcount,
			"undelivered": undelivered,
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// laneOut is one stream's lane view (pipeline page).
type laneOut struct {
	Waiting   *int   `json:"waiting"`
	InFlight  *int   `json:"in_flight"`
	Stale     int    `json:"stale"`
	Consumers int    `json:"consumers"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handlePipeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// -- component health -------------------------------------------------
	components := map[string]any{
		"postgres": "ok",
		"redis":    "ok",
	}
	if err := s.deps.DB.Ping(ctx); err != nil {
		components["postgres"] = "down"
	}
	if err := s.deps.Redis.Ping(ctx).Err(); err != nil {
		components["redis"] = "down"
	}
	components["tei_query"] = "down"
	components["embed_backend"] = "down"
	components["embedding_model"] = ""
	components["embed_provider"] = ""
	if def, err := s.deps.DB.GetDefaultEmbeddingModel(ctx); err == nil && def != nil {
		components["tei_query"] = checkURL(ctx, def.QueryURL)
		components["embed_backend"] = checkEmbedModel(ctx, def)
		components["embedding_model"] = def.ModelID
		components["embed_provider"] = def.Provider
	}

	// -- queue lanes -------------------------------------------------------
	lanes := map[string]any{}
	for _, name := range queue.AllStreams {
		lane := laneOut{}
		groups, err := s.deps.Redis.XInfoGroups(ctx, name).Result()
		if err != nil {
			slog.Warn("pipeline lane read failed", "stream", name, "err", err)
			lane.Error = err.Error()
			lanes[name] = lane
			continue
		}
		var grp *queueGroupInfo
		for _, g := range groups {
			if g.Name == queue.ConsumerGroup {
				grp = &queueGroupInfo{LastDelivered: g.LastDeliveredID}
				break
			}
		}
		if grp == nil {
			lanes[name] = lane
			continue
		}
		undelivered, err := queue.UndeliveredCount(ctx, s.deps.Redis, name)
		if err != nil {
			lane.Error = err.Error()
			lanes[name] = lane
			continue
		}
		pend, err := s.deps.Redis.XPendingExt(ctx, redisPendingExtArgs(name)).Result()
		if err != nil {
			lane.Error = err.Error()
			lanes[name] = lane
			continue
		}
		waiting := int(undelivered + int64(len(pend)))
		inFlight := len(pend)
		for _, p := range pend {
			// entries idle > 15 min are stale (system.py 900_000 ms check)
			if p.Idle > 15*time.Minute {
				lane.Stale++
			}
		}
		consumers, _ := s.deps.Redis.XInfoConsumers(ctx, name, queue.ConsumerGroup).Result()
		for _, c := range consumers {
			if c.Idle < 5*time.Minute {
				lane.Consumers++
			}
		}
		lane.Waiting = &waiting
		lane.InFlight = &inFlight
		lanes[name] = lane
	}

	// -- counters -----------------------------------------------------------
	counts := map[string]any{}
	if snap, err := s.deps.DB.MetricsSnapshot(ctx); err == nil {
		counts["docs_ready"] = snap.DocsReady
		counts["docs_parsing"] = snap.DocsParsing
		counts["docs_failed"] = snap.DocsFailed
		counts["docs_total"] = snap.DocsTotal
		counts["docs_awaiting_embed"] = snap.DocsAwaitingEmbed
		counts["docs_parsing_active"] = snap.DocsParsingActive
		counts["shards_done"] = snap.ShardsDone
		counts["shards_pending"] = snap.ShardsPending
		counts["shards_failed"] = snap.ShardsFailed
		counts["shards_running"] = snap.ShardsRunning
		counts["shards_total"] = snap.ShardsTotal
		// Full state breakdown — dashboards must NOT derive per-state counts
		// from ?limit=N document lists.
		counts["docs_states"] = snap.States
	}

	// -- in-flight parse detail ---------------------------------------------
	inFlight := []map[string]any{}
	if rows, err := s.deps.DB.InFlightParse(ctx); err == nil {
		for _, row := range rows {
			inFlight = append(inFlight, map[string]any{
				"title": row.Title, "idx": row.Idx,
				"page_start": row.PageStart, "page_end": row.PageEnd,
				"worker_id": row.WorkerID, "lease_until": row.LeaseUntil,
			})
		}
	}

	// -- chunk totals (2.0: ParadeDB replaces qdrant_points) -----------------
	chunksTotal := (*int)(nil)
	if n, err := s.deps.DB.CountChunks(ctx); err == nil {
		chunksTotal = &n
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"components":      components,
		"lanes":           lanes,
		"counts":          counts,
		"in_flight_parse": inFlight,
		// 2.0 delta: qdrant_points is replaced by chunks_total from ParadeDB.
		"chunks_total": chunksTotal,
		"server_time":  nil, // web layer stamps local time
	})
}

// queueGroupInfo carries the fields of XInfoGroups the lanes loop needs.
type queueGroupInfo struct {
	LastDelivered string
}

// checkEmbedModel probes one registered model's ingest-plane endpoint:
// ollama answers /api/tags, openai answers GET /v1/models, TEI /health.
func checkEmbedModel(ctx context.Context, m *store.EmbeddingModel) string {
	client := &http.Client{Timeout: 2 * time.Second}
	path := "/health"
	switch m.Provider {
	case "ollama":
		path = "/api/tags"
	case "openai":
		path = "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.IngestURL, "/")+path, nil)
	if err != nil {
		return "down"
	}
	if m.Provider == "openai" && m.HasAPIKey {
		// probe without the key: presence is enough for a reachability check;
		// the key itself must never transit logs/probe paths unnecessarily.
		_ = m
	}
	resp, err := client.Do(req)
	if err != nil {
		return "down"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "ok"
	}
	return "down"
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// JSON counters snapshot: in-process atomic counters + janitor rollup
	// read (§10.4 Prometheus-convertible shape).
	snap := MetricsSnapshotJSON()
	rollup := map[string]any{}
	if rows, err := s.deps.DB.LatestRollups(r.Context(), 60); err == nil {
		for _, row := range rows {
			b, _ := json.Marshal(row)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			rollup[row.Bucket.Format(time.RFC3339)] = m
		}
	}
	snap["rollup"] = rollup
	WriteJSON(w, http.StatusOK, snap)
}

var _ = store.BookSettled

// redisPendingExtArgs builds the 1.0 pending-range shape: min "-" max "+",
// count 100.
func redisPendingExtArgs(stream string) *redis.XPendingExtArgs {
	return &redis.XPendingExtArgs{Stream: stream, Group: queue.ConsumerGroup,
		Start: "-", End: "+", Count: 100}
}
