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
	got, err := effectiveDocroot(base, "public/public")
	if err != nil {
		t.Fatalf("public/public: %v", err)
	}
	if want := filepath.Join(base, "public", "public"); got != want {
		t.Fatalf("docroot = %q, want %q", got, want)
	}
	if got, err := effectiveDocroot(base, ""); err != nil || got != filepath.Join(base, "public") {
		t.Fatalf("empty suffix must give the standard public dir: %q %v", got, err)
	}
	// Traversal is neutralized by anchoring (Clean("/"+suffix) absorbs the
	// ".." segments above the root): "../../etc" resolves INSIDE the tree.
	got, err = effectiveDocroot(base, "../../etc")
	if err != nil || got != filepath.Join(base, "etc") {
		t.Fatalf("traversal must be anchored inside the site tree: %q %v", got, err)
	}
}
