package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runner CLI: golden-set dataset x live MCP endpoint -> results JSON +
// human summary MD. Port of scripts/eval/run.py.
//
// Exit codes: 0 on a clean run; non-zero when any query raised, or when
// <50% of queries returned ANY result rows (endpoint misconfig / auth /
// collection typo). Low hit-rate is a finding, not a crash.

const (
	// queryPacing is the verified pacing between queries.
	queryPacing = 300 * time.Millisecond
	// minResultFraction is the loud-failure threshold.
	minResultFraction = 0.5
)

// AnyResultsRate is the fraction of non-error queries whose search returned
// at least one row.
func AnyResultsRate(rows []map[string]any) float64 {
	eligible := 0
	withResults := 0
	for _, row := range rows {
		if _, isErr := row["error"]; isErr {
			continue
		}
		eligible++
		if rs, ok := row["results"].([]map[string]any); ok && len(rs) > 0 {
			withResults++
		}
	}
	if eligible == 0 {
		return 0.0
	}
	return float64(withResults) / float64(eligible)
}

// ResolveCollection resolves a --collection value to the collection ID that
// the search tool's payload filter actually matches on. A value matching an
// id is sent as-is; a value matching a name is resolved to that id.
func ResolveCollection(client *McpClient, value string) (string, error) {
	cols, err := client.ListCollections()
	if err != nil {
		return "", err
	}
	byID := map[string]bool{}
	names := []string{}
	ids := []string{}
	for _, c := range cols {
		id := toStr(c["id"])
		byID[id] = true
		ids = append(ids, id)
		if n := toStr(c["name"]); n != "" {
			names = append(names, n)
			if n == value {
				return id, nil
			}
		}
	}
	if byID[value] {
		return value, nil
	}
	sort.Strings(ids)
	sort.Strings(names)
	return "", fmt.Errorf(
		"--collection %q matches no collection id or name.\nvalid ids:   %s\nvalid names: %s",
		value, strings.Join(ids, ", "), strings.Join(names, ", "))
}

// RunQuery searches one query and judges it. Junk-flagged results stay in
// raw_results but are excluded from the filtered `results` ranking.
func RunQuery(client *McpClient, qc QueryCase, topK int, collectionID *string) map[string]any {
	started := time.Now()
	rawResults, err := client.Search(qc.Query, collectionID, topK)
	if err != nil {
		return map[string]any{"id": qc.ID, "query": qc.Query, "error": err.Error()}
	}
	results := []map[string]any{}
	for _, r := range rawResults {
		if !IsJunk(r) {
			results = append(results, r)
		}
	}
	rank := TitleHit(qc.ExpectedDocTitles, results)
	hitResult := map[string]any(nil)
	if rank != nil && *rank-1 < len(results) {
		hitResult = results[*rank-1]
	}
	row := map[string]any{
		"id":          qc.ID,
		"query":       qc.Query,
		"category":    qc.Category,
		"raw_results": rawResults,
		"results":     results,
		"hit_rank":    rank,
		"heading_hit": hitResult != nil && HeadingHit(qc.ExpectedHeadings, hitResult),
		"elapsed_s":   fmt.Sprintf("%.3f", time.Since(started).Seconds()),
	}
	if hitResult != nil {
		row["hit_title"] = hitResult["doc_title"]
	} else {
		row["hit_title"] = nil
	}
	return row
}

// --- summary metrics (judge.py CategoryStats/Summary) ---------------------

// CategoryStats aggregates one category's metrics.
type CategoryStats struct {
	Queries         int
	HitsAt1         int
	HitsAt3         int
	HitsAt8         int
	ReciprocalRanks []float64
	HeadingHits     int
}

// MRR is the mean reciprocal rank.
func (s *CategoryStats) MRR() float64 {
	if len(s.ReciprocalRanks) == 0 {
		return 0.0
	}
	sum := 0.0
	for _, r := range s.ReciprocalRanks {
		sum += r
	}
	return sum / float64(len(s.ReciprocalRanks))
}

