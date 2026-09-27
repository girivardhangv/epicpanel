package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// releasesBase is the LEGACY releases location (pre-2026-09-27 deploys).
// The releases model is retired: a deploy now updates the site's WORKDIR
// in place. siteReleasesDir/validReleaseTarget/pruneReleases survive only
// to roll back sites that have not migrated yet.
const releasesBase = "/srv/epicpanel/releases"

// siteReleasesDir is the legacy releases directory for a website.
func siteReleasesDir(websiteID string) string {
	return filepath.Join("/srv/epicpanel/websites", websiteID, "releases")
}

// siteWorkdir is the website's work tree: ALL repo files and folders live
// directly inside it (workdir/public for Laravel, workdir/index.html for a
// static repo). The vhost/FPM docroot resolves as <site>/workdir/<web_dir>.
func (e *Executor) siteWorkdir(websiteID string) string {
	return filepath.Join(e.docRootBase, websiteID, "workdir")
}

// siteGitDir keeps the git database OUT of the work tree (--separate-git-dir):
// workdir holds exactly the repo's files (a clean workdir/--work-tree split),
// .git is never web-servable material, and the site tree stays one level deep.
func (e *Executor) siteGitDir(websiteID string) string {
	return filepath.Join(e.docRootBase, websiteID, ".git")
}

// hasWorkdirGit reports whether the site runs the in-place workdir model:
// workdir/.git is a "gitdir: <site>/.git" pointer file left by
// --separate-git-dir clones.
func (e *Executor) hasWorkdirGit(websiteID string) bool {
	gitfile := filepath.Join(e.siteWorkdir(websiteID), ".git")
	b, err := os.ReadFile(gitfile)
	return err == nil && strings.HasPrefix(string(b), "gitdir:")
}

// currentContentDir resolves where the site's live content is right now:
// the workdir when populated (new model / migrated site), else the legacy
// public path with its release symlink resolved.
func (e *Executor) currentContentDir(websiteID string) string {
	wd := e.siteWorkdir(websiteID)
	if entries, err := os.ReadDir(wd); err == nil && len(entries) > 0 {
		return wd
	}
	public := filepath.Join(e.docRootBase, websiteID, "public")
	if link, err := os.Readlink(public); err == nil {
		return link
	}
	return public
}

// validReleaseTarget accepts release dirs in the legacy (outside the site
// tree) and in-tree locations, for old deployments mid-transition.
func validReleaseTarget(websiteID, target string) bool {
	return strings.HasPrefix(target, filepath.Join("/srv/epicpanel/websites", websiteID, "releases")+"/") ||
		strings.HasPrefix(target, releasesBase+"/"+websiteID+"/")
}

var validCommitSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DeploySpec is the full desired deploy context: repo source plus the site's
// runtime context, so a deploy BUILDS the app (composer / npm / pip / go
// build — per runtime, opt-in) and restarts + health-checks the app process
// afterwards (a release whose app never listens is reset back to the
// previous commit).
type DeploySpec struct {
	WebsiteID string
	RepoURL   string
	Branch    string
	// WebDir is the repo-relative running directory ("public" for Laravel).
	// The served docroot is <site>/workdir/<web_dir>; the fetched tree must
	// contain the dir or the deploy fails BEFORE the reset lands — a typo'd
	// web dir never goes live.
	WebDir string
	// AutoBuild runs the composer/npm build gate after the update. Default
	// OFF: a git deploy is a plain fetch (update + serve) — the panel
	// cannot know what the app needs, so building is the app owner's choice
	// (this toggle, or composer/artisan via the Commands runner).
	AutoBuild       bool
	TokenCipherB64  string
	Runtime         string
	RuntimeVersion  string
	BuildCommand    string
	UnixUser        string
	StartupCommand  string
	AppPort         int
	AppDesiredState string
	AppEnvEnc       string
}

// DeployOutcome is the job result for deploy_website. ReleaseDir keeps its
// wire name for compatibility but now carries the workdir path.
type DeployOutcome struct {
	CommitSHA  string `json:"commit_sha"`
	ReleaseDir string `json:"release_dir"`
	Log        string `json:"log"`
}

