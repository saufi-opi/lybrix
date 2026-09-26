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
	// Pages is aligned with Markdown's lines: Pages[i] is the 1-based page of
	// line i, where the single trailing "\n" that Markdown always ends with
	// does not start a new line. So len(Pages) == number of "\n" in Markdown
	// == len(strings.Split(strings.TrimSuffix(Markdown, "\n"), "\n")).
	//
	// It is nil when no shard contributed a page range. Consumers that index it
	// must treat a nil or short map as "no page information" rather than
	// guessing — the chunker's pageMapper does exactly that.
	Pages []int
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
		// A shard payload almost always ends in "\n" (docling's markdown does).
		// strings.Split would then produce a trailing empty element that is not
		// a real line — emitting a page entry for it inflates the map by one per
		// shard and shifts every subsequent page number. Normalize it away
		// before splitting so len(docLines) is the true line count.
		md = strings.TrimSuffix(md, "\n")
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
			if n == 0 {
				continue
			}
			span := pageEnd - pageStart + 1
			for local := 0; local < n; local++ {
				pageOfLine = append(pageOfLine, pageStart+(local*span)/n)
			}
		}
	}
	md := strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
	// TrimSpace can drop leading/trailing blank lines from the join, which would
	// leave Pages indexed against lines that are no longer in Markdown. Shift the
	// map to match, or drop it when the two cannot be reconciled.
	pageOfLine = alignPages(md, pageOfLine, lines)
	return StitchedDoc{Markdown: md, Pages: pageOfLine}
}

// alignPages reconciles a page map built from the pre-trim line list with the
// final markdown.
//
// Markdown is the joined lines with surrounding whitespace trimmed plus one
// trailing "\n", so the only possible discrepancy is dropped leading or trailing
// blank lines; the map is shifted by that amount. When the sizes still cannot be
// reconciled the map is dropped entirely — a missing page is better than a wrong
// one, and every consumer treats nil as "no page information".
func alignPages(markdown string, pageOfLine []int, lines []string) []int {
	if len(pageOfLine) == 0 {
		return nil
	}
	emitted := len(lines)
	// Count the leading lines TrimSpace removed from the join.
	lead := 0
	for lead < len(lines) && strings.TrimSpace(lines[lead]) == "" {
		lead++
	}
	if lead > 0 && lead <= len(pageOfLine) {
		pageOfLine = pageOfLine[lead:]
		emitted -= lead
	}
	// Trailing blank lines are trimmed too.
	trail := 0
	for trail < len(lines)-lead && strings.TrimSpace(lines[len(lines)-1-trail]) == "" {
		trail++
	}
	if trail > 0 && trail <= len(pageOfLine) {
		pageOfLine = pageOfLine[:len(pageOfLine)-trail]
		emitted -= trail
	}
	if len(pageOfLine) != emitted || len(pageOfLine) != strings.Count(markdown, "\n") {
		return nil
	}
	return pageOfLine
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
