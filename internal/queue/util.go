package queue

import (
	"crypto/rand"
	"encoding/hex"
)

func randomHex8() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on Linux; fall back to a fixed suffix
		return "00000000"
	}
	return hex.EncodeToString(b)
}