// HitAt1 / HitAt3 / HitAt8 / HitAtTopK are the hit rates.
func (s *CategoryStats) HitAt1() float64 {
	return ratio(s.HitsAt1, s.Queries)
}
func (s *CategoryStats) HitAt3() float64 { return ratio(s.HitsAt3, s.Queries) }
func (s *CategoryStats) HitAt8() float64 { return ratio(s.HitsAt8, s.Queries) }

// HitAtTopK is the any-rank hit rate — a rank was recorded iff the expected
// doc appeared within the run's top_k; correct for any top_k.
func (s *CategoryStats) HitAtTopK() float64 {
	return ratio(len(s.ReciprocalRanks), s.Queries)
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0.0
	}
	return float64(n) / float64(d)
}

func accumulate(stats *CategoryStats, rank *int, headingOK bool) {
	stats.Queries++
	if rank != nil {
		stats.ReciprocalRanks = append(stats.ReciprocalRanks, 1.0/float64(*rank))
		if *rank == 1 {
			stats.HitsAt1++
		}
		if *rank <= 3 {
			stats.HitsAt3++
		}
		if *rank <= 8 {
			stats.HitsAt8++
		}
		if headingOK {
			stats.HeadingHits++
		}
	}
}

// Summary aggregates one dataset run.
type Summary struct {
	TotalQueries     int
	Evaluated        int
	Errors           int
	EmptyResponses   int
	Overall          CategoryStats
	ByCategory       map[string]*CategoryStats
	JunkHitsTop8Raw  int
	JunkSlotsTop8Raw int
}

// Evaluate aggregates per-query judge rows into a Summary. junkFilter nil
// means off (metrics keep junk rows).
func Evaluate(rows []map[string]any, casesByID map[string]QueryCase, junkFilter func(map[string]any) bool) *Summary {
	s := &Summary{TotalQueries: len(rows), ByCategory: map[string]*CategoryStats{}}
	for _, row := range rows {
		if _, isErr := row["error"]; isErr {
			s.Errors++
			continue
		}
		qc, ok := casesByID[toStr(row["id"])]
		if !ok {
			continue
		}
		stats, ok := s.ByCategory[qc.Category]
		if !ok {
			stats = &CategoryStats{}
			s.ByCategory[qc.Category] = stats
		}
		results, _ := row["results"].([]map[string]any)
		if results == nil {
			// error-free row with no results key: treat as empty response
			s.EmptyResponses++
			stats, ok := s.ByCategory[toStr(row["category"])]
			if !ok {
				stats = &CategoryStats{}
				s.ByCategory[toStr(row["category"])] = stats
			}
			accumulate(stats, nil, false)
			continue
		}
		if len(results) == 0 {
			s.EmptyResponses++
			accumulate(stats, nil, false) // counts as a miss in its category
			continue
		}
		rawResults := results
		if raw, ok := row["raw_results"].([]map[string]any); ok {
			rawResults = raw
		}
		n := len(rawResults)
		if n > 8 {
			n = 8
		}
		s.JunkSlotsTop8Raw += n
		for i := 0; i < n; i++ {
			if junkFilter != nil && junkFilter(rawResults[i]) {
				s.JunkHitsTop8Raw++
			}
		}
		ranked := results
		if junkFilter != nil {
			ranked = []map[string]any{}
			for _, r := range results {
				if !junkFilter(r) {
					ranked = append(ranked, r)
				}
			}
		}
		rank := TitleHit(qc.ExpectedDocTitles, ranked)
		hitResult := map[string]any(nil)
		if rank != nil && *rank-1 < len(ranked) {
			hitResult = ranked[*rank-1]
		}
		headingOK := hitResult != nil && HeadingHit(qc.ExpectedHeadings, hitResult)
		accumulate(&s.Overall, rank, headingOK)
		accumulate(stats, rank, headingOK)
	}
	s.Evaluated = s.Overall.Queries
	return s
}

