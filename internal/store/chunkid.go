package store

import (
	"crypto/sha1"
	"encoding/hex"
)

// uuidNamespaceURL is RFC-4122's URL namespace: 6ba7b811-9dad-11d1-80b4-00c04fd430c8.
var uuidNamespaceURL = []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// DeterministicChunkID derives a stable UUIDv5 from doc_id AND chunk_hash so
// re-embedding upserts, never duplicates — and two documents sharing
// identical normalized text never collide on one id (R-12: chunk_hash is
// unique only per (doc_id, chunk_hash)). Mirrors 1.0's point_id_for.
func DeterministicChunkID(docID, chunkHash string) string {
	h := sha1.New()
	h.Write(uuidNamespaceURL)
	h.Write([]byte("chunk:" + docID + ":" + chunkHash))
	b := h.Sum(nil)
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hx := hex.EncodeToString(b)
	return hx[0:8] + "-" + hx[8:12] + "-" + hx[12:16] + "-" + hx[16:20] + "-" + hx[20:32]
}
