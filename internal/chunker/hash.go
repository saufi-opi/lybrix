package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashText is the chunk hash: sha256 over whitespace-normalized text.
//
// The normalization collapses every whitespace run to a single space, so a hash
// is stable across pure reformatting — a chunk whose WORD SEQUENCE is unchanged
// hashes the same whether its text is verbatim markdown or a flattened stream.
// That property is real but narrow: measured on a heading-bearing document,
// essentially no chunk keeps its hash across the chunker rewrite, because the
// old chunker prepended the heading text without its "#" marker while the new
// Content includes the heading line verbatim. Only heading-less spans whose
// words happen to line up are unchanged.
//
// The practical consequence is that re-chunking a document produces mostly NEW
// hashes, so it must be delete-then-insert, never ON CONFLICT DO NOTHING:
// skipping the writes would leave the old rows behind as searchable ghosts and
// pair stale text with a fresh embedding. See ResetDocForRechunk and the
// delete-before-insert in HandleEmbed.
//
// Do not change this to hash the raw text without a matching migration: it would
// change every existing chunk id.
func HashText(text string) string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(h[:])
}

// DropDuplicateChunks keeps the first occurrence of every chunk hash across the
// whole document, preserving order and renumbering seq densely so (doc_id, seq)
// stays contiguous.
//
// Document-wide rather than adjacent-only: repeated boilerplate, footers and
// page furniture stitched across shard boundaries reach COPY as duplicate
// hashes and previously aborted the transaction (BACKLOG R-24). Adjacent
// dedupe is subsumed — any adjacent duplicate was already seen.
func DropDuplicateChunks[T any](chunks []T, hash func(T) string, setSeq func(*T, int)) []T {
	out := make([]T, 0, len(chunks))
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

// RenumberGlobal assigns one dense, monotone seq across BOTH parent and child
// sets: parents first in document order, then children. Both slices must already
// be in document order (SplitParentChild guarantees it).
//
// This exists because the two sets are deduped independently, so each ends up
// numbered 0..N-1 and the sequences collide. ChunkNeighbours matches on seq
// without filtering is_parent, and the REST chunk detail derives prev/next from
// seq±1, so a collision returns the wrong kind of chunk (BACKLOG R-28).
//
// Returns the number of chunks numbered.
func RenumberGlobal(parents []Chunk, children []ChildChunk) int {
	seq := 0
	for i := range parents {
		parents[i].Seq = seq
		seq++
	}
	for i := range children {
		children[i].Seq = seq
		seq++
	}
	return seq
}
