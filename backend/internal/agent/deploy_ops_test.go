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

// TestEnsureLaravelSqlite — a Laravel app on SQLite (gitignored database
// file) must not fail the deploy at composer's post-autoload-dump: the
// agent creates the file from the .env contract before composer runs.
func TestEnsureLaravelSqlite(t *testing.T) {
	// Default path.
	rel := t.TempDir()
	if err := os.WriteFile(filepath.Join(rel, ".env"),
		[]byte("APP_KEY=x\nDB_CONNECTION=sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureLaravelSqlite(rel)
	if _, err := os.Stat(filepath.Join(rel, "database", "database.sqlite")); err != nil {
		t.Fatalf("default sqlite file must be created: %v", err)
	}
	// Custom DB_DATABASE path.
	rel2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(rel2, ".env"),
		[]byte("DB_CONNECTION=sqlite\nDB_DATABASE=/tmp/bwrepro-custom.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureLaravelSqlite(rel2)
	if _, err := os.Stat("/tmp/bwrepro-custom.sqlite"); err != nil {
		t.Fatalf("absolute DB_DATABASE must be honored: %v", err)
	}
	os.Remove("/tmp/bwrepro-custom.sqlite")
	// Existing file is never touched.
	rel3 := t.TempDir()
	if err := os.WriteFile(filepath.Join(rel3, ".env"), []byte("DB_CONNECTION=sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(rel3, "database"), 0o755)
	if err := os.WriteFile(filepath.Join(rel3, "database", "database.sqlite"), []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureLaravelSqlite(rel3)
	if b, _ := os.ReadFile(filepath.Join(rel3, "database", "database.sqlite")); string(b) != "DATA" {
		t.Fatal("existing sqlite file must not be overwritten")
	}
	// Not sqlite: nothing created.
	rel4 := t.TempDir()
	if err := os.WriteFile(filepath.Join(rel4, ".env"), []byte("DB_CONNECTION=mysql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureLaravelSqlite(rel4)
	if _, err := os.Stat(filepath.Join(rel4, "database")); !os.IsNotExist(err) {
		t.Fatal("non-sqlite apps must stay untouched")
	}
}

// TestCarryOverSqliteData — SQLite databases live under database/ and are
// never in git; a new release must inherit the live app's data.
func TestCarryOverSqliteData(t *testing.T) {
	base := t.TempDir()
	oldRel := filepath.Join(base, "old")
	newRel := filepath.Join(base, "new")
	for _, d := range []string{
		filepath.Join(oldRel, "database"),
		filepath.Join(oldRel, "uploads"),
		newRel,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldRel, ".env"), []byte("APP_KEY=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRel, "database", "database.sqlite"), []byte("REALDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The carry-over reads the CURRENT release through the public symlink.
	link := filepath.Join(base, "public")
	if err := os.Symlink(oldRel, link); err != nil {
		t.Fatal(err)
	}
	carryOverFromCurrent(link, newRel)
	if b, err := os.ReadFile(filepath.Join(newRel, ".env")); err != nil || string(b) != "APP_KEY=x" {
		t.Fatalf(".env carry-over: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(newRel, "database", "database.sqlite")); err != nil || string(b) != "REALDATA" {
		t.Fatalf("sqlite data carry-over: %v", err)
	}
}
