package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openAPISpec []byte

// GET /v1/openapi.json — machine-readable API contract (no auth).
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPISpec)
}
