package httpapi

import (
	"net/http"
	"strings"
)

// APIPrefixRewrite makes /api/v1/... the canonical public API prefix (master
// doc) while keeping the historical /v1/... routes working: /api/v1 requests
// are rewritten to the /v1 handlers before routing. One decision, both
// prefixes valid, zero route duplication.
func APIPrefixRewrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
		}
		next.ServeHTTP(w, r)
	})
}
