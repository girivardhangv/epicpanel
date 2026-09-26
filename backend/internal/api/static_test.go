package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebUIHandlerThreeApps(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "PLATFORM")
	write("assets/app.js", "platform-js")
	write("admin/index.html", "WHM")
	write("admin/assets/admin.js", "admin-js")
	// customer bundle deliberately absent: pre-sub-app web bundles must
	// degrade to the platform SPA, not 404 the whole origin.
	write("customer-placeholder.txt", "x")

	h := newWebUIHandler(dir)
	if h == nil {
		t.Fatal("handler nil for valid web dir")
	}
	get := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := get("/"); code != 200 || body != "PLATFORM" {
		t.Fatalf("root: %d %q", code, body)
	}
	// Bare prefix 301s to the trailing-slash form: the sub-app router's
	// basename cannot match an empty remaining path (blank page).
	req301 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec301 := httptest.NewRecorder()
	h.ServeHTTP(rec301, req301)
	if rec301.Code != http.StatusMovedPermanently || rec301.Header().Get("Location") != "/admin/" {
		t.Fatalf("/admin bare: %d %q", rec301.Code, rec301.Header().Get("Location"))
	}
	if code, body := get("/admin/"); code != 200 || body != "WHM" {
		t.Fatalf("/admin/: %d %q", code, body)
	}
	// Deep link falls back to the sub-app's own index (WHM router), not root.
	if code, body := get("/admin/nodes"); code != 200 || body != "WHM" {
		t.Fatalf("/admin/nodes deep link: %d %q", code, body)
	}
	if code, body := get("/admin/assets/admin.js"); code != 200 || body != "admin-js" {
		t.Fatalf("admin asset: %d %q", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/admin.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hashed sub-app asset must be immutable-cached, got %q", cc)
	}
	// Missing sub-app bundle falls back to the platform SPA.
	if code, body := get("/customer/websites"); code != 200 || body != "PLATFORM" {
		t.Fatalf("/customer (bundle missing): %d %q", code, body)
	}
	// API paths are never shadowed.
	if code, _ := get("/v1/anything"); code != 404 {
		t.Fatalf("/v1 must 404 from the UI handler, got %d", code)
	}
	if code, _ := get("/healthz"); code != 404 {
		t.Fatalf("/healthz must 404 from the UI handler, got %d", code)
	}
}
