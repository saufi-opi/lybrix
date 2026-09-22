package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// Compare two eval results files: aggregate deltas + per-query regression
// list. Port of scripts/eval/compare.py. Exit 0 either way — it is a
// measurement tool, not a gate — but --fail-on-regression gives CI a gate.

// loadRows reads {id -> row} from a run results JSON; errors/empty rows
// included.
func loadRows(path string) (map[string]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for _, row := range doc.Rows {
		out[toStr(row["id"])] = row
	}
	return out, nil
}

// HitRank is the effective hit rank: nil for error/miss rows; misses rank
// worse than any real rank (top_k + 1) so deltas and regressions stay
// well-defined.
func HitRank(row map[string]any) *int {
	if _, isErr := row["error"]; isErr {
		return nil
	}
	rs, ok := row["results"].([]map[string]any)
	if !ok || len(rs) == 0 {
		return nil
	}
	if rank, ok := row["hit_rank"].(*int); ok {
		return rank
	}
	return nil
}

// Classify compares one query pair: "regression", "improvement", or
// "unchanged".
func Classify(baseRow, candRow map[string]any, topK int) string {
	baseRank, candRank := HitRank(baseRow), HitRank(candRow)
	if sameRank(baseRank, candRank) {
		return "unchanged"
	}
	baseKey := topK + 1
	if baseRank != nil {
		baseKey = *baseRank
	}
	candKey := topK + 1
	if candRank != nil {
		candKey = *candRank
	}
	if candKey > baseKey {
		return "regression"
	}
	return "improvement"
}

func sameRank(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// summaryValue reads a metric from a summary with a hit_at_top_k
// back-compat fallback (old result files carry hit_at_8 under
// f"hit_at_{top_k}").
func summaryValue(summary map[string]any, metric string) (float64, bool) {
	if v, ok := summary[metric].(float64); ok {
		return v, true
	}
	if metric == "hit_at_top_k" {
		if v, ok := summary["hit_at_8"].(float64); ok {
			return v, true
		}
	}
	return 0, false
}

func compareCmd(argv []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	failOnRegression := fs.Bool("fail-on-regression", false, "exit non-zero when any query regressed")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: lybrix-eval compare <base.json> <candidate.json>")
	}
	basePath, candPath := fs.Arg(0), fs.Arg(1)

	baseDoc, err := readDoc(basePath)
	if err != nil {
		return err
	}
	topK := 8
	if baseDoc.TopK != 0 {
		topK = baseDoc.TopK
	}
	baseRows, err := loadRows(basePath)
	if err != nil {
		return err
	}
	candRows, err := loadRows(candPath)
	if err != nil {
		return err
	}

	var regressions, improvements []string
	shared := []string{}
	baseOnly := []string{}
	candOnly := []string{}
	for id := range baseRows {
		if _, ok := candRows[id]; !ok {
			baseOnly = append(baseOnly, id)
		}
	}
	for id := range candRows {
		if _, ok := baseRows[id]; !ok {
			candOnly = append(candOnly, id)
		} else {
			shared = append(shared, id)
		}
	}
	sort.Strings(shared)
	for _, id := range shared {
		switch Classify(baseRows[id], candRows[id], topK) {
		case "regression":
			regressions = append(regressions, id)
		case "improvement":
			improvements = append(improvements, id)
		}
	}

	fmt.Printf("matched queries: %d\n", len(shared))
	if len(baseOnly) > 0 {
		fmt.Printf("only in base: %s\n", joinComma(baseOnly))
	}
	if len(candOnly) > 0 {
		fmt.Printf("only in candidate: %s\n", joinComma(candOnly))
	}
	for _, metric := range []string{"hit_at_1", "hit_at_3", "hit_at_top_k", "hit_at_8", "mrr"} {
		b, okB := summaryValue(baseDoc.Summary, metric)
		c, okC := summaryValue(readCandidateSummary(candPath), metric)
		if okB && okC {
			delta := c - b
			fmt.Printf("%s: %s\n", metric, fmtDelta(delta))
		}
	}
	for _, pair := range []struct {
		title string
		ids   []string
	}{{"regressions", regressions}, {"improvements", improvements}} {
		fmt.Printf("%s: %d\n", pair.title, len(pair.ids))
		for _, id := range pair.ids {
			fmt.Printf("  - %s\n", id)
		}
	}
	if *failOnRegression && len(regressions) > 0 {
		fmt.Fprintf(os.Stderr, "error: %d regression(s)\n", len(regressions))
		os.Exit(1)
	}
	return nil
}

// resultsDoc mirrors the run results JSON top-level shape.
type resultsDoc struct {
	TopK    int            `json:"top_k"`
	Summary map[string]any `json:"summary"`
}

func readDoc(path string) (*resultsDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc resultsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func readCandidateSummary(path string) map[string]any {
	doc, err := readDoc(path)
	if err != nil {
		return nil
	}
	return doc.Summary
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

func fmtDelta(d float64) string {
	sign := "+"
	if d < 0 {
		sign = "-"
		d = -d
	}
	return sign + strconv.FormatFloat(d, 'f', 4, 64)
}
