package httpapi

import (
	"net/http"
	"strings"
)

// CORSMiddleware allows the configured browser origins (comma-separated in
// EPICPANEL_CORS_ORIGINS) to call the API with credentials (session cookies).
// A literal "*" is REFUSED for credentialed CORS — reflecting arbitrary
// origins with Allow-Credentials defeats the browser's same-origin policy
// (audit finding); list explicit origins instead.
func CORSMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o != "" && o != "*" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
