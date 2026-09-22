package pipeline

import (
	"encoding/json"
	"sort"
	"strings"
)

// StitchedDoc is the stitch output: one markdown string plus a per-line
// page map (0-based line index → 1-based page).
type StitchedDoc struct {
	Markdown string
	Pages    []int // len == line count of Markdown when computed
}

// Stitch concatenates shard markdown payloads in order into one doc.
//
// Heading reconciliation is line-based: a shard's first markdown heading
// that equals the previous shard's last is dropped (the 1-page overlap
// shows up here). shardPageRanges is a list of (page_start, page_end)
// 1-based inclusive pairs aligned with docs order (sorted shard idx). The
// result carries the per-line page map interpolated proportionally within
// each shard; ranges align to the docs that actually contributed lines — a
// shard with empty markdown is skipped together with its range so the map
// stays monotone. Port of stitch.py.
func Stitch(docs map[int]string, shardPageRanges map[int][2]int) StitchedDoc {
	idxs := make([]int, 0, len(docs))
	for idx := range docs {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	var lines []string
	var pageOfLine []int

	for _, idx := range idxs {
		md := normalizeMarkdown(docs[idx])
		if md == "" {
			continue
		}
		docLines := strings.Split(md, "\n")
		if len(lines) > 0 && len(docLines) > 0 {
			// drop a duplicated boundary heading from the overlap
			prevLast := lastHeading(lines)
			first := firstHeading(docLines)
			if prevLast != nil && first != nil && *first == *prevLast {
				docLines = docLines[1:]
				// and the blank line under it, if any
				for len(docLines) > 0 && strings.TrimSpace(docLines[0]) == "" {
					docLines = docLines[1:]
				}
			}
		}
		lines = append(lines, docLines...)
		if pr, ok := shardPageRanges[idx]; ok {
			pageStart, pageEnd := pr[0], pr[1]
			n := len(docLines)
			span := pageEnd - pageStart + 1
			for local := 0; local < n; local++ {
				pageOfLine = append(pageOfLine, pageStart+(local*span)/n)
			}
		}
	}
	md := strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
	return StitchedDoc{Markdown: md, Pages: pageOfLine}
}

// normalizeMarkdown tolerates the two payload shapes the parser uploads:
// plain markdown, or a JSON wrapper {"markdown": ...} (load_shard_docs
// treated non-JSON payloads as the markdown body itself; JSON payloads got
// their "markdown" field).
func normalizeMarkdown(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return text
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return text
	}
	if md, ok := payload["markdown"].(string); ok {
		return md
	}
	return text
}

func firstHeading(lines []string) *string {
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") {
			return &s
		}
	}
	return nil
}

func lastHeading(lines []string) *string {
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "#") {
			return &s
		}
	}
	return nil
}
