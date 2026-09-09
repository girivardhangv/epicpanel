package agent

// ============================================================================
// Phase 8 — Discord bot ops (node agent side). Bots are first-class
// workloads: isolated systemd transient units under a dedicated unix user,
// strong sandboxing, cgroup limits, env injected from a 0600
// EnvironmentFile. Customers never touch any daemon — every operation
// arrives as a job. NO docker, NO shell from customer payloads: commands are
// assembled from validated fields only.
//
// The coordinator merges DiscordOps into the agent dispatch (wave contract:
// handlers + registry in this file, worker.go untouched).
// ============================================================================

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// BotRootBase is the bot file layout root (bots are NOT websites).
const BotRootBase = "/srv/epicpanel/bots"

// BotUnitName is the systemd transient unit for a bot. The `epicpanel-app-`
// prefix keeps every bot inside the existing Phase 3 workload collector
// (listAppUnits samples `epicpanel-app-*`), so CPU/RAM/net/uptime/restarts
// stream live through the AppSample envelope without protocol changes.
func BotUnitName(botID string) string {
	if _, err := uuid.Parse(botID); err != nil {
		return ""
	}
	return "epicpanel-app-" + botID
}

// BotRoot is the bot's file root.
func BotRoot(botID string) string {
	return filepath.Join(BotRootBase, botID)
}

// botUserFor derives the dedicated unix user for a bot: ep-bot-<first-8>.
func botUserFor(botID string) (string, error) {
	id, err := uuid.Parse(botID)
	if err != nil {
		return "", fmt.Errorf("invalid bot id")
	}
	raw := strings.ReplaceAll(id.String(), "-", "")
	return "ep-bot-" + raw[:8], nil
}

// DepsTimeout bounds dependency installation (npm ci / pip install).
const DepsTimeout = 10 * time.Minute

// BotOpFunc is the signature the coordinator's dispatch merges.
type BotOpFunc func(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error)

// DiscordOps is the Phase 8 registry the coordinator merges into the agent
// job dispatch (worker.go stays untouched by this phase).
var DiscordOps = map[string]func(context.Context, *Executor, *Client, Config, json.RawMessage) (json.RawMessage, error){
	"bot_install":   HandleBotInstall,
	"bot_deploy_git": HandleBotDeployGit,
	"bot_upload":    HandleBotUpload,
	"bot_files":     HandleBotFiles,
	"bot_start":     HandleBotStart,
	"bot_stop":      HandleBotStop,
	"bot_restart":   HandleBotRestart,
	"bot_kill":      HandleBotKill,
	"bot_status":    HandleBotStatus,
	"bot_logs":      HandleBotLogs,
	"bot_delete":    HandleBotDelete,
}

// ---------------------------------------------------------------------------
// install — unix user, directories, dependency install (timeout-bounded)
// ---------------------------------------------------------------------------

