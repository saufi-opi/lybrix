package api

import (
	"net/http"
	"time"
)

// usageRow is one by_key entry (usage.py shape).
type usageRow struct {
	KeyID      string     `json:"key_id"`
	Name       *string    `json:"name"`
	Calls      int        `json:"calls"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// usageSummary is the /v1/usage/summary payload: {period, total, by_key},
// calls desc.
type usageSummary struct {
	Period string     `json:"period"`
	Total  int        `json:"total"`
	ByKey  []usageRow `json:"by_key"`
}

// periodCutoff implements usage.py's cutoff semantics:
// today → local midnight; 24h/7d/30d → now minus N hours.
func periodCutoff(period string, now time.Time) (time.Time, bool) {
	switch period {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), true
	case "24h":
		return now.Add(-24 * time.Hour), true
	case "7d":
		return now.Add(-24 * 7 * time.Hour), true
	case "30d":
		return now.Add(-24 * 30 * time.Hour), true
	}
	return time.Time{}, false
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	cutoff, ok := periodCutoff(period, time.Now())
	if !ok {
		writeDetail(w, http.StatusUnprocessableEntity,
			"period must be one of today|24h|7d|30d")
		return
	}
	rows, err := s.deps.DB.UsageSummary(r.Context(), cutoff)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := usageSummary{Period: period, ByKey: []usageRow{}}
	for _, row := range rows {
		out.Total += row.Calls
		out.ByKey = append(out.ByKey, usageRow{
			KeyID: row.KeyID, Name: row.Name, Calls: row.Calls, LastUsedAt: row.LastUsedAt,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}
