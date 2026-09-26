// header_tracker.go keeps a markdown table's header row alive across a chunk
// boundary.
//
// When a table is larger than the chunk budget it is split between rows (each
// row is a protected unit), so every chunk after the first would otherwise
// start mid-table with no column names — the reader, the embedder and BM25 all
// lose the context that says what the columns mean. The tracker detects the
// header row and signals mergeUnits to prepend it to each following chunk.
//
// Ported from docreader/splitter/header_hook.py (WeKnora, MIT).
package chunker

import (
	"regexp"
	"sort"
	"strings"
)

// headerTrackerHook is a pattern pair for contextual header detection: when
// startPattern matches a unit's text, that text becomes the active header, and
// it stays active until endPattern matches a later unit.
type headerTrackerHook struct {
	startPattern *regexp.Regexp
	endPattern   *regexp.Regexp
	priority     int
}

// defaultHeaderHooks returns the header tracking hooks. Only markdown tables are
// tracked: the table header row plus its separator row, e.g.
// "| A | B |\n| --- | --- |\n".
var defaultHeaderHooks = []headerTrackerHook{
	{
		startPattern: regexp.MustCompile(`(?si)^\s*(?:\|[^|\n]*)+[\r\n]+\s*(?:\|\s*:?-{3,}:?\s*)+\|?[\r\n]+$`),
		// Ends on a blank line, or on a line that starts with neither a pipe
		// nor whitespace (i.e. real prose resumed).
		endPattern: regexp.MustCompile(`(?si)^\s*$|^\s*[^|\s].*$`),
		priority:   15,
	},
}

// tableRowPattern matches a single markdown table row: "| cell | cell |".
var tableRowPattern = regexp.MustCompile(`(?m)^\s*(?:\|[^|\n]*)+\|\s*$`)

// markdownTableHookPriority matches the table hook's priority above.
const markdownTableHookPriority = 15

// headerTracker maintains the state of active headers across split units.
type headerTracker struct {
	hooks         []headerTrackerHook
	activeHeaders map[int]string // priority -> header text
	endedHeaders  map[int]bool   // priorities that have been ended
	// pendingExtend holds headers whose column-name row was empty, awaiting the
	// first data row to supply real column names.
	pendingExtend map[int]bool
	// pendingTableBreak is set when a table row unit ends with a paragraph
	// break. The blank line between two tables is consumed by the "\n\n"
	// split, so the header stays active for one more unit to let us tell a
	// continuing table from a new one.
	pendingTableBreak bool
	// headerEndedThisUnit tells mergeUnits to flush before the current unit,
	// so a new table is not merged into a chunk still carrying the previous
	// table's prepended header.
	headerEndedThisUnit bool
}

func newHeaderTracker() *headerTracker {
	return &headerTracker{
		hooks:         defaultHeaderHooks,
		activeHeaders: make(map[int]string),
		endedHeaders:  make(map[int]bool),
		pendingExtend: make(map[int]bool),
	}
}

// update checks split text for header start/end markers and updates state.
func (ht *headerTracker) update(split string) {
	ht.headerEndedThisUnit = false

	if ht.pendingTableBreak {
		ht.pendingTableBreak = false
		if _, active := ht.activeHeaders[markdownTableHookPriority]; active {
			if firstTableRowColumnCount(split) > 0 {
				// A row follows the break: a new table started.
				ht.clearTableHeader()
				ht.headerEndedThisUnit = true
			} else {
				ht.clearTableHeader()
			}
		}
	}

	// 1. End any active header whose end pattern matches this unit.
	for _, hook := range ht.hooks {
		if _, active := ht.activeHeaders[hook.priority]; active {
			if hook.endPattern.MatchString(split) {
				ht.endedHeaders[hook.priority] = true
				delete(ht.activeHeaders, hook.priority)
				delete(ht.pendingExtend, hook.priority)
			}
		}
	}

	// 1b. Paragraph splits consume the blank line between tables, so mark the
	// break after "| last row |\n\n" and resolve it on the next unit. Also end
	// when a new row's column count differs from the active header's.
	if _, active := ht.activeHeaders[markdownTableHookPriority]; active {
		if !ht.pendingExtend[markdownTableHookPriority] {
			if splitEndsWithParagraphBreak(split) {
				ht.pendingTableBreak = true
			} else {
				ht.endTableHeaderOnColumnMismatch(split)
			}
		}
	}

	// 2. A header whose column-name row was empty (e.g. "||") gets the first
	// data row promoted into real column names:
	//
	//	before: "||"               + "| --- | --- |\n"
	//	after:  "| col1 | col2 |\n" + "| --- | --- |\n"
	for p := range ht.pendingExtend {
		if _, active := ht.activeHeaders[p]; active && tableRowPattern.MatchString(split) {
			sep := extractSeparatorLine(ht.activeHeaders[p])
			ht.activeHeaders[p] = split + sep
		}
		delete(ht.pendingExtend, p)
	}

	// 3. Start new headers, for hooks that are neither active nor ended.
	for _, hook := range ht.hooks {
		if _, active := ht.activeHeaders[hook.priority]; active {
			continue
		}
		if ht.endedHeaders[hook.priority] {
			continue
		}
		if loc := hook.startPattern.FindString(split); loc != "" {
			ht.activeHeaders[hook.priority] = loc
			if isEmptyTableHeaderRow(loc) {
				ht.pendingExtend[hook.priority] = true
			}
		}
	}

	// 4. Once nothing is active, clear the ended set so future tables track.
	if len(ht.activeHeaders) == 0 {
		for k := range ht.endedHeaders {
			delete(ht.endedHeaders, k)
		}
	}
}

