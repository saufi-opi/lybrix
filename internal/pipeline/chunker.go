package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Tokenizer counts tokens; 2.0 standardizes on the whitespace heuristic the
// 1.0 chunker defaulted to (an optional HF-backed impl can slot in later).
type Tokenizer interface {
	Count(text string) int
}

// WhitespaceTokenizer is the cheap word-count heuristic (books-rag
// convention): good enough to keep chunks in a reasonable ballpark.
type WhitespaceTokenizer struct{}

func (WhitespaceTokenizer) Count(text string) int { return len(strings.Fields(text)) }

// Tuning constants (blueprint §5).
const (
	ChildWindowTokens  = 384
	ChildStrideTokens  = 64
	ParentBudgetTokens = 2048
	ParentHardCap      = 4096
)

// ParentChunk is one hierarchical parent row (is_parent=true, no embedding).
type ParentChunk struct {
	Seq         int
	Text        string
	ChunkHash   string
	TokenCount  int
	PageStart   *int
	PageEnd     *int
	HeadingPath []string
}

// ChildChunk is one hierarchical child row (is_parent=false, embedded).
type ChildChunk struct {
	Seq              int
	ParentSeq        int
	ParentHash       string
	Text             string
	ChunkHash        string
	TokenCount       int
	PageStart        *int
	PageEnd          *int
	HeadingPath      []string
	HeaderBreadcrumb string
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
var fenceRE = regexp.MustCompile("^(```|~~~)")

// BreadcrumbSep is the join separator for breadcrumbs.
const BreadcrumbSep = " > "

// NormalizeHeadingPath strips empties/whitespace; dedupes consecutive
// repeats (1.0 headings.py).
func NormalizeHeadingPath(path []string) []string {
	out := []string{}
	for _, part := range path {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != t {
			out = append(out, t)
		}
	}
	return out
}

// RenderHeadingPath joins the normalized path with " > ".
func RenderHeadingPath(path []string) string {
	return strings.Join(NormalizeHeadingPath(path), BreadcrumbSep)
}

// HashText is the 1.0 chunk hash: sha256 of whitespace-normalized text.
func HashText(text string) string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(h[:])
}

// section is one ATX-heading-delimited markdown section.
type section struct {
	headingPath []string
	body        []lineNo // (global line index, text) pairs
}

type lineNo struct {
	line int
	text string
}

// splitSections is the fence-aware _sections port (hybrid.py:117-163) —
// same heading-path stack semantics: a heading pops the stack down to the
// first level strictly lower than its own, then pushes.
func splitSections(markdown string) []section {
	var sections []section
	type levelTitle struct {
		level int
		title string
	}
	var stack []levelTitle
	var current []lineNo
	inFence := false
	started := false
	startLine := 0

	flush := func(endLine int) {
		if started {
			hs := make([]string, len(stack))
			for i, lt := range stack {
				hs[i] = lt.title
			}
			sections = append(sections, section{headingPath: hs, body: current})
		}
		current = nil
	}

	lines := strings.Split(markdown, "\n")
	lastLine := len(lines) - 1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if fenceRE.MatchString(strings.TrimSpace(trimmed)) {
			inFence = !inFence
			current = append(current, lineNo{line: i, text: trimmed})
			started = true
			continue
		}
		m := headingRE.FindStringSubmatch(trimmed)
		if m != nil && !inFence {
			// section body ends on the last line BEFORE this heading
			flush(max(i-1, 0))
			started = true
			startLine = i
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, levelTitle{level: level, title: title})
			continue
		}
		current = append(current, lineNo{line: i, text: trimmed})
		if strings.TrimSpace(trimmed) != "" {
			started = true
			if len(current) > 1 {
				nonEmpty := false
				for _, l := range current[:len(current)-1] {
					if strings.TrimSpace(l.text) != "" {
						nonEmpty = true
						break
					}
				}
				if !nonEmpty {
					startLine = i
				}
			}
		}
	}
	_ = startLine
	flush(lastLine)
	return sections
}

// sectionText builds the section body text: heading tail + body, the same
// "\n\n".join([heading_path[-1], body]) shape 1.0's _emit used.
func sectionText(headingPath []string, body []lineNo) string {
	var words []string
	for _, l := range body {
		words = append(words, strings.Fields(l.text)...)
	}
	text := strings.Join(words, " ")
	if len(headingPath) > 0 {
		text = strings.Join([]string{headingPath[len(headingPath)-1], text}, "\n\n")
	}
	return text
}

