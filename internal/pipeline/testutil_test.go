package pipeline

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func osWrite(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }

func jsonRead(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func stringsN(n int) string {
	words := make([]string, 0, n)
	for i := 0; i < n; i++ {
		words = append(words, "t")
	}
	return strings.Join(words, " ")
}
