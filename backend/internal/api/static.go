package api

// Static UI serving — the API binary hosts the built panel (frontend/dist)
// so a VPS install is one port, one process: http://IP:8080 serves the SPA,
// which calls the same-origin /v1 API. Enabled when EPICPANEL_WEB_DIR points
// at a directory containing index.html; otherwise API-only (dev mode).
//
// Rules:
//   - API paths are never shadowed: /v1/*, /healthz, /readyz, /metrics keep
//     their handlers (they are registered BEFORE the "GET /" catch-all, and
//     this handler refuses them defensively anyway).
//   - Unknown paths fall back to index.html (react-router SPA).
//   - Hashed assets (assets/*) are immutable-cached; index.html is no-cache.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// newWebUIHandler returns the SPA static handler, or nil when the web dir is
// not configured/valid (API-only mode).
func newWebUIHandler(webDir string) http.Handler {
	if webDir == "" {
		return nil
	}
	index := filepath.Join(webDir, "index.html")
	if info, err := os.Stat(index); err != nil || info.IsDir() {
		return nil
	}
	fs := http.StripPrefix("/", http.FileServer(http.Dir(webDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/v1/"),
			p == "/v1",
			p == "/healthz", p == "/readyz", p == "/metrics":
			http.NotFound(w, r)
			return
		}
		if p == "/" || p == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
			return
		}
		// Serve the file when it exists (hashed bundle assets); SPA fallback
		// otherwise (react-router deep links like /setup?token=…).
		full := filepath.Join(webDir, filepath.Clean("/"+p))
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			if strings.HasPrefix(p, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fs.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// mountWebUI attaches the SPA catch-all when a web dir is configured.
func (s *Server) mountWebUI(mux *http.ServeMux) {
	if h := newWebUIHandler(s.Cfg.WebDir); h != nil {
		// "GET /" also serves HEAD implicitly (net/http registers both).
		mux.Handle("GET /", h)
	}
}