// DeployGit updates the site's workdir to the branch head and serves it.
//
// New model (workdir/.git gitfile present): git fetch + reset --hard
// FETCH_HEAD in place. Untracked durable files (.env, uploads/, storage/,
// sqlite) survive the reset; the live .env additionally wins over a
// repo-tracked one via stash/restore.
//
// First deploy / migration (no workdir git): fresh clone with
// --separate-git-dir (workdir holds only repo files), durable state carried
// over from the current content (workdir seed, legacy public release, or
// manual content — which is first preserved under tmp/), then the legacy
// public symlink + releases history are removed by the next provision
// converge after the vhost re-render.
//
// App runtimes are restarted and health-checked after the update; a failed
// build or health check resets the workdir back to the previous commit.
func (e *Executor) DeployGit(ctx context.Context, spec DeploySpec) (*DeployOutcome, error) {
	websiteID, repoURL, branch, tokenCipherB64 := spec.WebsiteID, spec.RepoURL, spec.Branch, spec.TokenCipherB64
	if _, err := uuid.Parse(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	// file:// is accepted agent-side only for tests; the control plane
	// refuses it (repo_url must be http(s) there).
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") &&
		!strings.HasPrefix(repoURL, "file://") {
		return nil, fmt.Errorf("repo url must be http(s)")
	}
	branch = sanitizeBranch(branch)
	if err := e.ensureGit(ctx); err != nil {
		return nil, err
	}

	uid, gid, err := e.siteOwnerIDs(websiteID)
	if err != nil {
		return nil, fmt.Errorf("site tree missing or unowned: %w", err)
	}

	workdir := e.siteWorkdir(websiteID)
	gitDir := e.siteGitDir(websiteID)

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

	// Carry over environment + durable user content between trees. The live
	// .env always wins (the repo's committed .env, if any, is a template).
	carry := func(src, dst string) {
		log.step("carrying over .env and uploads")
		carryOverDurable(src, dst)
	}

	var sha string
	if e.hasWorkdirGit(websiteID) {
		// --- in-place update: fetch + reset, untracked durable files survive.
		log.step("updating workdir in place (git fetch + reset)")
		envPath := filepath.Join(workdir, ".env")
		envStash, hadEnv := "", false
		if b, err := os.ReadFile(envPath); err == nil {
			envStash, hadEnv = string(b), true
		}
		prevSHA, _ := execGitSHA(ctx, workdir)
		if err := e.run(ctx, "git", "-C", workdir, "fetch", "--depth", "1", "origin", branch); err != nil {
			// Dumb-HTTP remotes and some mirrors don't support shallow fetch;
			// fall back to a full fetch before failing the deployment.
			if err2 := e.run(ctx, "git", "-C", workdir, "fetch", "origin", branch); err2 != nil {
				return nil, fmt.Errorf("git fetch: %w", err2)
			}
		}
		if err := checkFetchedWebDir(ctx, workdir, spec.WebDir); err != nil {
			log.step(err.Error())
		} else {
			log.stepf("running directory %q present in the fetched tree", strings.Trim(spec.WebDir, "/"))
		}
		if err := e.run(ctx, "git", "-C", workdir, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return nil, fmt.Errorf("git reset: %w", err)
		}
		if hadEnv {
			_ = os.WriteFile(envPath, []byte(envStash), 0o600)
		}
		sha, _ = execGitSHA(ctx, workdir)
		log.stepf("workdir at %s @ %s", repoURL, branch)

		// Ownership: reset wrote tracked files as the agent (root) — hand the
		// tree back to the site user BEFORE anything runs as it (build,
		// commands, the app process writing cache/logs).
		_ = e.chownFn(workdir, uid, gid)
		_ = e.chownFn(gitDir, uid, gid)

		// Build gate — OPT-IN (plain fetch & activate by default).
		if spec.AutoBuild {
			buildLog, buildErr := e.buildRelease(ctx, spec, workdir)
			log.WriteString(buildLog)
			if buildErr != nil {
				// Best effort: put the previous commit back so the site keeps
				// serving old code instead of a half-built tree.
				if prevSHA != "" {
					_ = e.run(ctx, "git", "-C", workdir, "reset", "--hard", prevSHA)
				}
				return nil, fmt.Errorf("release build failed (workdir reset to previous commit): %w", buildErr)
			}
			log.step("build completed")
		} else {
			log.step("build skipped (auto-build off) — plain fetch & activate")
		}
	} else {
		// --- fresh clone into workdir (first deploy, or legacy/migrated site
		// without a git work tree yet).
		log.step("fetching source")
		tmpBase := filepath.Join(e.docRootBase, websiteID, "tmp")
		if err := os.MkdirAll(tmpBase, 0o755); err != nil {
			return nil, err
		}
		seedID, err := randomURLSafe(6)
		if err != nil {
			return nil, err
		}
		cloneDir := filepath.Join(tmpBase, "deploy-"+seedID)
		cloneArgs := []string{"clone", "--separate-git-dir", gitDir, "--depth", "1", "--branch", branch, cloneURL, cloneDir}
		if err := e.run(ctx, "git", cloneArgs...); err != nil {
			// Dumb-HTTP remotes and some mirrors don't support shallow clones;
			// fall back to a full clone before failing the deployment.
			fullArgs := []string{"clone", "--separate-git-dir", gitDir, "--branch", branch, cloneURL, cloneDir}
			if err2 := e.run(ctx, "git", fullArgs...); err2 != nil {
				_ = os.RemoveAll(cloneDir)
				return nil, fmt.Errorf("git clone: %w", err2)
			}
		}
		cleanupClone := func() { _ = os.RemoveAll(cloneDir) }

		if err := checkReleaseWebDir(cloneDir, spec.WebDir); err != nil {
			log.step(err.Error())
		} else {
			log.stepf("running directory %q present in the release", strings.Trim(spec.WebDir, "/"))
		}
		sha, _ = execGitSHA(ctx, cloneDir)

		// Durable state from whatever currently lives on the box (migration
		// seed / legacy release / manual content).
		cur := e.currentContentDir(websiteID)
		carry(cur, cloneDir)

		// Swap workdir → clone. Pre-existing content (manual uploads or the
		// provision migration seed) is preserved under tmp/ first.
		var preserved string
		if entries, err := os.ReadDir(workdir); err == nil && len(entries) > 0 {
			preserved = filepath.Join(tmpBase, "initial-manual-"+time.Now().UTC().Format("20060102-150405"))
			if err := os.Rename(workdir, preserved); err != nil {
				cleanupClone()
				return nil, fmt.Errorf("preserve previous workdir content: %w", err)
			}
			log.stepf("previous workdir content preserved at %s", preserved)
		} else {
			_ = os.RemoveAll(workdir)
		}
		if err := os.Rename(cloneDir, workdir); err != nil {
			cleanupClone()
			return nil, fmt.Errorf("activate workdir: %w", err)
		}

		// Ownership: the clone was created by the agent (root) — hand the
		// work tree + git database to the site user BEFORE anything runs as
		// it (build gate, commands, the app process).
		_ = e.chownFn(workdir, uid, gid)
		_ = e.chownFn(gitDir, uid, gid)

		// Build gate — OPT-IN (plain fetch & activate by default).
		if spec.AutoBuild {
			buildLog, buildErr := e.buildRelease(ctx, spec, workdir)
			log.WriteString(buildLog)
			if buildErr != nil {
				restorePreserved(workdir, preserved)
				return nil, fmt.Errorf("release build failed (previous content restored): %w", buildErr)
			}
			log.step("build completed")
		} else {
			log.step("build skipped (auto-build off) — plain fetch & activate")
		}
	}

	// App runtimes: restart the process on the new code and health-check the
	// port. A release whose app never listens is reset back and the previous
	// code is started again — deploys never end in a dead site.
	if isAppRuntime(spec.Runtime) && spec.AppPort > 0 && spec.AppDesiredState == "running" {
		appSpec := AppSpec{
			WebsiteID:      spec.WebsiteID,
			UnixUser:       spec.UnixUser,
			Runtime:        spec.Runtime,
			RuntimeVersion: spec.RuntimeVersion,
			StartupCommand: spec.StartupCommand,
			InternalPort:   spec.AppPort,
			EnvEnc:         spec.AppEnvEnc,
			AppRoot:        workdir, // systemd runs the app straight from the work tree
		}
		if _, err := e.RestartApp(ctx, appSpec); err != nil {
			e.deployFailureRestore(ctx, websiteID, workdir, spec, sha)
			return nil, fmt.Errorf("app restart after deploy: %w", err)
		}
		if err := waitPortListening(ctx, spec.AppPort, 20*time.Second); err != nil {
			e.deployFailureRestore(ctx, websiteID, workdir, spec, sha)
			if _, rerr := e.RestartApp(ctx, appSpec); rerr != nil {
				log.step("previous app could not be restarted — start it from the panel")
			}
			return nil, fmt.Errorf("app did not listen on 127.0.0.1:%d within 20s — workdir reset to the previous state: %w", spec.AppPort, err)
		}
		log.stepf("app healthy on 127.0.0.1:%d", spec.AppPort)
	}

	log.step("done")
	slog.Info("deployment completed", "website", websiteID, "workdir", workdir, "sha", sha)
	return &DeployOutcome{CommitSHA: sha, ReleaseDir: workdir, Log: log.String()}, nil
}

// restorePreserved puts the preserved pre-deploy content back into workdir
// after a failed fresh-clone deploy (best effort — fresh sites may end up
// with an empty workdir, same blast radius as a failed first release).
func restorePreserved(workdir, preserved string) {
	if preserved == "" {
		return
	}
	_ = os.RemoveAll(workdir)
	_ = os.Rename(preserved, workdir)
}

// deployFailureRestore reverts the workdir after a failed app restart /
// health check: reset to the previous commit when a git history exists,
// otherwise restore the preserved pre-deploy content.
func (e *Executor) deployFailureRestore(ctx context.Context, websiteID, workdir string, spec DeploySpec, newSHA string) {
	if e.hasWorkdirGit(websiteID) {
		prev, _ := gitPrevSHA(ctx, workdir, newSHA)
		if prev != "" && prev != newSHA {
			envPath := filepath.Join(workdir, ".env")
			envStash, hadEnv := "", false
			if b, err := os.ReadFile(envPath); err == nil {
				envStash, hadEnv = string(b), true
			}
			_ = e.run(ctx, "git", "-C", workdir, "reset", "--hard", prev)
			if hadEnv {
				_ = os.WriteFile(envPath, []byte(envStash), 0o600)
			}
			slog.Warn("deploy rolled back to previous commit", "website", websiteID, "sha", prev)
		}
		return
	}
	// Fresh-clone path: the preserved content sits under tmp/.
	tmpBase := filepath.Join(e.docRootBase, websiteID, "tmp")
	entries, _ := os.ReadDir(tmpBase)
	for _, en := range entries {
		if !strings.HasPrefix(en.Name(), "initial-manual-") {
			continue
		}
		candidate := filepath.Join(tmpBase, en.Name())
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			restorePreserved(workdir, candidate)
			slog.Warn("deploy rolled back to preserved content", "website", websiteID, "from", candidate)
			return
		}
	}
}

