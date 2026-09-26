package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckReleaseWebDir — the running-directory gate: empty = release root
// (no gate), existing dir passes, a typo fails with an actionable error.
func TestCheckReleaseWebDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkReleaseWebDir(base, ""); err != nil {
		t.Fatalf("empty web dir must pass: %v", err)
	}
	if err := checkReleaseWebDir(base, "/public/"); err != nil {
		t.Fatalf("existing web dir must pass: %v", err)
	}
	err := checkReleaseWebDir(base, "publc")
	if err == nil || !strings.Contains(err.Error(), "web_dir") {
		t.Fatalf("missing web dir must fail with an actionable error, got %v", err)
	}
}

// TestEffectiveDocrootWebDirComposition — the deploy running directory
// resolves UNDER the web root (<site>/public/<web_dir>), which follows the
// release symlink after the first deploy; traversal stays refused.
func TestEffectiveDocrootWebDirComposition(t *testing.T) {
	base := t.TempDir()
	got, err := effectiveDocroot(base, "public/public", true)
	if err != nil {
		t.Fatalf("public/public: %v", err)
	}
	if want := filepath.Join(base, "public", "public"); got != want {
		t.Fatalf("docroot = %q, want %q", got, want)
	}
	if got, err := effectiveDocroot(base, "", true); err != nil || got != filepath.Join(base, "public") {
		t.Fatalf("empty suffix must give the standard public dir: %q %v", got, err)
	}
	// Traversal is neutralized by anchoring (Clean("/"+suffix) absorbs the
	// ".." segments above the root): "../../etc" resolves INSIDE the tree.
	got, err = effectiveDocroot(base, "../../etc", true)
	if err != nil || got != filepath.Join(base, "etc") {
		t.Fatalf("traversal must be anchored inside the site tree: %q %v", got, err)
	}

	// Non-materialized (deploy-managed) resolution: path computed, NOTHING
	// created on disk — no manufactured empty folders with default indexes.
	fresh := t.TempDir()
	got, err = effectiveDocroot(fresh, "public/public", false)
	if err != nil {
		t.Fatalf("non-materialized resolve: %v", err)
	}
	if want := filepath.Join(fresh, "public", "public"); got != want {
		t.Fatalf("non-materialized path = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("non-materialized docroot must not exist on disk, err=%v", err)
	}
}

// TestReleaseLocationAndValidation — releases live INSIDE the site tree so
// the customer's cloned files are visible in the file manager, SSH sandbox,
// and disk accounting; legacy external release dirs stay valid for rollback.
func TestReleaseLocationAndValidation(t *testing.T) {
	id := "55555555-5555-5555-5555-555555555555"
	want := "/srv/epicpanel/websites/" + id + "/releases"
	if got := siteReleasesDir(id); got != want {
		t.Fatalf("siteReleasesDir = %q, want %q", got, want)
	}
	if !validReleaseTarget(id, want+"/20260927-000000-abc") {
		t.Fatal("in-tree release dir must be valid")
	}
	if !validReleaseTarget(id, "/srv/epicpanel/releases/"+id+"/old-release") {
		t.Fatal("legacy release dir must stay valid (rollback of old deploys)")
	}
	if validReleaseTarget(id, "/srv/epicpanel/releases/other-site/rel") {
		t.Fatal("another site's release dir must be rejected")
	}
	if validReleaseTarget(id, "/srv/epicpanel/websites/"+id+"/public") {
		t.Fatal("non-release paths must be rejected")
	}
}
