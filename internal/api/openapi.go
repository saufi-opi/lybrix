package api

import (
	_ "embed"
	"net/http"
)

// openapiJSON is the checked-in contract snapshot at
// services/web/openapi.json — served byte-identical at /openapi.json so
// `npm run generate-client` stays deterministic.
//
//go:embed openapi_snapshot.json
var openapiJSON []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openapiJSON)
}
