package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

const releasesBase = "/srv/epicpanel/releases"

// DeployOutcome is the job result for deploy_website.
type DeployOutcome struct {
	CommitSHA  string `json:"commit_sha"`
	ReleaseDir string `json:"release_dir"`
	Log        string `json:"log"`
}

// DeployGit clones/pulls the repo into a new release dir, carries over
// .env / user-uploads, and atomically flips the public symlink. Previous
// releases are retained for rollback (pruned to the last 5).
func (e *Executor) DeployGit(ctx context.Context, websiteID, repoURL, branch, tokenCipherB64 string) (*DeployOutcome, error) {
	if _, err := uuid.Parse(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		return nil, fmt.Errorf("repo url must be http(s)")
	}
	branch = sanitizeBranch(branch)
	if err := e.ensureGit(ctx); err != nil {
		return nil, err
	}

	uid, gid, err := siteOwnerIDs(websiteID)
	if err != nil {
		return nil, fmt.Errorf("site tree missing or unowned: %w", err)
	}

	releasesDir := filepath.Join(releasesBase, websiteID)
	publicLink := filepath.Join("/srv/epicpanel/websites", websiteID, "public")
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		return nil, err
	}

	releaseID, err := randomURLSafe(6)
	if err != nil {
		return nil, err
	}
	releaseDir := filepath.Join(releasesDir, time.Now().UTC().Format("20060102-150405")+"-"+releaseID)

	log := newLogBuilder()
	var cloneURL string
	if tokenCipherB64 != "" {
		token, err := decodeDeployToken(tokenCipherB64)
		if err != nil {
			return nil, fmt.Errorf("decode deploy token: %w", err)
		}
		cloneURL = withGitToken(repoURL, token)
	} else {
		cloneURL = repoURL
	}

	log.step("fetching source")
	cloneArgs := []string{"clone", "--depth", "1", "--branch", branch, cloneURL, releaseDir}
	if err := e.run(ctx, "git", cloneArgs...); err != nil {
		// Dumb-HTTP remotes and some mirrors don't support shallow clones;
		// fall back to a full clone before failing the deployment.
		fullArgs := []string{"clone", "--branch", branch, cloneURL, releaseDir}
		if err2 := e.run(ctx, "git", fullArgs...); err2 != nil {
			_ = os.RemoveAll(releaseDir)
			return nil, fmt.Errorf("git clone: %w", err2)
		}
	}
	log.stepf("cloned %s @ %s", repoURL, branch)

	sha := ""
	if b, err := os.ReadFile(filepath.Join(releaseDir, ".git", "HEAD")); err == nil {
		_ = e.run(ctx, "git", "-C", releaseDir, "fetch", "--depth", "1", "origin", branch)
		out, err := execGitSHA(ctx, releaseDir)
		if err == nil {
			sha = out
		}
		_ = b
	}
	// Strip .git from the release (not web-servable material).
	_ = os.RemoveAll(filepath.Join(releaseDir, ".git"))

	// Carry over environment + durable user content from the live release.
	log.step("carrying over .env and uploads")
	carryOverFromCurrent(publicLink, releaseDir)

	// Ownership + permissions: site user owns everything.
	_ = chownRecursive(releaseDir, uid, gid)

	log.step("activating release")
	// First deployment: public is a real directory (initial provisioning).
	// Convert it into the current release layout by moving it aside.
	if info, err := os.Lstat(publicLink); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		migrated := filepath.Join(releasesDir, "initial-manual")
		if err := os.Rename(publicLink, migrated); err != nil {
			return nil, fmt.Errorf("migrate initial content: %w", err)
		}
		// Repo content wins; carry over only local state the repo can't know
		// about (.env, uploads, storage) if present in the manual content.
		for _, name := range []string{".env", "uploads", "storage"} {
			src := filepath.Join(migrated, name)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst := filepath.Join(releaseDir, name)
			if st, err := os.Stat(src); err == nil && st.IsDir() {
				_ = os.MkdirAll(dst, 0o755)
				_ = copyTree(src, dst)
			} else if b, err := os.ReadFile(src); err == nil {
				_ = os.WriteFile(dst, b, 0o600)
			}
		}
	}
	tmpLink := publicLink + ".new"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(releaseDir, tmpLink); err != nil {
		return nil, fmt.Errorf("stage symlink: %w", err)
	}
	if err := os.Rename(tmpLink, publicLink); err != nil {
		return nil, fmt.Errorf("activate release: %w", err)
	}

	// Prune old releases (keep 5).
	e.pruneReleases(releasesDir, 5)

	log.step("done")
	slog.Info("deployment completed", "website", websiteID, "release", releaseDir, "sha", sha)
	return &DeployOutcome{CommitSHA: sha, ReleaseDir: releaseDir, Log: log.String()}, nil
}