// ChunkHierarchical produces the 2.0 parent–child chunk structure:
//
//	Parent  = one section's text snapped to headings, capped at 2048 tokens
//	          (over-budget sections split at sub-heading boundaries; else
//	          hard-cut at 4096), is_parent=true, no embedding.
//	Child   = sliding window inside its parent: 384 tokens, 64-token stride
//	          overlap; carries parent link, breadcrumb, per-window page
//	          range (R-13: windows must carry their own line numbers).
//
// pages maps the 0-based line index of markdown to a 1-based page number
// (built at stitch time). Returns [] for empty input.
func ChunkHierarchical(markdown string, pages []int, tok Tokenizer) ([]ParentChunk, []ChildChunk) {
	if tok == nil {
		tok = WhitespaceTokenizer{}
	}
	var parents []ParentChunk
	var children []ChildChunk

	for _, sec := range splitSections(markdown) {
		// 1.0 _sections semantics: a section with no body words is never
		// emitted — the heading tail is added at emit time, so an empty
		// body yields no chunk even though secText contains the heading.
		bodyWords := 0
		for _, l := range sec.body {
			bodyWords += len(strings.Fields(l.text))
		}
		if bodyWords == 0 {
			continue
		}
		secText := sectionText(sec.headingPath, sec.body)
		if strings.TrimSpace(secText) == "" {
			continue
		}

		// --- parent -------------------------------------------------------
		parentPieces := splitIntoParents(secText, tok)
		for _, pText := range parentPieces {
			parents = append(parents, ParentChunk{
				Seq:         len(parents),
				Text:        pText,
				ChunkHash:   HashText(pText),
				TokenCount:  tok.Count(pText),
				PageStart:   pageAt(pages, sec),
				PageEnd:     pageEndAt(pages, sec),
				HeadingPath: NormalizeHeadingPath(sec.headingPath),
			})
		}

		// --- children -----------------------------------------------------
		// Child text passed to the embedder is breadcrumb-prefixed
		// ("[Doc > Chapter > Section] " + text) per blueprint §5; stored
		// text stays unprefixed (prefix applied at embed time — keeps
		// read_pages output clean and chunk_hash stable across prefix
		// changes).
		parent := &parents[len(parents)-1]
		breadcrumb := RenderHeadingPath(sec.headingPath)
		windows := childWindows(sec, tok)
		for _, w := range windows {
			wText := w.text
			if wText == "" {
				continue
			}
			child := ChildChunk{
				Seq:         len(children),
				ParentSeq:   parent.Seq,
				ParentHash:  parent.ChunkHash,
				Text:        wText,
				ChunkHash:   HashText(wText),
				TokenCount:  tok.Count(wText),
				PageStart:   w.pageStart,
				PageEnd:     w.pageEnd,
				HeadingPath: NormalizeHeadingPath(sec.headingPath),
			}
			if breadcrumb != "" {
				child.HeaderBreadcrumb = "[ " + breadcrumb + " ]"
			}
			children = append(children, child)
		}
	}
	return parents, children
}

func intPtr(i int) *int       { return &i }

func pageAt(pages []int, sec section) *int {
	for _, l := range sec.body {
		if strings.TrimSpace(l.text) != "" && l.line < len(pages) {
			return intPtr(pages[l.line])
		}
	}
	return nil
}

func pageEndAt(pages []int, sec section) *int {
	for i := len(sec.body) - 1; i >= 0; i-- {
		if strings.TrimSpace(sec.body[i].text) != "" && sec.body[i].line < len(pages) {
			return intPtr(pages[sec.body[i].line])
		}
	}
	return nil
}

