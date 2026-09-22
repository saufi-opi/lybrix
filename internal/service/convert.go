package service

import (
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/store"
)

// toStoreParents maps pipeline parents onto store rows (deterministic
// UUIDv5 ids from doc_id+hash, like 1.0's point ids).
func toStoreParents(parents []pipeline.ParentChunk) []store.ParentChunk {
	out := make([]store.ParentChunk, 0, len(parents))
	for _, p := range parents {
		out = append(out, store.ParentChunk{
			ID:               "",
			Seq:              p.Seq,
			PageStart:        p.PageStart,
			PageEnd:          p.PageEnd,
			HeadingPath:      p.HeadingPath,
			HeaderBreadcrumb: nil,
			Text:             p.Text,
			TokenCount:       p.TokenCount,
			ChunkHash:        p.ChunkHash,
		})
	}
	return out
}

// toStoreChildren maps pipeline children onto store rows.
func toStoreChildren(children []pipeline.ChildChunk) []store.ChildChunk {
	out := make([]store.ChildChunk, 0, len(children))
	for _, c := range children {
		out = append(out, store.ChildChunk{
			ID:               "",
			ParentID:         nil,
			Seq:              c.Seq,
			PageStart:        c.PageStart,
			PageEnd:          c.PageEnd,
			HeadingPath:      c.HeadingPath,
			HeaderBreadcrumb: strPtr2(c.HeaderBreadcrumb),
			Text:             c.Text,
			TokenCount:       c.TokenCount,
			ChunkHash:        c.ChunkHash,
		})
	}
	return out
}

func strPtr2(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