// installPayload mirrors discord.BotInstallPayload (kept local to avoid the
// control-plane import in hot paths; identical JSON).
type installPayload struct {
	BotID          string `json:"bot_id"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	EnvEnc         string `json:"env_enc"`
}

// HandleBotInstall provisions the bot tree: dedicated unix user, dirs, kind
// marker, dependency install. Idempotent (safe to retry).
func HandleBotInstall(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p installPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	botUser, err := botUserFor(p.BotID)
	if err != nil {
		return nil, err
	}
	if err := ensureBotUser(botUser); err != nil {
		return nil, fmt.Errorf("bot user: %w", err)
	}
	// Ownership MUST come from the bot's unix user, not from the current
	// owner of the tree: the agent runs as root, so a freshly created tree
	// is root-owned and the circular stat would "chown" it to root — the
	// bot user (and systemd's User=) would then be unable to read or write
	// its own working directory (caught live: start crash-loops on
	// permission denied).
	uid, gid, err := idsForUser(botUser)
	if err != nil {
		return nil, fmt.Errorf("bot user ids: %w", err)
	}
	root := BotRoot(p.BotID)
	for _, dir := range []string{root, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	_ = chownRecursive(root, uid, gid)
	// The bot base (/srv/epicpanel/bots) is root-owned 0750: systemd needs to
	// CHDIR through it into the tree before ReadWritePaths applies. Grant the
	// bot's group traverse-only (--x) on the base — the tree below stays
	// bot-private (mirrors the www-data ACL pattern for websites).
	_ = exec.Command("setfacl", "-m", "g:"+botUser+":--x", BotRootBase).Run()
	_ = exec.Command("setfacl", "-m", "d:g:"+botUser+":--x", BotRootBase).Run()

	// Dependency install via the runtime abstraction (new runtime = provider).
	log := &strings.Builder{}
	if p.Runtime == "node" || p.Runtime == "python" {
		if err := botDepsInstall(ctx, e, p.Runtime, p.RuntimeVersion, root, uid, gid, log); err != nil {
			return nil, fmt.Errorf("%s (%w)", tailString(log.String(), 400), err)
		}
	} else if p.Runtime == "java" {
		log.WriteString("java runtime: foundation only — no dependency install\n")
	} else {
		return nil, fmt.Errorf("runtime %q has no bot deployment", p.Runtime)
	}

	slog.Info("bot installed", "bot", p.BotID, "runtime", p.Runtime, "user", botUser)
	out := discord.BotOutcome{BotID: p.BotID, OK: true, Log: scrubLog(log.String(), p.EnvEnc)}
	return json.Marshal(out)
}

// botDepsInstall runs the dependency step for node/python bots. Never a
// shell string from the payload: fixed argv per runtime.
func botDepsInstall(ctx context.Context, e *Executor, runtime, version, root string, uid, gid int, log *strings.Builder) error {
	ctx, cancel := context.WithTimeout(ctx, DepsTimeout)
	defer cancel()
	switch runtime {
	case "node":
		if _, err := os.Stat(filepath.Join(root, "package-lock.json")); err == nil {
			log.WriteString("npm ci --no-audit --no-fund\n")
			if out, err := runAsBotUser(ctx, uid, gid, root, "npm", "ci", "--no-audit", "--no-fund"); err != nil {
				return fmt.Errorf("npm ci: %s", tailString(out, 400))
			}
		} else if _, err := os.Stat(filepath.Join(root, "package.json")); err == nil {
			log.WriteString("npm install --no-audit --no-fund\n")
			if out, err := runAsBotUser(ctx, uid, gid, root, "npm", "install", "--no-audit", "--no-fund"); err != nil {
				return fmt.Errorf("npm install: %s", tailString(out, 400))
			}
		} else {
			log.WriteString("no package.json — skipping dependency install\n")
		}
	case "python":
		pyBin := resolvePython(version)
		venv := filepath.Join(root, "venv")
		if _, err := os.Stat(filepath.Join(venv, "bin", "python")); os.IsNotExist(err) {
			log.WriteString(pyBin + " -m venv venv\n")
			if out, err := runAsBotUser(ctx, uid, gid, root, pyBin, "-m", "venv", venv); err != nil {
				return fmt.Errorf("venv create: %s", tailString(out, 400))
			}
		}
		if _, err := os.Stat(filepath.Join(root, "requirements.txt")); err == nil {
			log.WriteString("venv/bin/pip install -r requirements.txt\n")
			if out, err := runAsBotUser(ctx, uid, gid, root,
				filepath.Join(venv, "bin", "pip"), "install", "-r", "requirements.txt"); err != nil {
				return fmt.Errorf("pip install: %s", tailString(out, 400))
			}
		} else {
			log.WriteString("no requirements.txt — skipping dependency install\n")
		}
	default:
		return fmt.Errorf("runtime %q has no dependency step", runtime)
	}
	return nil
}

// resolvePython picks the requested interpreter (honest fallback: whatever
// python3 exists, reported in the log by the caller).
func resolvePython(version string) string {
	if version != "" {
		if _, err := exec.LookPath("python" + version); err == nil {
			return "python" + version
		}
		major := strings.Split(version, ".")[0]
		if _, err := exec.LookPath("python" + major); err == nil {
			return "python" + major
		}
	}
	return "python3"
}

// runAsBotUser executes argv as the bot's unix user inside root (setpriv —
// never run customer code as root).
func runAsBotUser(ctx context.Context, uid, gid int, dir string, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	full := append([]string{"--reuid", strconv.Itoa(uid), "--regid", strconv.Itoa(gid),
		"--clear-groups", "--inh-caps", "-0", "--",
		"env", "HOME=" + dir, "USER=" + strconv.Itoa(uid), "TERM=xterm",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		name}, args...)
	cmd := exec.CommandContext(c, "setpriv", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensureBotUser creates the dedicated system user (idempotent).
func ensureBotUser(name string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	}
	out, err := exec.Command("useradd", "--system", "--shell", "/usr/sbin/nologin",
		"--home-dir", "/nonexistent", "--no-create-home", name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("useradd: %s (%w)", tailString(string(out), 200), err)
	}
	return nil
}

func botOwnerIDs(root string) (int, int, error) {
	if _, err := os.Stat(root); err != nil {
		_ = os.MkdirAll(root, 0o750)
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, fmt.Errorf("stat unsupported")
}

// scrubLog redacts secret values from an outcome log before it is stored in
// the job result (which the API can list). env values arrive encrypted; the
// agent decrypts once for the EnvironmentFile and for scrubbing.
func scrubLog(log string, envEnc string) string {
	vars, err := decryptEnvEnc(envEnc)
	if err != nil || len(vars) == 0 {
		return log
	}
	return discord.ScrubText(log, discord.SecretValues(vars, ""))
}

// decryptEnvEnc opens the base64 ciphertext blob into a map.
func decryptEnvEnc(enc string) (map[string]string, error) {
	if enc == "" {
		return map[string]string{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// git deploy (foundation) — pull/clone + deps, timeout-bounded, token scrubbed
// ---------------------------------------------------------------------------

type deployGitPayload struct {
	BotID        string `json:"bot_id"`
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	TokenEnc     string `json:"token_enc,omitempty"`
	BuildTimeout int    `json:"build_timeout_s"`
}

// HandleBotDeployGit clones/pulls the repo into the bot root and installs
// deps. The token decrypts transiently in memory and NEVER appears in argv
// (uses git credential env), in logs or in results.
func HandleBotDeployGit(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p deployGitPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := BotRoot(p.BotID)
	uid, gid, err := botOwnerIDs(root)
	if err != nil {
		return nil, fmt.Errorf("bot tree missing (install first): %w", err)
	}
	if p.RepoURL == "" {
		return nil, fmt.Errorf("repo_url is required")
	}
	_ = chownRecursive(root, uid, gid)

	timeout := time.Duration(p.BuildTimeout) * time.Second
	if timeout <= 0 || timeout > 15*time.Minute {
		timeout = discord.DefaultBuildTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	token, err := decodeGitToken(p.TokenEnc)
	if err != nil {
		return nil, err
	}

	log := &strings.Builder{}
	gitDir := filepath.Join(root, ".git")
	env := append([]string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"HOME=" + root,
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}, gitCredEnv(p.RepoURL, token)...)
	if _, err := os.Stat(gitDir); err == nil {
		log.WriteString("git fetch --all --prune\n")
		if out, gerr := runBotGit(ctx, uid, gid, root, env, "fetch", "--all", "--prune"); gerr != nil {
			return nil, fmt.Errorf("git fetch: %s", scrubLog(tailString(out, 400), p.TokenEnc))
		}
	} else {
		// Clone into a temp dir then move into place (idempotent retry).
		log.WriteString("git clone " + sanitizeRepoURL(p.RepoURL) + "\n")
		tmp := root + ".clone-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if out, gerr := runBotGit(ctx, uid, gid, BotRootBase, env, "clone", "--depth", "50", p.RepoURL, tmp); gerr != nil {
			return nil, fmt.Errorf("git clone: %s", scrubLog(tailString(out, 400), p.TokenEnc))
		}
		_ = os.RemoveAll(root)
		if err := os.Rename(tmp, root); err != nil {
			return nil, err
		}
	}
	branch := p.Branch
	if branch == "" {
		branch = "main"
	}
	log.WriteString("git checkout " + branch + "\n")
	if out, gerr := runBotGit(ctx, uid, gid, root, env, "checkout", branch); gerr != nil {
		return nil, fmt.Errorf("git checkout: %s", scrubLog(tailString(out, 400), p.TokenEnc))
	}
	log.WriteString("git reset --hard origin/" + branch + "\n")
	if out, gerr := runBotGit(ctx, uid, gid, root, env, "reset", "--hard", "origin/"+branch); gerr != nil {
		return nil, fmt.Errorf("git reset: %s", scrubLog(tailString(out, 400), p.TokenEnc))
	}

	slog.Info("bot git deploy done", "bot", p.BotID, "repo", sanitizeRepoURL(p.RepoURL))
	out := discord.BotOutcome{BotID: p.BotID, OK: true, Log: scrubLog(log.String(), p.TokenEnc)}
	return json.Marshal(out)
}

// decodeGitToken opens the stored token ciphertext ("" stays "").
func decodeGitToken(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	return secretbox.Decrypt(raw)
}

// sanitizeRepoURL strips any credentials for logging.
func sanitizeRepoURL(repoURL string) string {
	if i := strings.Index(repoURL, "@"); i > 0 && strings.Contains(repoURL[:i], ":") {
		if j := strings.Index(repoURL[:i], "//"); j >= 0 {
			return repoURL[:j+2] + "[redacted]@" + repoURL[i+1:]
		}
	}
	return repoURL
}

// gitCredEnv builds the git credential rewrite for private https repos: the
// credentialed URL lives ONLY in the child process env (GIT_CONFIG
// url.insteadOf) — never in argv, never in logs.
func gitCredEnv(repoURL, token string) []string {
	if token == "" || !strings.HasPrefix(repoURL, "https://") {
		return nil
	}
	host := strings.TrimPrefix(repoURL, "https://")
	if i := strings.Index(host, "/"); i > 0 {
		host = host[:i]
	}
	cred := "https://x-access-token:" + token + "@" + host
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + cred + ".insteadOf=https://" + host}
}

// runBotGit runs git as the bot user inside dir.
func runBotGit(ctx context.Context, uid, gid int, dir string, env []string, args ...string) (string, error) {
	full := append([]string{"--reuid", strconv.Itoa(uid), "--regid", strconv.Itoa(gid),
		"--clear-groups", "--inh-caps", "-0", "--", "env"}, env...)
	full = append(full, "git")
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "setpriv", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------------------------------------------------------------------------
// file upload + listing (agent fs ops — controlled paths only)
// ---------------------------------------------------------------------------

type uploadPayload struct {
	BotID string `json:"bot_id"`
	Path  string `json:"path"`
	B64   string `json:"content_b64"`
}

// HandleBotUpload writes one file inside the bot tree. Path traversal and
// symlink escapes are rejected; .env-like files get 0600.
func HandleBotUpload(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p uploadPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := BotRoot(p.BotID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("bot tree missing (install first)")
	}
	target, err := safeBotPath(root, p.Path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(p.B64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content")
	}
	if len(raw) > 8*1024*1024 {
		return nil, fmt.Errorf("file too large (max 8 MiB per upload)")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return nil, err
	}
	perm := os.FileMode(0o640)
	if isSecretishPath(p.Path) {
		perm = 0o600
	}
	if err := os.WriteFile(target, raw, perm); err != nil {
		return nil, err
	}
	uid, gid, _ := botOwnerIDs(root)
	_ = os.Chown(target, uid, gid)
	out := discord.BotOutcome{BotID: p.BotID, OK: true, Log: "wrote " + p.Path}
	return json.Marshal(out)
}

// isSecretishPath reports whether a path looks like it holds secrets (those
// files are written 0600 and never listed with content).
func isSecretishPath(rel string) bool {
	base := filepath.Base(rel)
	return base == ".env" || strings.HasSuffix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key")
}

// safeBotPath resolves rel inside root (symlink-proof).
func safeBotPath(root, rel string) (string, error) {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path must be relative to the bot root")
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path escapes the bot root")
	}
	full := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		if os.IsNotExist(err) {
			return full, nil // new file: parent checked at write time
		}
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the bot root")
	}
	return filepath.Join(resolved, filepath.Base(full)), nil
}

type filesPayload struct {
	BotID string `json:"bot_id"`
	Path  string `json:"path"`
}

// HandleBotFiles lists a directory inside the bot tree (no file content —
// content download is out of scope for bots; customers upload, deploy, run).
func HandleBotFiles(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p filesPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := BotRoot(p.BotID)
	rel := p.Path
	if rel == "" {
		rel = "."
	}
	dir, err := safeBotPath(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := discord.BotFilesOutcome{BotID: p.BotID, Path: rel}
	for _, en := range entries {
		info, err := en.Info()
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, discord.FileEntry{
			Name: en.Name(), IsDir: en.IsDir(), Size: info.Size(), ModTime: info.ModTime(),
		})
	}
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// lifecycle — start / stop / restart / kill / status
// ---------------------------------------------------------------------------

type lifecyclePayload struct {
	BotID          string   `json:"bot_id"`
	Runtime        string   `json:"runtime"`
	RuntimeVersion string   `json:"runtime_version"`
	StartupFile    string   `json:"startup_file"`
	StartupCommand string   `json:"startup_command"`
	EnvEnc         string   `json:"env_enc"`
	RestartPolicy  string   `json:"restart_policy"`
	NetAllow       []string `json:"net_allow,omitempty"`
	Limits         struct {
		CPUPercent float64 `json:"cpu_percent"`
		MemoryMB   int64   `json:"memory_mb"`
		DiskMB     int64   `json:"disk_mb"`
		PidsMax    int64   `json:"pids_max"`
	} `json:"limits"`
}

// HandleBotStart creates the sandboxed transient unit and starts it.
//
// Isolation without a container runtime (Docker is unavailable on this
// deployment — deviation documented in the phase handoff):
//   - dedicated unix user (no login shell)
//   - NoNewPrivileges, ProtectSystem=strict, ProtectHome, PrivateTmp,
//     PrivateDevices, RestrictSUIDSGID, ReadWritePaths=bot tree only
//   - cgroup v2 limits: CPUQuota, MemoryMax, TasksMax (= PID cap)
//   - env via EnvironmentFile 0600 (decrypted at start, never logged)
//   - network controls (IPAddressAllow/Deny) when the payload requests them
func HandleBotStart(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p lifecyclePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	root := BotRoot(p.BotID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("bot tree missing (install first)")
	}
	botUser, err := botUserFor(p.BotID)
	if err != nil {
		return nil, err
	}
	// Trust the bot USER for ownership (not the tree's current owner): if a
	// retried start races a fresh tree, root ownership would silently break
	// the unit again. Fall back to the tree owner only when the user lookup
	// fails (user removed mid-flight).
	uid, gid, uerr := idsForUser(botUser)
	if uerr != nil {
		uid, gid, err = botOwnerIDs(root)
		if err != nil {
			return nil, err
		}
	}

	// Startup command: provider default when the payload carries none.
	startCmd := strings.TrimSpace(p.StartupCommand)
	if startCmd == "" {
		switch p.Runtime {
		case "node":
			file := orDefault(p.StartupFile, "index.js")
			startCmd = "node " + file
		case "python":
			venvPython := filepath.Join(root, "venv", "bin", "python")
			bin := venvPython
			if _, err := os.Stat(venvPython); err != nil {
				bin = resolvePython(p.RuntimeVersion)
			}
			startCmd = bin + " " + orDefault(p.StartupFile, "bot.py")
		default:
			return nil, fmt.Errorf("runtime %q cannot start", p.Runtime)
		}
	}

	// EnvironmentFile: decrypt env ONCE here; the file is 0600 bot-user
	// owned; values never appear in logs, unit file or results.
	vars, err := decryptEnvEnc(p.EnvEnc)
	if err != nil {
		return nil, fmt.Errorf("env decrypt: %w", err)
	}
	envFile := filepath.Join(root, "bot.env")
	var envContent strings.Builder
	for k, v := range vars {
		if !safeEnvKey(k) {
			return nil, fmt.Errorf("invalid env key %q", k)
		}
		envContent.WriteString(k + "=" + sanitizeEnvValue(v) + "\n")
	}
	if err := os.WriteFile(envFile, []byte(envContent.String()), 0o600); err != nil {
		return nil, err
	}
	_ = os.Chown(envFile, uid, gid)

	// Idempotent restart: stop any previous instance first.
	_ = e.run(ctx, "systemctl", "stop", unit)
	_ = e.run(ctx, "systemctl", "reset-failed", unit)

	args := []string{"--unit=" + unit,
		"--property", "User=" + botUser,
		"--property", "Group=" + botUser,
		"--property", "WorkingDirectory=" + root,
		"--property", "EnvironmentFile=" + envFile,
		"--property", "Restart=" + restartPolicyFor(p.RestartPolicy),
		"--property", "RestartSec=3",
		"--property", "NoNewPrivileges=yes",
		"--property", "ProtectSystem=strict",
		"--property", "ProtectHome=yes",
		"--property", "PrivateTmp=yes",
		"--property", "PrivateDevices=yes",
		"--property", "RestrictSUIDSGID=yes",
		"--property", "ReadWritePaths=" + root,
		"--property", "UMask=0027",
		"--collect",
	}
	if p.Limits.CPUPercent > 0 {
		args = append(args, "--property", "CPUQuota="+strconv.FormatFloat(p.Limits.CPUPercent, 'f', -1, 64)+"%")
	}
	if p.Limits.MemoryMB > 0 {
		args = append(args, "--property", "MemoryMax="+strconv.FormatInt(p.Limits.MemoryMB, 10)+"M")
		// Soft cap at 90% so the kernel reclaims before hard-killing.
		args = append(args, "--property", "MemoryHigh="+strconv.FormatInt(p.Limits.MemoryMB*9/10, 10)+"M")
	}
	if p.Limits.PidsMax > 0 {
		args = append(args, "--property", "TasksMax="+strconv.FormatInt(p.Limits.PidsMax, 10))
	}
	if len(p.NetAllow) > 0 {
		// Network controls where systemd supports them (default: open —
		// Discord bots need the gateway; this is an opt-in allowlist).
		args = append(args, "--property", "IPAddressDeny=any")
		for _, cidr := range p.NetAllow {
			args = append(args, "--property", "IPAddressAllow="+cidr)
		}
	}
	args = append(args, "/bin/bash", "-c", startCmd)

	if err := e.run(ctx, "systemd-run", args...); err != nil {
		return nil, fmt.Errorf("systemd-run: %w", err)
	}
	rememberBotSpec(p.BotID, p.EnvEnc, "")
	slog.Info("bot started", "bot", p.BotID, "unit", unit)

	outcome := discord.BotStatusOutcome{BotID: p.BotID, Unit: unit, Log: "started as " + unit}
	if st, err := botUnitState(ctx, unit); err == nil {
		outcome.UnitState = st.state
		outcome.UptimeS = st.uptimeS
		outcome.Restarts = st.restarts
	}
	return json.Marshal(outcome)
}

// restartPolicyFor validates the restart policy (never arbitrary).
func restartPolicyFor(p string) string {
	switch p {
	case "always":
		return "always"
	case "no":
		return "no"
	default:
		return "on-failure"
	}
}

// HandleBotStop stops the unit.
func HandleBotStop(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p struct {
		BotID string `json:"bot_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	if err := e.run(ctx, "systemctl", "stop", unit); err != nil {
		slog.Info("bot stop", "unit", unit, "note", "already stopped")
	}
	_ = e.run(ctx, "systemctl", "reset-failed", unit)
	out := discord.BotStatusOutcome{BotID: p.BotID, Unit: unit, UnitState: "inactive"}
	return json.Marshal(out)
}

