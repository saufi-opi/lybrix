// heading_hierarchy.go tracks markdown heading nesting so the heading-aware
// tier can attach a breadcrumb like "# Top > ## Section > ### Subsection" to
// each chunk.
//
// This is deliberately separate from header_tracker.go, which tracks table
// headers via start/end pattern hooks. A markdown heading has no explicit end
// marker — it is ended by the next heading of equal or shallower depth — so the
// hook abstraction does not fit and an explicit level stack is used instead.
package chunker

import "strings"

// HeadingHierarchy maintains a stack of active markdown headings indexed by
// level (1..6). Pushing a level-N heading clears every entry at level >= N,
// because the previous siblings and descendants are no longer in scope.
type HeadingHierarchy struct {
	// stack[i] holds the heading text for level i+1 (stack[0] = H1). Entries
	// beyond the deepest active level are empty.
	stack [6]string
	depth int
}

// NewHeadingHierarchy returns an empty hierarchy.
func NewHeadingHierarchy() *HeadingHierarchy { return &HeadingHierarchy{} }

// Observe parses line and updates the hierarchy when it is a markdown heading,
// returning (level, text) or (0, "") otherwise.
//
// Headings that merely look like headings inside a fenced code block are NOT
// detected here — callers must not feed fence content to Observe (the heading
// tier tracks fences itself).
func (h *HeadingHierarchy) Observe(line string) (int, string) {
	m := MarkdownHeadingPattern.FindStringSubmatch(line)
	if m == nil {
		return 0, ""
	}
	level := len(m[1])
	if level < 1 || level > 6 {
		return 0, ""
	}
	heading := strings.TrimSpace(m[2])
	h.stack[level-1] = heading
	for i := level; i < 6; i++ {
		h.stack[i] = ""
	}
	if level > h.depth {
		h.depth = level
	} else {
		// Pushing a shallower heading can shrink the active depth.
		h.depth = 0
		for i := 0; i < 6; i++ {
			if h.stack[i] != "" {
				h.depth = i + 1
			}
		}
	}
	return level, heading
}

// Breadcrumb returns the active path joined by " > ", e.g.
// "Chapter 1 > Section 2 > Subsection a", or "" when no headings are active.
func (h *HeadingHierarchy) Breadcrumb() string {
	if h.depth == 0 {
		return ""
	}
	parts := make([]string, 0, h.depth)
	for i := 0; i < h.depth; i++ {
		if h.stack[i] != "" {
			parts = append(parts, h.stack[i])
		}
	}
	return strings.Join(parts, " > ")
}

// BreadcrumbWithHashes returns the path with its original `#` prefixes, suitable
// for embedding into chunk content as a context header:
//
//	"# Chapter 1\n## Section 2\n### Subsection a"
func (h *HeadingHierarchy) BreadcrumbWithHashes() string {
	if h.depth == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < h.depth; i++ {
		if h.stack[i] == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(strings.Repeat("#", i+1))
		sb.WriteByte(' ')
		sb.WriteString(h.stack[i])
	}
	return sb.String()
}

// Depth returns the deepest active heading level (0 when none).
func (h *HeadingHierarchy) Depth() int { return h.depth }

// Reset clears all state.
func (h *HeadingHierarchy) Reset() {
	for i := range h.stack {
		h.stack[i] = ""
	}
	h.depth = 0
}

// mergeBreadcrumbs combines a parent and child breadcrumb into one. When the
// child re-ran heading detection over the parent's content, its first breadcrumb
// line typically duplicates the parent's last line (the parent's leading heading
// sits at the top of the child's input); that duplicate is dropped so the
// embedding context is not redundant.
func mergeBreadcrumbs(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	parentLines := strings.Split(parent, "\n")
	childLines := strings.Split(child, "\n")
	if len(parentLines) > 0 && len(childLines) > 0 &&
		strings.TrimSpace(parentLines[len(parentLines)-1]) == strings.TrimSpace(childLines[0]) {
		childLines = childLines[1:]
	}
	if len(childLines) == 0 {
		return parent
	}
	return parent + "\n" + strings.Join(childLines, "\n")
}

// commonHeadingPrefix returns the leading whole lines shared by two breadcrumbs,
// used when coalescing adjacent small chunks under one heading context.
func commonHeadingPrefix(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	n := 0
	for n < len(aLines) && n < len(bLines) && aLines[n] == bLines[n] {
		n++
	}
	if n == 0 {
		return ""
	}
	return strings.Join(aLines[:n], "\n")
}
