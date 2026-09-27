package agent

import (
	"strings"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Repro: re-provision (serving converge) of a DEPLOYED site with web_dir
// set — public is a symlink to <site>/releases/<ts> (releases inside the
// tree). web_server=none keeps root-only steps out; the filesystem logic
// is exactly what runs on the node.
func TestProvisionWebDirRepro(t *testing.T) {
	e := NewExecutor()
	base := t.TempDir()
	e.docRootBase = base
	e.chownFn = func(string, int, int) error { return nil } // no root in tests
	siteID := "6e443b6c-0fb6-45a0-a7b2-1b887fc68e2a"
	siteBase := filepath.Join(base, siteID)
	rel := filepath.Join(siteBase, "releases", "20260926-202741-YTwvGph5")
	pub := filepath.Join(rel, "public")
	for _, d := range []string{pub, filepath.Join(rel, "app"), siteBase} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Deployed state: public -> release (symlink), logs/ tmp/ real dirs.
	if err := os.Symlink(rel, filepath.Join(siteBase, "public")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(siteBase, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	// ensureUnixUser would useradd (root-only); create the group+user-shaped
	// lookup hit by pointing the executor at an existing user is not possible
	// without root — so bypass via an existing name the validator accepts.
	// "www-data" is explicitly allowed by validUnixUserName and exists.
	_, err := e.ProvisionWebsite(t.Context(), ProvisionPayload{
		WebsiteID:      siteID,
		Organization:   "00000000-0000-0000-0000-000000000001",
		Name:           "repro",
		UnixUser:       "www-data", // explicitly allowed by validUnixUserName; exists on every distro
		Runtime:        "static",
		WebServer:      "none",
		WebDir:         "public",
		PrimaryDomain:  "repro.example.test",
		RuntimeVersion: "8.3",
	})
	// Contract: with web_dir set on a DEPLOYED site (public -> release
	// symlink), everything up to the root-only steps must succeed. Without
	// root the run legitimately stops at the first privileged write
	// (chown is stubbed; the nginx default-vhost write is not). Any OTHER
	// error (docroot resolution, charset, grants, placeholder) is a real
	// regression.
	if err != nil && !strings.Contains(err.Error(), "install nginx") {
		t.Fatalf("provision with web_dir failed before the root boundary: %v", err)
	}
	_ = time.Now
}