// getHeaders returns the active headers concatenated, highest priority first.
func (ht *headerTracker) getHeaders() string {
	if len(ht.activeHeaders) == 0 {
		return ""
	}
	type entry struct {
		priority int
		text     string
	}
	entries := make([]entry, 0, len(ht.activeHeaders))
	for p, t := range ht.activeHeaders {
		entries = append(entries, entry{p, t})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].priority > entries[j].priority })

	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.text
	}
	return strings.Join(parts, "\n")
}

// isEmptyTableHeaderRow reports whether the header row (the line before the
// separator) contains only pipes and whitespace, i.e. the column names are
// empty. MarkItDown and similar converters emit this shape:
//
//	||
//	| --- | --- |
//	| real column A | real column B |
func isEmptyTableHeaderRow(header string) bool {
	idx := strings.IndexByte(header, '\n')
	if idx < 0 {
		return false
	}
	for _, r := range strings.TrimSpace(header[:idx]) {
		if r != '|' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// extractSeparatorLine returns the separator line (e.g. "| --- | --- |\n") from a
// table header string, or "" when there is none.
func extractSeparatorLine(header string) string {
	for _, line := range strings.Split(header, "\n") {
		if strings.Contains(line, "---") {
			return line + "\n"
		}
	}
	return ""
}

func (ht *headerTracker) clearTableHeader() {
	ht.endedHeaders[markdownTableHookPriority] = true
	delete(ht.activeHeaders, markdownTableHookPriority)
	delete(ht.pendingExtend, markdownTableHookPriority)
}

func (ht *headerTracker) endTableHeaderOnColumnMismatch(split string) {
	header, ok := ht.activeHeaders[markdownTableHookPriority]
	if !ok {
		return
	}
	rowCols := firstTableRowColumnCount(split)
	headerCols := headerTableColumnCount(header)
	if rowCols > 0 && headerCols > 0 && rowCols != headerCols {
		ht.clearTableHeader()
		ht.headerEndedThisUnit = true
	}
}

func splitEndsWithParagraphBreak(split string) bool {
	trimmed := strings.TrimRight(split, " \t\r")
	return strings.HasSuffix(trimmed, "\n\n") || strings.HasSuffix(trimmed, "\r\n\r\n")
}

// tableRowColumnCount counts the cells in one markdown table row.
func tableRowColumnCount(line string) int {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return 0
	}
	parts := strings.Split(line, "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	return len(parts)
}

// firstTableRowColumnCount returns the column count of the first table row in
// text, or 0 when there is none.
func firstTableRowColumnCount(text string) int {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && tableRowPattern.MatchString(line) {
			return tableRowColumnCount(line)
		}
	}
	return 0
}

// headerTableColumnCount returns the column count of the header's column-name
// row (skipping the separator line), or 0.
func headerTableColumnCount(header string) int {
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "---") {
			continue
		}
		if n := tableRowColumnCount(line); n > 0 {
			return n
		}
	}
	return 0
}

// headerColumnMismatch reports whether the next unit starts a new table whose
// width differs from the active header's.
func headerColumnMismatch(headers, nextUnit string) bool {
	headerCols := headerTableColumnCount(headers)
	rowCols := firstTableRowColumnCount(nextUnit)
	return headerCols > 0 && rowCols > 0 && headerCols != rowCols
}

// headerAlreadyPresent reports whether the header (or just its column-name row)
// already appears in the overlap or the next unit, which prevents duplicating it
// when the boundary happens to fall inside the table's own header.
func headerAlreadyPresent(headers, overlapText, unitText string) bool {
	if strings.Contains(overlapText, headers) || strings.Contains(unitText, headers) {
		return true
	}
	colRow := headerColumnRow(headers)
	if colRow == "" {
		return false
	}
	return strings.Contains(overlapText, colRow) || strings.Contains(unitText, colRow)
}

// headerColumnRow extracts the column-name line from a header string, or "" when
// the header carries no meaningful column names.
func headerColumnRow(header string) string {
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "---") {
			continue
		}
		onlyPipes := true
		for _, r := range line {
			if r != '|' && r != ' ' && r != '\t' {
				onlyPipes = false
				break
			}
		}
		if !onlyPipes {
			return line
		}
	}
	return ""
}
