// Package web embeds the built EpicPanel SPA so the panel ships as a single
// binary. CI copies frontend/dist/* into ./dist before building a release; a
// placeholder index.html is committed so the embed always compiles. Set
// EPICPANEL_DIST_DIR to serve an external build instead (development).
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

const cspHeader = "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; frame-ancestors 'none'"

// Handler serves the embedded SPA with an index.html fallback for client
// routes. Unknown /api/ paths return the JSON error envelope.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "NOT_FOUND", "message": "route not found"},
			})
			return
		}

		upath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if upath == "" || upath == "." {
			upath = "index.html"
		}
		if info, statErr := fs.Stat(sub, upath); statErr == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// Not a real asset → SPA route.
		w.Header().Set("Content-Security-Policy", cspHeader)
		http.ServeFileFS(w, r, sub, "index.html")
	})
}
