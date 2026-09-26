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
//
// Three UIs share the one origin (WHM/cPanel style, one port):
//   - /            platform panel   (frontend/dist)
//   - /admin/*     WHM experience   (frontend/apps/admin/dist, vite base /admin/)
//   - /customer/*  cPanel experience(frontend/apps/customer/dist, vite base /customer/)
//
// Sub-apps degrade gracefully: a bundle built before they existed falls
// through to the platform SPA (which renders its own unknown-route UI).
func newWebUIHandler(webDir string) http.Handler {
	if webDir == "" {
		return nil
	}
	index := filepath.Join(webDir, "index.html")
	if info, err := os.Stat(index); err != nil || info.IsDir() {
		return nil
	}
	root := &spaApp{prefix: "", dir: webDir, index: index}
	subs := []*spaApp{}
	for _, a := range []struct{ prefix, sub string }{{"/admin", "admin"}, {"/customer", "customer"}} {
		subDir := filepath.Join(webDir, a.sub)
		subIndex := filepath.Join(subDir, "index.html")
		if info, err := os.Stat(subIndex); err == nil && !info.IsDir() {
			subs = append(subs, &spaApp{prefix: a.prefix, dir: subDir, index: subIndex})
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/v1/"),
			p == "/v1",
			p == "/healthz", p == "/readyz", p == "/metrics":
			http.NotFound(w, r)
			return
		}
		for _, app := range subs {
			if p == app.prefix || strings.HasPrefix(p, app.prefix+"/") {
				app.serve(w, r)
				return
			}
		}
		root.serve(w, r)
	})
}

// spaApp serves one built SPA from dir, falling back to its index.html for
// client-side routes. prefix "" serves from the origin root.
type spaApp struct {
	prefix string
	dir    string
	index  string
}

func (a *spaApp) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if a.prefix != "" {
		p = strings.TrimPrefix(p, a.prefix)
		if p == "" {
			p = "/"
		}
	}
	if p == "/" || p == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, a.index)
		return
	}
	// Serve the file when it exists (hashed bundle assets); SPA fallback
	// otherwise (react-router deep links like /setup?token=…).
	full := filepath.Join(a.dir, filepath.Clean("/"+p))
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		if strings.HasPrefix(p, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, full)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, a.index)
}

// mountWebUI attaches the SPA catch-all when a web dir is configured.
func (s *Server) mountWebUI(mux *http.ServeMux) {
	if h := newWebUIHandler(s.Cfg.WebDir); h != nil {
		// "GET /" also serves HEAD implicitly (net/http registers both).
		mux.Handle("GET /", h)
	}
}
