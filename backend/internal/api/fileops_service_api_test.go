package api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// newOpsSite: admin client + org + server + a provisioned site (job completed).
func newOpsSite(t *testing.T, prefix string) (admin *testClient, orgID, websiteID, serverID string) {
	t.Helper()
	_, admin = newTestServer(t)
	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": prefix + "-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": prefix + "-org"})
	orgID, _ = resp.body["id"].(string)
	agent := admin.NewClient()
	_, websiteID = newPHPTestSite(t, admin, agent, orgID)
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers", nil)
	if list, ok := resp.body["servers"].([]any); ok && len(list) > 0 {
		serverID, _ = list[0].(map[string]any)["id"].(string)
	}
	return admin, orgID, websiteID, serverID
}

// Reconcile: admin gets 202 + job id; developer is refused (admin+).
func TestReconcileEndpoint(t *testing.T) {
	admin, orgID, websiteID, _ := newOpsSite(t, "rec")
	dev := admin.NewClient()
	dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "rec-dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "rec-dev@example.test", "role": "developer",
	})
	reconBase := "/v1/organizations/" + orgID + "/websites/" + websiteID + "/reconcile"

	resp := dev.do("POST", reconBase, nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("developer reconcile must 403, got %d", resp.status)
	}
	resp = admin.do("POST", reconBase, nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("admin reconcile must 202, got %d %v", resp.status, resp.body)
	}
	if _, ok := resp.body["job_id"]; !ok {
		t.Fatalf("expected job_id: %v", resp.body)
	}
}

// Service restart: strict allowlist (422), known service 202 + job,
// developer refused (platform-admin only surface).
func TestServiceRestartEndpoint(t *testing.T) {
	admin, orgID, _, serverID := newOpsSite(t, "svc")
	svcBase := "/v1/organizations/" + orgID + "/servers/" + serverID + "/services"

	resp := admin.do("POST", svcBase+"/rm-rf/restart", nil)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown service must 422, got %d", resp.status)
	}
	resp = admin.do("POST", svcBase+"/nginx/restart", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("nginx restart must 202, got %d %v", resp.status, resp.body)
	}
	if _, ok := resp.body["job_id"]; !ok {
		t.Fatalf("expected job_id: %v", resp.body)
	}
	resp = admin.do("POST", svcBase+"/php8.3-fpm/restart", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("php8.3-fpm restart must 202, got %d %v", resp.status, resp.body)
	}

	dev := admin.NewClient()
	dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "svc-dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "svc-dev@example.test", "role": "developer",
	})
	resp = dev.do("POST", svcBase+"/nginx/restart", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("developer service restart must 403, got %d", resp.status)
	}
}

// Full file-op round trip through the API: upload zip → extract → copy →
// compress → extract the produced zip. SiteRoot is repointed at a temp dir
// so the live /srv tree is never touched.
func TestFileOpsExtractCopyCompress(t *testing.T) {
	tmp := t.TempDir()
	old := websites.SiteRoot
	websites.SiteRoot = tmp
	defer func() { websites.SiteRoot = old }()

	admin, orgID, websiteID, _ := newOpsSite(t, "fm")
	// The provision job is faked in tests — create the site dir the real
	// provisioning would have made under the repointed SiteRoot.
	if err := os.MkdirAll(filepath.Join(tmp, websiteID), 0o755); err != nil {
		t.Fatal(err)
	}
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID + "/files"

	// Build a zip in-memory and upload it.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"pkg/index.html", "<h1>hi</h1>"},
		{"pkg/assets/app.css", "body{}"},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	resp := admin.doUpload(base+"/upload?path=/", "pkg.zip", buf.Bytes())
	if resp.status != http.StatusCreated {
		t.Fatalf("upload: %d %v", resp.status, resp.body)
	}

	// Extract.
	resp = admin.do("POST", base+"/extract", map[string]any{"path": "pkg.zip", "to": "site"})
	if resp.status != http.StatusOK {
		t.Fatalf("extract: %d %v", resp.status, resp.body)
	}
	if n, _ := resp.body["entries"].(float64); n != 2 {
		t.Fatalf("expected 2 entries, got %v", resp.body)
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "site", "pkg", "index.html")); err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}

	// Copy.
	resp = admin.do("POST", base+"/copy", map[string]any{"path": "site/pkg", "to": "site/pkg-copy"})
	if resp.status != http.StatusCreated {
		t.Fatalf("copy: %d %v", resp.status, resp.body)
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "site", "pkg-copy", "assets", "app.css")); err != nil {
		t.Fatalf("copied file missing: %v", err)
	}

	// Copy into itself rejected; destination conflict rejected.
	resp = admin.do("POST", base+"/copy", map[string]any{"path": "site/pkg", "to": "site/pkg/inner"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("copy-into-self must 422, got %d", resp.status)
	}
	resp = admin.do("POST", base+"/copy", map[string]any{"path": "site/pkg", "to": "site/pkg-copy"})
	if resp.status != http.StatusConflict {
		t.Fatalf("existing destination must 409, got %d", resp.status)
	}

	// Compress.
	resp = admin.do("POST", base+"/compress", map[string]any{"paths": []string{"site/pkg"}, "to": "backup"})
	if resp.status != http.StatusCreated {
		t.Fatalf("compress: %d %v", resp.status, resp.body)
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "backup.zip")); err != nil {
		t.Fatalf("zip missing: %v", err)
	}

	// Extract the produced zip (round trip).
	resp = admin.do("POST", base+"/extract", map[string]any{"path": "backup.zip", "to": "restored"})
	if resp.status != http.StatusOK {
		t.Fatalf("zip round-trip extract: %d %v", resp.status, resp.body)
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "restored", "site", "pkg", "index.html")); err != nil {
		t.Fatalf("round-trip file missing: %v", err)
	}

	// Zip-slip through the API: an archive with a traversal entry must fail
	// and leave nothing outside the extraction dir.
	var evil bytes.Buffer
	ezw := zip.NewWriter(&evil)
	ew, err := ezw.Create("../escaped.txt")
	if err != nil {
		t.Fatal(err)
	}
	ew.Write([]byte("pwned"))
	ezw.Close()
	resp = admin.doUpload(base+"/upload?path=/", "evil.zip", evil.Bytes())
	if resp.status != http.StatusCreated {
		t.Fatalf("evil upload: %d %v", resp.status, resp.body)
	}
	resp = admin.do("POST", base+"/extract", map[string]any{"path": "evil.zip", "to": "site"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("zip-slip extract must 422, got %d", resp.status)
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "escaped.txt")); err == nil {
		t.Fatal("zip-slip file must not exist")
	}
	if _, err := os.Stat(filepath.Join(tmp, websiteID, "site", "escaped.txt")); err == nil {
		t.Fatal("zip-slip file must not land in the target dir either")
	}
}
