package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// QueryCase is one dataset row (dataset.py QueryCase).
type QueryCase struct {
	ID                string   `json:"id"`
	Query             string   `json:"query"`
	ExpectedDocTitles []string `json:"expected_doc_titles"`
	Category          string   `json:"category"`
	ExpectedHeadings  []string `json:"expected_headings"`
	Notes             string   `json:"notes"`
}

var categories = map[string]bool{"exact": true, "conceptual": true, "drift": true}

// LoadDataset parses and validates a JSONL dataset; errors carry the
// 1-based line number (dataset.py contract).
func LoadDataset(path string) ([]QueryCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []QueryCase
	seen := map[string]int{}
	for lineNo, line := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue // tolerate blank lines
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(text), &row); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %v", lineNo+1, err)
		}
		qc, err := validateRow(row, lineNo+1)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[qc.ID]; dup {
			return nil, fmt.Errorf("line %d: duplicate id %q (first seen line %d)", lineNo+1, qc.ID, prev)
		}
		seen[qc.ID] = lineNo + 1
		cases = append(cases, *qc)
	}
	return cases, nil
}

func validateRow(row map[string]any, lineNo int) (*QueryCase, error) {
	for _, req := range []string{"id", "query", "category"} {
		v, ok := row[req].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("line %d: missing or empty required field %q", lineNo, req)
		}
	}
	rawTitles, ok := row["expected_doc_titles"].([]any)
	if !ok || len(rawTitles) == 0 {
		return nil, fmt.Errorf("line %d: expected_doc_titles must be a non-empty list of non-empty strings", lineNo)
	}
	titles := []string{}
	for _, t := range rawTitles {
		s, ok := t.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("line %d: expected_doc_titles must be a non-empty list of non-empty strings", lineNo)
		}
		titles = append(titles, s)
	}
	category, _ := row["category"].(string)
	if !categories[category] {
		return nil, fmt.Errorf("line %d: unknown category %q (expected one of exact, conceptual, drift)", lineNo, category)
	}
	for _, title := range titles {
		if SpaceStrippedLen(title) < 8 {
			return nil, fmt.Errorf(
				"line %d: expected title %q normalizes to fewer than 8 space-stripped chars and can never match (see titlesMatch); fix the row",
				lineNo, title)
		}
	}
	headings := []string{}
	if rawH, ok := row["expected_headings"].([]any); ok {
		for _, h := range rawH {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("line %d: expected_headings must be a list of strings", lineNo)
			}
			headings = append(headings, s)
		}
	}
	notes, _ := row["notes"].(string)
	return &QueryCase{
		ID: row["id"].(string), Query: row["query"].(string),
		ExpectedDocTitles: titles, Category: category,
		ExpectedHeadings: headings, Notes: notes,
	}, nil
}
