package service

import (
	"github.com/saufi-opi/lybrix/internal/chunker"
	"github.com/saufi-opi/lybrix/internal/store"
)

// toStoreParents maps chunker parents onto store rows. Parent id is left empty;
// the store derives it deterministically from (doc_id, chunk_hash).
func toStoreParents(parents []chunker.Chunk) []store.ParentChunk {
	out := make([]store.ParentChunk, 0, len(parents))
	for _, p := range parents {
		out = append(out, store.ParentChunk{
			ID:               "",
			Seq:              p.Seq,
			PageStart:        p.PageStart,
			PageEnd:          p.PageEnd,
			HeadingPath:      headingPathFromBreadcrumb(p.ContextHeader),
			HeaderBreadcrumb: nil,
			Text:             p.Content,
			TokenCount:       chunker.ApproxTokenCount(p.Content, chunker.LangMixed),
			ChunkHash:        p.ChunkHash(),
		})
	}
	return out
}

// toStoreChildren maps chunker children onto store rows. ParentID is resolved by
// the caller once the surviving parent set is known (a parent removed by dedupe
// must not be referenced).
func toStoreChildren(children []chunker.ChildChunk) []store.ChildChunk {
	out := make([]store.ChildChunk, 0, len(children))
	for _, c := range children {
		out = append(out, store.ChildChunk{
			ID:               "",
			ParentID:         nil,
			Seq:              c.Seq,
			PageStart:        c.PageStart,
			PageEnd:          c.PageEnd,
			HeadingPath:      headingPathFromBreadcrumb(c.ContextHeader),
			HeaderBreadcrumb: strPtr2(c.ContextHeader),
			Text:             c.Content,
			TokenCount:       chunker.ApproxTokenCount(c.Content, chunker.LangMixed),
			ChunkHash:        c.ChunkHash(),
		})
	}
	return out
}

// headingPathFromBreadcrumb recovers the heading path list from a breadcrumb.
//
// The chunker emits ContextHeader as markdown heading lines
// ("# Chapter 1\n## Section 2"); the store keeps the same information as a
// []string path, which the UI and the eval harness read for citations.
func headingPathFromBreadcrumb(breadcrumb string) []string {
	if breadcrumb == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(breadcrumb); i++ {
		if i == len(breadcrumb) || breadcrumb[i] == '\n' {
			line := trimHeadingPrefix(breadcrumb[start:i])
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trimHeadingPrefix strips a leading run of '#' characters and the space after
// them, leaving the heading text.
func trimHeadingPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	end := len(line)
	for end > i && (line[end-1] == ' ' || line[end-1] == '\t' || line[end-1] == '\r') {
		end--
	}
	return line[i:end]
}

func strPtr2(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
