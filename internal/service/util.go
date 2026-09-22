package service

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	"github.com/saufi-opi/lybrix/internal/store"
)

// uuidNamespaceURL is RFC-4122's URL namespace: 6ba7b811-9dad-11d1-80b4-00c04fd430c8.
var uuidNamespaceURL = uuidBytes(0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8)

func uuidBytes(bs ...byte) []byte { return bs }

// jobInt pulls an int job field (JSON numbers decode as float64).
func jobInt(job map[string]any, key string) (int, bool) {
	v, ok := job[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// derefInt dereferences an optional int (nil → 0).
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// strPtr/intPtr are optional-pointer shorthands for events.
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// shortID returns 8 random hex chars for worker identity.
func shortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}

var _ = fmt.Sprintf

// docCollection returns the doc's collection id ("" when NULL).
func docCollection(doc *store.Document) string {
	if doc.CollectionID == nil {
		return ""
	}
	return *doc.CollectionID
}

// deterministicUUID derives a stable UUIDv5 from doc_id AND chunk_hash so
// re-embedding upserts, never duplicates — and two documents sharing
// identical normalized text never collide (R-12: chunk_hash is unique only
// per (doc_id, chunk_hash)). Mirrors 1.0's point_id_for.
func deterministicUUID(docID, chunkHash string) string {
	return uuidv5("chunk:" + docID + ":" + chunkHash)
}

// uuidv5 computes RFC-4122 v5 in the URL namespace (1.0 parity).
func uuidv5(name string) string {
	h := sha1.New()
	h.Write(uuidNamespaceURL)
	h.Write([]byte(name))
	b := h.Sum(nil)
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hx := hex.EncodeToString(b)
	return hx[0:8] + "-" + hx[8:12] + "-" + hx[12:16] + "-" + hx[16:20] + "-" + hx[20:32]
}