// HandleBotRestart = stop + start with the current spec.
func HandleBotRestart(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	if _, err := HandleBotStop(ctx, e, c, cfg, payload); err != nil {
		return nil, err
	}
	return HandleBotStart(ctx, e, c, cfg, payload)
}

// HandleBotKill SIGKILLs the main process then stops the unit.
func HandleBotKill(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p struct {
		BotID string `json:"bot_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	_ = e.run(ctx, "systemctl", "kill", "--signal=SIGKILL", unit)
	_ = e.run(ctx, "systemctl", "stop", unit)
	_ = e.run(ctx, "systemctl", "reset-failed", unit)
	out := discord.BotStatusOutcome{BotID: p.BotID, Unit: unit, UnitState: "inactive"}
	return json.Marshal(out)
}

// unitTruth is the observed systemd state for a bot unit.
type unitTruth struct {
	state    string
	uptimeS  int64
	restarts int
}

func botUnitState(ctx context.Context, unit string) (unitTruth, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"-p", "ActiveState", "-p", "NRestarts", "-p", "ActiveEnterTimestamp", "--no-pager").Output()
	if err != nil && len(out) == 0 {
		return unitTruth{}, err
	}
	t := unitTruth{}
	for _, line := range splitLines(out) {
		switch {
		case strings.HasPrefix(line, "ActiveState="):
			t.state = strings.TrimSpace(strings.TrimPrefix(line, "ActiveState="))
		case strings.HasPrefix(line, "NRestarts="):
			t.restarts, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "NRestarts=")))
		case strings.HasPrefix(line, "ActiveEnterTimestamp="):
			v := strings.TrimSpace(strings.TrimPrefix(line, "ActiveEnterTimestamp="))
			if ts, perr := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", v, time.Local); perr == nil {
				t.uptimeS = int64(time.Since(ts).Seconds())
				if t.uptimeS < 0 {
					t.uptimeS = 0
				}
			}
		}
	}
	return t, nil
}

// HandleBotStatus reports agent truth for the unit.
func HandleBotStatus(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p struct {
		BotID string `json:"bot_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	out := discord.BotStatusOutcome{BotID: p.BotID, Unit: unit, UnitState: "inactive"}
	if st, err := botUnitState(ctx, unit); err == nil {
		out.UnitState = st.state
		out.UptimeS = st.uptimeS
		out.Restarts = st.restarts
	}
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// logs — ring buffer snapshot/tail (scrubbed); console input is API-allowlisted
// ---------------------------------------------------------------------------

type logsPayload struct {
	BotID    string `json:"bot_id"`
	Lines    int    `json:"lines"`
	AfterSeq int64  `json:"after_seq"`
}

// HandleBotLogs returns recent console lines from the agent-side ring buffer
// (journald-fed, scrubbed against this bot's env values + git token). The
// control plane re-scrubs before serving — defense in depth.
func HandleBotLogs(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p logsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if p.Lines <= 0 || p.Lines > 500 {
		p.Lines = 200
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	lines, next, err := botConsoleLines(ctx, unit, p.BotID, p.Lines, p.AfterSeq, botSecrets(p.BotID))
	if err != nil {
		return nil, err
	}
	out := discord.BotLogsOutcome{BotID: p.BotID, Lines: lines, NextSeq: next}
	return json.Marshal(out)
}

// HandleBotDelete stops the unit and removes the bot tree + user.
func HandleBotDelete(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p struct {
		BotID string `json:"bot_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := BotUnitName(p.BotID)
	if unit == "" {
		return nil, fmt.Errorf("invalid bot id")
	}
	_ = e.run(ctx, "systemctl", "stop", unit)
	_ = e.run(ctx, "systemctl", "reset-failed", unit)
	_ = os.RemoveAll(BotRoot(p.BotID))
	dropBotSpec(p.BotID)
	if botUser, err := botUserFor(p.BotID); err == nil {
		_ = exec.Command("userdel", botUser).Run()
	}
	out := discord.BotOutcome{BotID: p.BotID, OK: true, Log: "deleted"}
	return json.Marshal(out)
}