// splitIntoParents caps a section's text at 2048 tokens: an over-budget
// section is hard-cut at 4096 tokens into chunks (the 2.0 "hard-cut at
// 4096" rule; sub-heading splitting happens naturally because each
// sub-section is its own section from _sections).
func splitIntoParents(text string, tok Tokenizer) []string {
	total := tok.Count(text)
	if total <= ParentBudgetTokens {
		return []string{text}
	}
	// over budget: cut into ≤4096-token slices at word boundaries
	words := strings.Fields(text)
	var out []string
	var cur []string
	curTokens := 0
	for _, w := range words {
		t := tok.Count(w)
		if curTokens > 0 && curTokens+t > ParentHardCap {
			out = append(out, strings.Join(cur, " "))
			cur, curTokens = nil, 0
		}
		cur = append(cur, w)
		curTokens += t
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}

// childWindow is one sliding-window child with its own page range (R-13).
type childWindow struct {
	text      string
	pageStart *int
	pageEnd   *int
}

// childWindows produces 384-token windows with 64-token stride overlap
// inside one section. Each window carries its own first/last line's pages
// (the section's span made every window of a long section cite the full
// range — R-13).
func childWindows(sec section, tok Tokenizer) []childWindow {
	type wordLine struct {
		word string
		line int
	}
	var words []wordLine
	for _, l := range sec.body {
		for _, w := range strings.Fields(l.text) {
			words = append(words, wordLine{word: w, line: l.line})
		}
	}
	if len(words) == 0 {
		return nil
	}
	var out []childWindow
	start := 0
	for start < len(words) {
		// window: up to ChildWindowTokens tokens
		end := start
		tokens := 0
		for end < len(words) {
			t := tok.Count(words[end].word)
			if tokens > 0 && tokens+t > ChildWindowTokens {
				break
			}
			tokens += t
			end++
		}
		var wwords []string
		for i := start; i < end; i++ {
			wwords = append(wwords, words[i].word)
		}
		// page ranges are attached post-hoc by AttachChildPages (the window
		// builder stays page-agnostic so tests can run without a page map)
		out = append(out, childWindow{text: strings.Join(wwords, " ")})
		if end >= len(words) {
			break
		}
		// stride overlap: advance by (window - stride) tokens
		advance := ChildWindowTokens - ChildStrideTokens
		if advance < 1 {
			advance = 1
		}
		start += advance
	}
	return out
}

// AttachChildPages maps each child's first/last word line to its page via
// the stitch page map. Called by the embedder after ChunkHierarchical when
// a page map exists; children carry nil ranges without one.
func AttachChildPages(markdown string, pages []int, children []ChildChunk) {
	if len(pages) == 0 {
		return
	}
	lines := strings.Split(markdown, "\n")
	for ci := range children {
		child := &children[ci]
		// find the child's words in the line stream to recover line bounds;
		// matching by normalized token sequence keeps this robust to the
		// heading-tail prefix.
		childWords := strings.Fields(child.Text)
		if len(childWords) == 0 {
			continue
		}
		first, last := findWordSpan(lines, childWords)
		if first < 0 {
			continue
		}
		if first < len(pages) {
			child.PageStart = intPtr(pages[first])
		}
		if last < len(pages) {
			child.PageEnd = intPtr(pages[last])
		}
	}
}

// findWordSpan locates the first and last line index carrying the child's
// word sequence (greedy prefix match on the first word, then scan).
func findWordSpan(lines []string, words []string) (int, int) {
	first := -1
	last := -1
	wi := 0
	for li, line := range lines {
		for _, w := range strings.Fields(line) {
			if wi < len(words) && w == words[wi] {
				if first < 0 {
					first = li
				}
				wi++
				if wi == len(words) {
					last = li
					return first, last
				}
			} else if wi > 0 && wi < len(words) {
				// tolerate mid-span lines that don't match word-by-word
				// (punctuation splits): track the last line that matched
				last = li
			}
		}
	}
	if first >= 0 && last < 0 {
		last = first
	}
	return first, last
}

// DropDuplicateChunks keeps the first occurrence of every chunk hash across
// the whole document (order preserved, seq renumbered densely so
// (doc_id, seq) stays contiguous). Adjacent dedupe is subsumed: any adjacent
// duplicate was already seen (BACKLOG R-24).
func DropDuplicateChunks[T any](chunks []T, hash func(T) string, setSeq func(*T, int)) []T {
	out := []T{}
	seen := make(map[string]struct{}, len(chunks))
	for i := range chunks {
		h := hash(chunks[i])
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		setSeq(&chunks[i], len(out))
		out = append(out, chunks[i])
	}
	return out
}
