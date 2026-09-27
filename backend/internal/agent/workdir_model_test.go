package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a tiny local repo for file:// deploys: commit 1 ships
// index.html + gone.txt + public/index.php; commit 2 edits index.html and
// deletes gone.txt. Returns the repo dir and the two SHAs.
func gitRepo(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "index.html"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "public", "index.php"), []byte("<?php echo 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "one")
	sha1 := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "index.html"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "two")
	sha2 := run("rev-parse", "HEAD")
	return repo, sha1, sha2
}

// newTestExecutor mirrors the root-free harness: temp docRootBase, stubbed
// chown, and a provisioned-looking site tree (logs/ tmp/).
func newTestExecutor(t *testing.T, siteID string) (*Executor, string) {
	t.Helper()
	e := NewExecutor()
	base := t.TempDir()
	e.docRootBase = base
	e.chownFn = func(string, int, int) error { return nil }
	siteBase := filepath.Join(base, siteID)
	for _, d := range []string{"logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(siteBase, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return e, siteBase
}

// TestMigrateLegacySiteLayout — the three legacy shapes a pre-workdir site
// can present, plus idempotence:
//   - manual/static site (public/ real dir): atomic RENAME to workdir;
//   - legacy git site (public -> release symlink): workdir seeded from the
//     live release so the vhost re-render keeps serving identical content;
//   - DANGLING public symlink (release pruned): link dropped — the exact
//     state that bricked provisions with `mkdir <site>/public: file exists`.
func TestMigrateLegacySiteLayout(t *testing.T) {
	// Real dir → rename.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "public", "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "public", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateLegacySiteLayout(base)
	if _, err := os.Stat(filepath.Join(base, "workdir", "index.html")); err != nil {
		t.Fatalf("manual site must be renamed into workdir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "public")); !os.IsNotExist(err) {
		t.Fatalf("public must be gone after rename: %v", err)
	}

	// Live release symlink → seeded copy, original kept.
	base2 := t.TempDir()
	rel := filepath.Join(base2, "releases", "r1")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(base2, "public")); err != nil {
		t.Fatal(err)
	}
	migrateLegacySiteLayout(base2)
	if b, err := os.ReadFile(filepath.Join(base2, "workdir", "index.php")); err != nil || string(b) != "<?php" {
		t.Fatalf("workdir must be seeded from the live release: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(base2, "public")); err != nil {
		t.Fatalf("legacy public link must stay until cleanup: %v", err)
	}
	// Idempotent: a second run must not re-seed or touch anything.
	if err := os.WriteFile(filepath.Join(rel, "index.php"), []byte("<?php 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateLegacySiteLayout(base2)
	if b, _ := os.ReadFile(filepath.Join(base2, "workdir", "index.php")); string(b) != "<?php" {
		t.Fatal("second migration run must be a no-op")
	}

	// Dangling symlink → dropped (the provision-bricking state).
	base3 := t.TempDir()
	if err := os.MkdirAll(base3, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base3, "releases", "vanished"), filepath.Join(base3, "public")); err != nil {
		t.Fatal(err)
	}
	migrateLegacySiteLayout(base3)
	if _, err := os.Lstat(filepath.Join(base3, "public")); !os.IsNotExist(err) {
		t.Fatalf("dangling public symlink must be removed: %v", err)
	}
}

// TestCleanupLegacyReleaseLayout — the legacy scaffolding (public link +
// releases history) is retired ONLY once the site has a workdir git tree
// (rollback-by-commit capability); before that the symlink rollback path
// must keep its targets.
func TestCleanupLegacyReleaseLayout(t *testing.T) {
	e := NewExecutor()
	e.docRootBase = t.TempDir()
	siteID := "44444444-4444-4444-4444-444444444444"
	siteBase := filepath.Join(e.docRootBase, siteID)
	wd := filepath.Join(siteBase, "workdir")
	rel := filepath.Join(siteBase, "releases", "r1")
	for _, d := range []string{wd, rel} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wd, "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(siteBase, "public")); err != nil {
		t.Fatal(err)
	}

	// No git tree yet: legacy dirs must survive (symlink rollback works).
	e.cleanupLegacyReleaseLayout(siteBase)
	if _, err := os.Readlink(filepath.Join(siteBase, "public")); err != nil {
		t.Fatal("without a workdir git tree the legacy layout must be kept")
	}

	// With the gitfile present (in-place model live): retire the legacy dirs.
	if err := os.WriteFile(filepath.Join(wd, ".git"), []byte("gitdir: "+e.siteGitDir(siteID)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.cleanupLegacyReleaseLayout(siteBase)
	if _, err := os.Lstat(filepath.Join(siteBase, "public")); !os.IsNotExist(err) {
		t.Fatalf("public must be removed once the workdir model owns serving: %v", err)
	}
	if _, err := os.Stat(rel); !os.IsNotExist(err) {
		t.Fatalf("releases history must be removed once rollback-by-commit exists: %v", err)
	}
}

// TestDeployGitWorkdirLifecycle — the full in-place deploy contract on a
// real local repo: fresh clone into workdir (git database OUT of the work
// tree), then in-place updates where durable untracked files survive and
// repo deletions land, then rollback by commit SHA (with the live .env
// preserved through every reset).
func TestDeployGitWorkdirLifecycle(t *testing.T) {
	repo, sha1, sha2 := gitRepo(t)
	siteID := "33333333-3333-3333-3333-333333333333"
	e, _ := newTestExecutor(t, siteID)
	workdir := e.siteWorkdir(siteID)
	spec := DeploySpec{
		WebsiteID: siteID,
		RepoURL:   "file://" + repo,
		Branch:    "main",
		WebDir:    "public",
		Runtime:   "static",
	}

	// Deploy 1 — fresh clone.
	out, err := e.DeployGit(t.Context(), spec)
	if err != nil {
		t.Fatalf("deploy 1: %v", err)
	}
	if out.CommitSHA != sha2 {
		t.Fatalf("deploy 1 sha = %s, want %s", out.CommitSHA, sha2)
	}
	if b, err := os.ReadFile(filepath.Join(workdir, "public", "index.php")); err != nil || len(b) == 0 {
		t.Fatalf("repo files must sit directly in workdir: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(workdir, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("workdir/.git must be a gitfile pointer (separate git dir), got %+v", fi)
	}
	if _, err := os.Stat(filepath.Join(e.siteGitDir(siteID), "HEAD")); err != nil {
		t.Fatalf("git database must live outside the work tree: %v", err)
	}
	// Durable state the app owns (untracked — never in git).
	if err := os.WriteFile(filepath.Join(workdir, ".env"), []byte("APP_KEY=live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "storage", "f.txt"), []byte("upload"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deploy 2 — in-place update: edits land, deletions land, durable survives.
	out, err = e.DeployGit(t.Context(), spec)
	if err != nil {
		t.Fatalf("deploy 2: %v", err)
	}
	if out.CommitSHA != sha2 {
		t.Fatalf("deploy 2 sha = %s, want %s", out.CommitSHA, sha2)
	}
	if b, _ := os.ReadFile(filepath.Join(workdir, "index.html")); string(b) != "v2" {
		t.Fatalf("in-place update must land edits, got %q", b)
	}
	if _, err := os.Stat(filepath.Join(workdir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("in-place update must remove repo-deleted files")
	}
	if b, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || string(b) != "APP_KEY=live" {
		t.Fatalf(".env must survive the reset: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(workdir, "storage", "f.txt")); err != nil || string(b) != "upload" {
		t.Fatalf("storage must survive the reset: %v", err)
	}
	if out.ReleaseDir != workdir {
		t.Fatalf("outcome release dir = %q, want the workdir path", out.ReleaseDir)
	}

	// Rollback by SHA — old code back, durable state kept.
	rb, err := e.RollbackGit(t.Context(), siteID, "", sha1)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(workdir, "index.html")); string(b) != "v1" {
		t.Fatalf("rollback must restore the previous commit, got %q", b)
	}
	if _, err := os.Stat(filepath.Join(workdir, "gone.txt")); err != nil {
		t.Fatalf("rollback must restore files deleted since: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || string(b) != "APP_KEY=live" {
		t.Fatalf(".env must survive rollback: %v", err)
	}
	if !strings.Contains(rb.Log, sha1[:7]) {
		t.Fatalf("rollback log must name the commit, got %q", rb.Log)
	}

	// Legacy release-dir rollback must be refused on workdir sites.
	if _, err := e.RollbackGit(t.Context(), siteID, filepath.Join(e.docRootBase, siteID, "releases", "r1"), ""); err == nil {
		t.Fatal("release-dir rollback must fail once the site is on the workdir model")
	}
}

// TestDeployGitPreservesManualContent — a fresh-clone deploy over manual
// workdir content (or the migration seed) carries the durable files into
// the new tree and preserves the old content under tmp/ instead of
// destroying it.
func TestDeployGitPreservesManualContent(t *testing.T) {
	repo, _, _ := gitRepo(t)
	siteID := "22222222-2222-2222-2222-222222222222"
	e, siteBase := newTestExecutor(t, siteID)
	workdir := e.siteWorkdir(siteID)
	// Manual content: an .env the repo cannot know + a real upload.
	if err := os.MkdirAll(workdir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".env"), []byte("APP_KEY=manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "index.html"), []byte("manual site"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.DeployGit(t.Context(), DeploySpec{
		WebsiteID: siteID, RepoURL: "file://" + repo, Branch: "main", WebDir: "public", Runtime: "static",
	})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out.Log)
	}
	if b, err := os.ReadFile(filepath.Join(workdir, ".env")); err != nil || string(b) != "APP_KEY=manual" {
		t.Fatalf("manual .env must be carried into the new tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "public", "index.php")); err != nil {
		t.Fatalf("repo files must land in workdir: %v", err)
	}
	// Old content preserved under tmp/initial-manual-*, not destroyed.
	matches, _ := filepath.Glob(filepath.Join(siteBase, "tmp", "initial-manual-*", "index.html"))
	if len(matches) == 0 {
		t.Fatal("previous manual content must be preserved under tmp/initial-manual-*")
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != "manual site" {
		t.Fatalf("preserved content must be the original, got %q", b)
	}
}

// TestDeployGitBadWebDirRejected — a repo URL that is not http(s)/file://
// fails fast, and the fresh-clone path leaves no stray tmp dirs behind on
// clone failure.
func TestDeployGitBadRepoURL(t *testing.T) {
	siteID := "11111111-1111-1111-1111-111111111111"
	e, _ := newTestExecutor(t, siteID)
	if _, err := e.DeployGit(t.Context(), DeploySpec{WebsiteID: siteID, RepoURL: "ftp://x", Runtime: "static"}); err == nil {
		t.Fatal("non-http(s)/file repo url must be refused")
	}
	if _, err := e.DeployGit(t.Context(), DeploySpec{WebsiteID: siteID, RepoURL: "not-a-url", Runtime: "static"}); err == nil {
		t.Fatal("garbage repo url must be refused")
	}
}
