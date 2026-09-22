package api

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics: in-process counters exposed at /v1/system/metrics in a shape
// trivially convertible to Prometheus text format (§10.4) — the 1.0
// metrics.py design, atomic counters instead of a mutex map.
var (
	metricsMu      sync.Mutex
	metricsStarted = time.Now()

	countSearchRequests atomic.Int64
	countSearchHits     atomic.Int64
	countEmbedJobs      atomic.Int64
	countParseJobs      atomic.Int64
	countSplitJobs      atomic.Int64
	countShardsDone     atomic.Int64
	countShardsFailed   atomic.Int64
	countAPIRequests    atomic.Int64
	countMCPRequests    atomic.Int64
	searchLatencySumMS  atomic.Int64
	searchLatencyCount  atomic.Int64
)

// IncrSearchRequest records one search call and its latency.
func IncrSearchRequest(latencyMS int64) {
	countSearchRequests.Add(1)
	searchLatencySumMS.Add(latencyMS)
	searchLatencyCount.Add(1)
}

// IncrNamed bumps a named pipeline counter.
func IncrNamed(name string) {
	switch name {
	case "split_jobs":
		countSplitJobs.Add(1)
	case "parse_jobs":
		countParseJobs.Add(1)
	case "embed_jobs":
		countEmbedJobs.Add(1)
	case "shards_done":
		countShardsDone.Add(1)
	case "shards_failed":
		countShardsFailed.Add(1)
	case "search_hits":
		countSearchHits.Add(1)
	case "api_requests":
		countAPIRequests.Add(1)
	case "mcp_requests":
		countMCPRequests.Add(1)
	}
}

// MetricsSnapshotJSON is the /v1/system/metrics body.
func MetricsSnapshotJSON() map[string]any {
	metricsMu.Lock()
	started := metricsStarted
	metricsMu.Unlock()
	out := map[string]any{
		"uptime_s": time.Since(started).Seconds(),
		"counters": map[string]int64{
			"search_requests": countSearchRequests.Load(),
			"search_hits":     countSearchHits.Load(),
			"split_jobs":      countSplitJobs.Load(),
			"parse_jobs":      countParseJobs.Load(),
			"embed_jobs":      countEmbedJobs.Load(),
			"shards_done":     countShardsDone.Load(),
			"shards_failed":   countShardsFailed.Load(),
			"api_requests":    countAPIRequests.Load(),
			"mcp_requests":    countMCPRequests.Load(),
		},
	}
	if n := searchLatencyCount.Load(); n > 0 {
		out["search_latency_avg_ms"] = searchLatencySumMS.Load() / n
	}
	return out
}

var _ = context.Background