// --- CLI -------------------------------------------------------------------

func runCmd(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataset := fs.String("dataset", "", "path to a JSONL dataset file")
	topK := fs.Int("top-k", 8, "search top_k (default 8)")
	label := fs.String("label", "", "label for the output files")
	junkFilter := fs.String("junk-filter", "on", "exclude TOC/junk-heading results from metrics (default on)")
	collection := fs.String("collection", "", "collection id or name (resolved to id); omit for the full corpus")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *dataset == "" || *label == "" {
		return fmt.Errorf("--dataset and --label are required")
	}
	cases, err := LoadDataset(*dataset)
	if err != nil {
		return err
	}
	client, err := NewMcpClient()
	if err != nil {
		return err
	}
	if err := client.Handshake(); err != nil {
		return err
	}
	var collectionID *string
	if *collection != "" {
		id, err := ResolveCollection(client, *collection)
		if err != nil {
			return err
		}
		collectionID = &id
	}

	casesByID := map[string]QueryCase{}
	for _, qc := range cases {
		casesByID[qc.ID] = qc
	}

	rows := []map[string]any{}
	for i, qc := range cases {
		rows = append(rows, RunQuery(client, qc, *topK, collectionID))
		if i < len(cases)-1 {
			time.Sleep(queryPacing)
		}
	}

	var jf func(map[string]any) bool
	if *junkFilter == "on" {
		jf = IsJunk
	}
	summary := Evaluate(rows, casesByID, jf)

	timestamp := time.Now().UTC().Format("20060102T150405Z")
	resultsDir := filepath.Join("scripts", "eval", "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return err
	}
	jsonPath := filepath.Join(resultsDir, fmt.Sprintf("%s-%s.json", *label, timestamp))
	mdPath := filepath.Join(resultsDir, fmt.Sprintf("%s-%s.md", *label, timestamp))

	summaryJSON := renderSummaryJSON(*label, timestamp, *dataset, *topK, *junkFilter, collectionID, rows, summary)
	if err := os.WriteFile(jsonPath, summaryJSON, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(mdPath, []byte(RenderSummaryMD(*label, rows, summary, *topK)), 0o644); err != nil {
		return err
	}

	o := summary.Overall
	fmt.Printf("label=%s queries=%d hit@1=%.2f hit@3=%.2f hit@%d=%.2f mrr=%.3f\n",
		*label, summary.TotalQueries, o.HitAt1(), o.HitAt3(), *topK, o.HitAtTopK(), o.MRR())
	fmt.Printf("wrote %s\nwrote %s\n", jsonPath, mdPath)

	failures := 0
	for _, row := range rows {
		if _, isErr := row["error"]; isErr {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "error: %d query(ies) raised during the run:\n", failures)
		for _, row := range rows {
			if e, ok := row["error"]; ok {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", row["id"], e)
			}
		}
		os.Exit(1)
	}
	rate := AnyResultsRate(rows)
	if rate < minResultFraction {
		fmt.Fprintf(os.Stderr,
			"error: only %.0f%% of queries returned any results (< %.0f%%) — check the endpoint URL, auth token, and --collection value (a wrong value silently filters to zero rows)\n",
			rate*100, minResultFraction*100)
		os.Exit(1)
	}
	return nil
}

func renderSummaryJSON(label, timestamp, dataset string, topK int, junkFilter string, collectionID *string, rows []map[string]any, s *Summary) []byte {
	summaryObj := map[string]any{
		"total_queries":   s.TotalQueries,
		"evaluated":       s.Evaluated,
		"errors":          s.Errors,
		"empty_responses": s.EmptyResponses,
		"hit_at_1":        s.Overall.HitAt1(),
		"hit_at_3":        s.Overall.HitAt3(),
		// hit_at_top_k (R-19): the old f"hit_at_{top_k}" key carried the
		// judge's fixed hit@8 value — the semantic key is always right.
		"hit_at_top_k": s.Overall.HitAtTopK(),
		"mrr":          s.Overall.MRR(),
		"by_category":  map[string]any{},
	}
	for name, stats := range s.ByCategory {
		summaryObj["by_category"].(map[string]any)[name] = map[string]any{
			"queries":      stats.Queries,
			"hit_at_1":     stats.HitAt1(),
			"hit_at_3":     stats.HitAt3(),
			"hit_at_8":     stats.HitAt8(),
			"hit_at_top_k": stats.HitAtTopK(),
			"mrr":          stats.MRR(),
		}
	}
	out := map[string]any{
		"label":         label,
		"created_utc":   timestamp,
		"dataset":       dataset,
		"top_k":         topK,
		"junk_filter":   junkFilter,
		"collection_id": collectionID,
		"rows":          rows,
		"summary":       summaryObj,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func fmtPct(num, den int) string {
	if den == 0 {
		return "n/a"
	}
	return strconv.Itoa(num*100/den) + "%"
}

// RenderSummaryMD is the human-readable run summary: hit@k / MRR,
// per-category table, junk-filter impact, per-query one-liners.
func RenderSummaryMD(label string, rows []map[string]any, s *Summary, topK int) string {
	var b strings.Builder
	overall := s.Overall
	fmt.Fprintf(&b, "# Eval run: %s\n\n", label)
	fmt.Fprintf(&b, "- Queries: %d (evaluated %d, errors %d, empty %d)\n",
		s.TotalQueries, s.Evaluated, s.Errors, s.EmptyResponses)
	fmt.Fprintf(&b, "- top_k: %d, junk-filter: on\n", topK)
	fmt.Fprintf(&b, "- hit@1 %s, hit@3 %s, hit@%d %s, MRR %.3f\n",
		fmtPct(overall.HitsAt1, overall.Queries), fmtPct(overall.HitsAt3, overall.Queries),
		topK, fmtPct(overall.HitsAt8, overall.Queries), overall.MRR())
	if overall.Queries > 0 {
		fmt.Fprintf(&b, "- heading-path hits among title-hits: %d/%d\n",
			overall.HeadingHits, countHits(rows))
	}
	b.WriteString("\n## Per category\n\n")
	b.WriteString("| category | n | hit@1 | hit@3 | hit@8 | MRR |\n|---|---|---|---|---|---|\n")
	names := []string{}
	for name := range s.ByCategory {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		st := s.ByCategory[name]
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %.3f |\n",
			name, st.Queries, fmtPct(st.HitsAt1, st.Queries), fmtPct(st.HitsAt3, st.Queries),
			fmtPct(st.HitsAt8, st.Queries), st.MRR())
	}
	fmt.Fprintf(&b, "\n## Junk-filter impact (raw top-8 slots)\n\n")
	fmt.Fprintf(&b, "- junk-flagged results in raw top-8: %d/%d slots (%s of top-8 noise)\n",
		s.JunkHitsTop8Raw, s.JunkSlotsTop8Raw, fmtPct(s.JunkHitsTop8Raw, s.JunkSlotsTop8Raw))
	b.WriteString("\n## Per query\n\n")
	for _, row := range rows {
		if e, ok := row["error"]; ok {
			fmt.Fprintf(&b, "- %s: ERROR %v\n", row["id"], e)
			continue
		}
		rank, ok := row["hit_rank"].(*int)
		rankText := "miss"
		if ok && rank != nil {
			rankText = strconv.Itoa(*rank)
		}
		title := "-"
		if t, ok := row["hit_title"].(string); ok && t != "" {
			title = t
		}
		fmt.Fprintf(&b, "- %s [%s]: rank %s -> %s\n", row["id"], row["category"], rankText, title)
	}
	b.WriteString("\n")
	return b.String()
}

func countHits(rows []map[string]any) int {
	n := 0
	for _, row := range rows {
		if rank, ok := row["hit_rank"].(*int); ok && rank != nil {
			n++
		}
	}
	return n
}