// RollbackGit repoints the public symlink at a previous release (atomic).
func (e *Executor) RollbackGit(ctx context.Context, websiteID, targetReleaseDir string) (*DeployOutcome, error) {
	if _, err := uuid.Parse(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if !strings.HasPrefix(targetReleaseDir, releasesBase+"/") {
		return nil, fmt.Errorf("invalid release dir")
	}
	if _, err := os.Stat(targetReleaseDir); err != nil {
		return nil, fmt.Errorf("target release missing: %w", err)
	}
	publicLink := filepath.Join("/srv/epicpanel/websites", websiteID, "public")
	tmpLink := publicLink + ".new"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(targetReleaseDir, tmpLink); err != nil {
		return nil, fmt.Errorf("stage symlink: %w", err)
	}
	if err := os.Rename(tmpLink, publicLink); err != nil {
		return nil, fmt.Errorf("activate rollback: %w", err)
	}
	slog.Info("rollback completed", "website", websiteID, "release", targetReleaseDir)
	return &DeployOutcome{ReleaseDir: targetReleaseDir, Log: "rolled back to " + filepath.Base(targetReleaseDir)}, nil
}

func (e *Executor) ensureGit(ctx context.Context) error {
	if _, err := exec.LookPath("git"); err == nil {
		return nil
	}
	return e.aptInstall(ctx, []string{"git"}, "", "git")
}

// siteOwnerIDs returns the uid/gid owning the site tree (the site's unix user).
func siteOwnerIDs(websiteID string) (int, int, error) {
	base := filepath.Join("/srv/epicpanel/websites", websiteID)
	info, err := os.Stat(base)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, fmt.Errorf("stat unsupported")
}

// carryOverFromCurrent copies .env and a small set of durable dirs from the
// currently live release into the new release.
func carryOverFromCurrent(publicLink, newRelease string) {
	if cur, err := os.Readlink(publicLink); err == nil {
		for _, name := range []string{".env"} {
			if b, err := os.ReadFile(filepath.Join(cur, name)); err == nil {
				_ = os.WriteFile(filepath.Join(newRelease, name), b, 0o600)
			}
		}
		for _, dir := range []string{"uploads", "storage"} {
			src := filepath.Join(cur, dir)
			if st, err := os.Stat(src); err == nil && st.IsDir() {
				dst := filepath.Join(newRelease, dir)
				_ = os.MkdirAll(dst, 0o755)
				_ = copyTree(src, dst)
			}
		}
	}
}

func (e *Executor) pruneReleases(releasesDir string, keep int) {
	entries, err := os.ReadDir(releasesDir)
	if err != nil || len(entries) <= keep {
		return
	}
	// Dir names sort by timestamp prefix; oldest first.
	for i := 0; i < len(entries)-keep; i++ {
		_ = os.RemoveAll(filepath.Join(releasesDir, entries[i].Name()))
	}
}

func sanitizeBranch(b string) string {
	b = strings.TrimSpace(b)
	if b == "" {
		return "main"
	}
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '/':
		default:
			return "main"
		}
	}
	return b
}

func withGitToken(repoURL, token string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}
	u.User = url.UserPassword("epicpanel", token)
	return u.String()
}

func decodeDeployToken(cipherB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return "", err
	}
	return secretbox.Decrypt(raw)
}

func execGitSHA(ctx context.Context, dir string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// logBuilder accumulates human-readable deploy logs (kept short).
type logBuilder struct {
	sb strings.Builder
}

func newLogBuilder() *logBuilder { return &logBuilder{} }

func (l *logBuilder) step(msg string) {
	l.sb.WriteString("- " + msg + "\n")
}

func (l *logBuilder) stepf(format string, args ...any) {
	l.step(fmt.Sprintf(format, args...))
}

func (l *logBuilder) String() string { return l.sb.String() }