// gitPrevSHA resolves the commit the workdir was on before the deploy that
// produced newSHA: the reflog's second-to-newest HEAD entry.
func gitPrevSHA(ctx context.Context, workdir, newSHA string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "git", "-C", workdir, "reflog", "--format=%H", "-n", "10").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha := strings.TrimSpace(line)
		if sha != "" && sha != newSHA && validCommitSHARe.MatchString(sha) {
			return sha, nil
		}
	}
	return "", fmt.Errorf("no previous commit in reflog")
}

// RollbackGit repoints the site at an earlier state. Workdir-model sites
// roll back by commit SHA (git reset --hard — the work tree is in-place);
// legacy sites still on the public symlink flip it at a previous release
// (atomic).
func (e *Executor) RollbackGit(ctx context.Context, websiteID, targetReleaseDir, targetCommitSHA string) (*DeployOutcome, error) {
	if _, err := uuid.Parse(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	workdir := e.siteWorkdir(websiteID)
	if e.hasWorkdirGit(websiteID) {
		sha := strings.TrimSpace(targetCommitSHA)
		if !validCommitSHARe.MatchString(sha) {
			return nil, fmt.Errorf("rollback needs a 40-char commit sha, got %q", sha)
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		catFile := func() error {
			return exec.CommandContext(c, "git", "-C", workdir, "cat-file", "-e", sha+"^{commit}").Run()
		}
		if err := catFile(); err != nil {
			// Shallow history (deploys fetch --depth 1) doesn't carry the old
			// commit — unshallow once, then re-check.
			_ = e.run(ctx, "git", "-C", workdir, "fetch", "--unshallow", "origin")
			if catErr := catFile(); catErr != nil {
				cancel()
				return nil, fmt.Errorf("commit %s not found in the site's git history: %w", sha, catErr)
			}
		}
		cancel()
		envPath := filepath.Join(workdir, ".env")
		envStash, hadEnv := "", false
		if b, err := os.ReadFile(envPath); err == nil {
			envStash, hadEnv = string(b), true
		}
		if err := e.run(ctx, "git", "-C", workdir, "reset", "--hard", sha); err != nil {
			return nil, fmt.Errorf("git reset: %w", err)
		}
		if hadEnv {
			_ = os.WriteFile(envPath, []byte(envStash), 0o600)
		}
		slog.Info("rollback completed", "website", websiteID, "sha", sha)
		return &DeployOutcome{CommitSHA: sha, ReleaseDir: workdir, Log: "rolled back to " + sha[:7]}, nil
	}
	// Legacy site: release-symlink flip.
	if !validReleaseTarget(websiteID, targetReleaseDir) {
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
func (e *Executor) siteOwnerIDs(websiteID string) (int, int, error) {
	base := filepath.Join(e.docRootBase, websiteID)
	info, err := os.Stat(base)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, fmt.Errorf("stat unsupported")
}

// carryOverDurable copies .env and a small set of durable content from src
// into dst: user uploads, storage, and SQLite database files (which live
// under database/ and are never in git — without carrying them, every
// deploy would hand the app a fresh empty database). Used when the workdir
// is seeded fresh; in-place updates keep these files naturally (untracked
// files survive reset --hard).
func carryOverDurable(srcDir, dstDir string) {
	if srcDir == "" || srcDir == dstDir {
		return
	}
	for _, name := range []string{".env"} {
		if b, err := os.ReadFile(filepath.Join(srcDir, name)); err == nil {
			_ = os.WriteFile(filepath.Join(dstDir, name), b, 0o600)
		}
	}
	for _, dir := range []string{"uploads", "storage"} {
		src := filepath.Join(srcDir, dir)
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			dst := filepath.Join(dstDir, dir)
			_ = os.MkdirAll(dst, 0o755)
			_ = copyTree(src, dst)
		}
	}
	if dbFiles, err := filepath.Glob(filepath.Join(srcDir, "database", "*.sqlite")); err == nil {
		for _, f := range dbFiles {
			dst := filepath.Join(dstDir, "database", filepath.Base(f))
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			if b, err := os.ReadFile(f); err == nil {
				_ = os.WriteFile(dst, b, 0o600)
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

// ensureLaravelSqlite creates the SQLite database file a Laravel app
// expects when its .env sets DB_CONNECTION=sqlite. The file is normally
// gitignored, so a fresh clone never has it — and composer's
// post-autoload-dump (artisan package:discover) boots the app, whose
// sqlite connector refuses a missing database file, failing the deploy
// before it can go live. DB_DATABASE wins when set; otherwise the Laravel
// default database/database.sqlite. An empty file IS a valid empty SQLite
// database; the first successful release carries it forward with data.
func ensureLaravelSqlite(releaseDir string) {
	envPath := filepath.Join(releaseDir, ".env")
	b, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	sqlite := false
	dbPath := ""
	for _, line := range strings.Split(string(b), "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "DB_CONNECTION":
			sqlite = strings.TrimSpace(kv[1]) == "sqlite"
		case "DB_DATABASE":
			dbPath = strings.TrimSpace(kv[1])
		}
	}
	if !sqlite {
		return
	}
	if dbPath == "" {
		dbPath = "database/database.sqlite"
	}
	target := dbPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(releaseDir, filepath.FromSlash(dbPath))
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		_ = os.WriteFile(target, nil, 0o600)
	}
}

// checkReleaseWebDir validates the running directory against a freshly
// cloned tree: empty = repo root; otherwise the dir must exist (a typo'd
// web dir fails the deploy before activation instead of going live with a
// broken docroot).
func checkReleaseWebDir(releaseDir, webDir string) error {
	webDir = strings.Trim(webDir, "/")
	if webDir == "" {
		return nil
	}
	fi, err := os.Stat(filepath.Join(releaseDir, filepath.FromSlash(webDir)))
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("running directory %q not found in the release — check the deployment web_dir setting", webDir)
	}
	return nil
}

// checkFetchedWebDir is the in-place twin of checkReleaseWebDir: it checks
// the FETCHED tree (FETCH_HEAD) BEFORE the reset lands, so a typo'd web dir
// is reported while the current content still serves.
func checkFetchedWebDir(ctx context.Context, workdir, webDir string) error {
	webDir = strings.Trim(webDir, "/")
	if webDir == "" {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "git", "-C", workdir, "ls-tree", "--name-only", "FETCH_HEAD", "--", webDir).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("running directory %q not found in the fetched tree — check the deployment web_dir setting", webDir)
	}
	return nil
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

// buildRelease builds the workdir in place, as the site user: composer for
// PHP (when composer.json exists), npm for node, venv+pip for python, go
// build for go. Reuses the app build helpers with the workdir as root —
// builds are always per-tree (fresh node_modules/venv/bin), never shared.
func (e *Executor) buildRelease(ctx context.Context, spec DeploySpec, releaseDir string) (string, error) {
	var log strings.Builder
	uid, gid, err := e.siteOwnerIDs(spec.WebsiteID)
	if err != nil {
		return "", err
	}
	p := AppSpec{WebsiteID: spec.WebsiteID, Runtime: spec.Runtime, RuntimeVersion: spec.RuntimeVersion, BuildCommand: spec.BuildCommand}
	switch spec.Runtime {
	case "php":
		if fileExists(filepath.Join(releaseDir, "composer.json")) {
			// Composer + unzip must EXIST before the build gate runs: a fresh
			// node never had them installed (the one-click installers ensure
			// them on demand; a git deploy must too, or every PHP deploy dies
			// here with "env: 'composer': No such file or directory").
			if _, err := exec.LookPath("unzip"); err != nil {
				if err := e.aptInstall(ctx, []string{"unzip"}, "", "unzip"); err != nil {
					return log.String(), fmt.Errorf("ensure unzip: %w", err)
				}
			}
			if err := e.EnsureComposer(ctx); err != nil {
				return log.String(), fmt.Errorf("ensure composer: %w", err)
			}
			// Pin composer to the SITE'S PHP version (same contract as the
			// site command runner): the bare `composer` PHAR shebang resolves
			// `php` from PATH = the system default (e.g. 8.3), while the
			// site's pool runs the selected runtime (e.g. 8.5) — composer
			// then evaluates platform requirements against the WRONG php.
			phpBin := "php"
			if minor := strings.TrimSpace(spec.RuntimeVersion); phpMinorRe.MatchString(minor) {
				phpBin = phpBinary(minor)
			} else {
				phpBin = phpBinary(latestInstalledPHPMinor(e))
			}
			ensureLaravelSqlite(releaseDir)
			log.WriteString("composer install --no-dev --optimize-autoloader\n")
			if out, err := e.runAsSiteEnv(ctx, uid, gid, releaseDir, nil, phpBin,
				composerBin, "install", "--no-dev", "--optimize-autoloader", "--no-interaction"); err != nil {
				return log.String(), fmt.Errorf("composer install (php %s): %s (%w)", spec.RuntimeVersion, tailString(out, 400), err)
			}
		}
	case "node":
		if err := e.deployNodeApp(ctx, p, releaseDir, &log); err != nil {
			return log.String(), err
		}
	case "python":
		if err := e.deployPythonApp(ctx, p, releaseDir, &log); err != nil {
			return log.String(), err
		}
	case "go":
		if err := e.deployGoApp(ctx, p, releaseDir, &log); err != nil {
			return log.String(), err
		}
	}
	return log.String(), nil
}

// waitPortListening polls a loopback TCP port until it accepts a connection
// (or the timeout expires). This is the deploy health gate for app runtimes.
func waitPortListening(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s to listen", addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
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

// WriteString makes logBuilder an io.Writer so multi-line build output can
// be appended verbatim.
func (l *logBuilder) WriteString(s string) { l.sb.WriteString(s) }

func (l *logBuilder) String() string { return l.sb.String() }
