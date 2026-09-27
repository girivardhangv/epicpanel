package deployments

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunningDirOptionsFor — the dropdown source resolution: exact listing
// from the workdir when the site tree exists, legacy public fallback for
// mid-migration sites, framework presets when nothing is listable. Dotfiles
// (.git!) never surface as running directories.
func TestRunningDirOptionsFor(t *testing.T) {
	// No site tree → presets with the repo-root option first.
	opts, source := runningDirOptionsFor(filepath.Join(t.TempDir(), "missing"))
	if source != "presets" {
		t.Fatalf("source = %q, want presets", source)
	}
	if opts[0].Value != "" || opts[0].Label != "workdir (repo root)" {
		t.Fatalf("first option must be the workdir root, got %+v", opts[0])
	}
	found := map[string]bool{}
	for _, o := range opts {
		found[o.Value] = true
	}
	if !found["public"] || !found["dist"] {
		t.Fatalf("framework presets missing: %+v", opts)
	}

	// Workdir listing: repo root first, real dirs only, dotfiles skipped.
	base := t.TempDir()
	wd := filepath.Join(base, "workdir")
	for _, d := range []string{"public", "app", ".git"} {
		if err := os.MkdirAll(filepath.Join(wd, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wd, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, source = runningDirOptionsFor(base)
	if source != "workdir" {
		t.Fatalf("source = %q, want workdir", source)
	}
	vals := map[string]bool{}
	for _, o := range opts {
		vals[o.Value] = true
	}
	if !vals["public"] || !vals["app"] {
		t.Fatalf("workdir dirs missing from options: %+v", opts)
	}
	if vals[".git"] || vals["file.txt"] {
		t.Fatalf("dotfiles/files must not be options: %+v", opts)
	}
	if len(opts) == 0 || opts[0].Value != "" {
		t.Fatalf("repo root option must come first: %+v", opts)
	}

	// Legacy site mid-migration: public tree mirrors the repo.
	base2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base2, "public", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, source = runningDirOptionsFor(base2)
	if source != "workdir" {
		t.Fatalf("legacy fallback source = %q, want workdir", source)
	}
	if len(opts) != 2 || opts[1].Value != "web" {
		t.Fatalf("legacy public dirs must be listed: %+v", opts)
	}
}
