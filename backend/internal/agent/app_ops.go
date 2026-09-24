package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// ============================================================================
// APPLICATION LIFECYCLE (node / python / go)
// ============================================================================

// AppSpec is the desired state for a process-based application.
type AppSpec struct {
	WebsiteID      string            `json:"website_id"`
	UnixUser       string            `json:"unix_user"`
	Runtime        string            `json:"runtime"` // node | python | go
	RuntimeVersion string            `json:"runtime_version"`
	AppRoot        string            `json:"app_root"`        // empty = legacy app/ workspace; releases pass the public path
	StartupFile    string            `json:"startup_file"`    // node: server.js; python: wsgi.py module path
	StartupCommand string            `json:"startup_command"` // go: binary path or node fallback
	BuildCommand   string            `json:"build_command"`   // npm ci && npm run build / pip install / go build
	InternalPort   int               `json:"internal_port"`
	Env            map[string]string `json:"env"`
	// EnvEnc is the base64 secretbox ciphertext of the JSON env map (the
	// control plane encrypts env at rest; it travels ciphertext-only, same
	// trust model as the deploy token). Decoded into Env by decodeEnv.
	EnvEnc string `json:"app_env_enc,omitempty"`
}

// decodeEnv merges the encrypted env payload into p.Env.
func (p *AppSpec) decodeEnv() error {
	if p.EnvEnc == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(p.EnvEnc)
	if err != nil {
		return fmt.Errorf("decode app env: %w", err)
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return fmt.Errorf("decrypt app env: %w", err)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(plain), &env); err != nil {
		return fmt.Errorf("parse app env: %w", err)
	}
	if p.Env == nil {
		p.Env = env
	} else {
		for k, v := range env {
			p.Env[k] = v
		}
	}
	p.EnvEnc = ""
	return nil
}

// appRootFor resolves the build/run directory: the spec override (release
// layout — apps build and run inside the release the public symlink points
// at) or the legacy app/ workspace. An override must stay inside the site
// tree (defense in depth; the control plane already constrains it).
func appRootFor(p AppSpec) (string, error) {
	base := filepath.Join("/srv/epicpanel/websites", p.WebsiteID)
	if p.AppRoot == "" {
		return filepath.Join(base, "app"), nil
	}
	clean := filepath.Clean(p.AppRoot)
	if clean != base && !strings.HasPrefix(clean, base+string(filepath.Separator)) {
		return "", fmt.Errorf("app_root escapes the site tree")
	}
	return clean, nil
}

// AppServiceName is the systemd unit for an application process.
func AppServiceName(websiteID string) string {
	return "epicpanel-app-" + websiteID
}

// AppPort allocates a deterministic internal port from the website UUID so
// apps never collide: 10000 + (hash % 20000) → 10000..29999.
func AppPort(websiteID string) (int, error) {
	if _, err := uuid.Parse(websiteID); err != nil {
		return 0, fmt.Errorf("invalid website id")
	}
	sum := 0
	for _, c := range strings.ReplaceAll(websiteID, "-", "") {
		sum = (sum*31 + int(c)) % 20000
	}
	return 10000 + sum, nil
}

// DeployApp prepares an application for its runtime: dirs, deps, build.
// Node: npm ci/install (+ optional build). Python: venv + pip install.
// Go: go build to bin/app. Runs under the SITE USER (never root).
func (e *Executor) DeployApp(ctx context.Context, p AppSpec) (*AppOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if err := p.decodeEnv(); err != nil {
		return nil, err
	}
	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("site owner: %w", err)
	}
	appRoot, err := appRootFor(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(appRoot, 0o750); err != nil {
		return nil, err
	}

	var buildLog strings.Builder
	switch p.Runtime {
	case "node":
		if err := e.deployNodeApp(ctx, p, appRoot, &buildLog); err != nil {
			return nil, err
		}
	case "python":
		if err := e.deployPythonApp(ctx, p, appRoot, &buildLog); err != nil {
			return nil, err
		}
	case "go":
		if err := e.deployGoApp(ctx, p, appRoot, &buildLog); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("runtime %q has no app deployment", p.Runtime)
	}

	_ = chownRecursive(appRoot, uid, gid)
	slog.Info("app deployed", "website", p.WebsiteID, "runtime", p.Runtime)
	return &AppOutcome{AppRoot: appRoot, Log: buildLog.String()}, nil
}

// runAsSite executes a command as the site user inside the app root via
// setpriv (drop privileges in-process; never run user code as root).
func (e *Executor) runAsSite(ctx context.Context, uid, gid int, dir string, name string, args ...string) (string, error) {
	return e.runAsSiteEnv(ctx, uid, gid, dir, nil, name, args...)
}

// runAsSiteEnv is runAsSite with extra env assignments appended (a later
// duplicate key wins, so a PATH here overrides the default one).
func (e *Executor) runAsSiteEnv(ctx context.Context, uid, gid int, dir string, env []string, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	// No --inh-caps: setpriv variants reject numeric specs ("unknown
	// capability 0"), and dropping uid/gid already sheds capabilities.
	full := append([]string{"--reuid", strconv.Itoa(uid), "--regid", strconv.Itoa(gid),
		"--clear-groups", "--",
		"env", "HOME=" + dir, "USER=" + fmt.Sprint(uid), "TERM=xterm",
		"PATH=/usr/local/bin:/usr/bin:/bin"},
		env...)
	full = append(full, name)
	full = append(full, args...)
	cmd := exec.CommandContext(c, "setpriv", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// nodeRuntimeBinDir returns the bin dir of the site's managed Node major
// (/opt/epicpanel/node/<major>/bin), or "" when that major is not managed —
// PATH resolution applies then (system-installed Node).
func nodeRuntimeBinDir(version string) string {
	major := nodeMajor(version)
	if major == "" || !fileExists(nodeManagedBin(major)) {
		return ""
	}
	return filepath.Join(nodeRootBase, major, "bin")
}

func (e *Executor) deployNodeApp(ctx context.Context, p AppSpec, appRoot string, log *strings.Builder) error {
	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return err
	}
	// Pin the build to the site's managed Node major: absolute npm plus its
	// bin dir prepended to PATH (npm scripts spawn node via PATH, and the
	// system-wide `node` may be a completely different major).
	npmBin, env := "npm", []string(nil)
	if binDir := nodeRuntimeBinDir(p.RuntimeVersion); binDir != "" {
		npmBin = filepath.Join(binDir, "npm")
		env = []string{"PATH=" + binDir + ":/usr/local/bin:/usr/bin:/bin"}
	}
	lockFile := filepath.Join(appRoot, "package-lock.json")
	if _, err := os.Stat(lockFile); err == nil {
		log.WriteString("npm ci...\n")
		if out, err := e.runAsSiteEnv(ctx, uid, gid, appRoot, env, npmBin, "ci", "--no-audit", "--no-fund"); err != nil {
			return fmt.Errorf("npm ci: %s (%w)", tailString(out, 400), err)
		}
	} else if _, err := os.Stat(filepath.Join(appRoot, "package.json")); err == nil {
		log.WriteString("npm install...\n")
		if out, err := e.runAsSiteEnv(ctx, uid, gid, appRoot, env, npmBin, "install", "--no-audit", "--no-fund"); err != nil {
			return fmt.Errorf("npm install: %s (%w)", tailString(out, 400), err)
		}
	}
	if p.BuildCommand != "" {
		script := strings.TrimPrefix(strings.TrimSpace(p.BuildCommand), "npm run ")
		log.WriteString("npm run " + script + "...\n")
		if out, err := e.runAsSiteEnv(ctx, uid, gid, appRoot, env, npmBin, "run", script); err != nil {
			return fmt.Errorf("npm run %s: %s (%w)", script, tailString(out, 400), err)
		}
	}
	return nil
}

func (e *Executor) deployPythonApp(ctx context.Context, p AppSpec, appRoot string, log *strings.Builder) error {
	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return err
	}
	version := p.RuntimeVersion
	if version == "" {
		version = "3.12"
	}
	venv := filepath.Join(appRoot, "venv")
	if _, err := os.Stat(filepath.Join(venv, "bin", "python")); os.IsNotExist(err) {
		log.WriteString("python" + versionMajorDot(version) + " -m venv venv...\n")
		if out, err := e.runAsSite(ctx, uid, gid, appRoot,
			"python"+versionMajorDot(version), "-m", "venv", venv); err != nil {
			return fmt.Errorf("venv create: %s (%w)", tailString(out, 400), err)
		}
	}
	if req := filepath.Join(appRoot, "requirements.txt"); fileExists(req) {
		log.WriteString("pip install -r requirements.txt...\n")
		if out, err := e.runAsSite(ctx, uid, gid, appRoot,
			filepath.Join(venv, "bin", "pip"), "install", "-r", "requirements.txt"); err != nil {
			return fmt.Errorf("pip install: %s (%w)", tailString(out, 400), err)
		}
	}
	return nil
}

func (e *Executor) deployGoApp(ctx context.Context, p AppSpec, appRoot string, log *strings.Builder) error {
	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return err
	}
	binDir := filepath.Join(appRoot, "bin")
	_ = os.MkdirAll(binDir, 0o750)
	binPath := filepath.Join(binDir, "app")
	if _, err := os.Stat(binPath); err == nil {
		log.WriteString("binary already present; skipping build\n")
		return nil
	}
	log.WriteString("go build -o bin/app ./...\n")
	goBin := "/usr/local/bin/go"
	if _, err := exec.LookPath("go"); err == nil {
		goBin = "go"
	}
	// GOCACHE/GOMODCACHE under the app root so the build runs unprivileged.
	if out, err := e.runAsSite(ctx, uid, gid, appRoot,
		"env", "GOCACHE="+filepath.Join(appRoot, ".gocache"),
		"GOMODCACHE="+filepath.Join(appRoot, ".gomodcache"),
		"HOME="+appRoot,
		goBin, "build", "-o", binPath, "./..."); err != nil {
		return fmt.Errorf("go build: %s (%w)", tailString(out, 400), err)
	}
	return nil
}

// AppOutcome is the job result payload for provision/build operations.
type AppOutcome struct {
	AppRoot string `json:"app_root"`
	Log     string `json:"log"`
}

// StartApp creates a supervised systemd service for the application and
// starts it. systemd is the process manager: restart policy, cgroup resource
// control, journald logs, graceful shutdown come from systemd — not from us.
func (e *Executor) StartApp(ctx context.Context, p AppSpec) (*AppOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if !safeIdentifier(strings.ReplaceAll(p.UnixUser, "ep-", "ep_")) && !strings.HasPrefix(p.UnixUser, "ep-") {
		return nil, fmt.Errorf("invalid unix user")
	}
	if err := p.decodeEnv(); err != nil {
		return nil, err
	}
	if p.InternalPort <= 0 {
		port, err := AppPort(p.WebsiteID)
		if err != nil {
			return nil, err
		}
		p.InternalPort = port
	}
	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("site owner: %w", err)
	}
	appRoot, err := appRootFor(p)
	if err != nil {
		return nil, err
	}

	startCmd := p.StartupCommand
	if startCmd == "" {
		switch p.Runtime {
		case "node":
			// Absolute managed binary — the unit must never depend on PATH's
			// `node`, which is whichever major owns the default shim.
			nodeBin := "node"
			if binDir := nodeRuntimeBinDir(p.RuntimeVersion); binDir != "" {
				nodeBin = filepath.Join(binDir, "node")
			}
			startCmd = nodeBin + " " + filepath.Base(orDefault(p.StartupFile, "server.js"))
		case "python":
			venvBin := filepath.Join(appRoot, "venv", "bin")
			if fileExists(filepath.Join(venvBin, "gunicorn")) {
				startCmd = filepath.Join(venvBin, "gunicorn") + " --workers 2 --bind 127.0.0.1:" + strconv.Itoa(p.InternalPort) + " " + orDefault(p.StartupFile, "app:app")
			} else if fileExists(filepath.Join(venvBin, "uvicorn")) {
				startCmd = filepath.Join(venvBin, "uvicorn") + " --host 127.0.0.1 --port " + strconv.Itoa(p.InternalPort) + " " + orDefault(p.StartupFile, "app:app")
			} else {
				return nil, fmt.Errorf("python app needs gunicorn or uvicorn in requirements.txt (WSGI/ASGI server)")
			}
		case "go":
			startCmd = filepath.Join(appRoot, "bin", "app")
		default:
			return nil, fmt.Errorf("runtime %q has no app process", p.Runtime)
		}
	}

	// Environment file (0600, site-user owned) — secrets never in the unit.
	envFile := filepath.Join("/srv/epicpanel/websites", p.WebsiteID, "app.env")
	envContent := "PORT=" + strconv.Itoa(p.InternalPort) + "\n"
	for k, v := range p.Env {
		if !safeEnvKey(k) {
			return nil, fmt.Errorf("invalid env key %q", k)
		}
		envContent += k + "=" + sanitizeEnvValue(v) + "\n"
	}
	if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
		return nil, err
	}
	_ = os.Chown(envFile, uid, gid)

	// Transient systemd unit via systemd-run: scoped to the site user, with
	// resource controls (memory/process caps), auto-restart, journald logs.
	unit := AppServiceName(p.WebsiteID)
	execStart := strings.Builder{}
	execStart.WriteString("/bin/bash -lc ")
	execStart.WriteString("'")
	execStart.WriteString(strings.ReplaceAll(startCmd, "'", `'\''`))
	execStart.WriteString("'")

	stop := e.run(ctx, "systemctl", "stop", unit) // idempotent restart
	_ = stop
	runArgs := []string{"systemd-run", "--unit=" + unit,
		"--property", "User=" + p.UnixUser,
		"--property", "WorkingDirectory=" + appRoot,
		"--property", "EnvironmentFile=" + envFile,
		"--property", "Restart=on-failure",
		"--property", "RestartSec=3",
		"--property", "MemoryMax=512M",
		"--property", "TasksMax=128",
		"--property", "NoNewPrivileges=yes",
		"--property", "ProtectSystem=strict",
		"--property", "ReadWritePaths=" + appRoot + " " + filepath.Join("/srv/epicpanel/websites", p.WebsiteID, "logs"),
		"--property", "PrivateTmp=yes",
	}
	// Pin the unit's PATH to the site's managed Node major so the startup
	// command — and anything it spawns — resolves the right node/npm even
	// in a custom startup command. bash -c does not reset PATH (no login
	// shell), so this holds.
	if p.Runtime == "node" {
		if binDir := nodeRuntimeBinDir(p.RuntimeVersion); binDir != "" {
			runArgs = append(runArgs, "--property",
				"Environment=PATH="+binDir+":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
		}
	}
	runArgs = append(runArgs, "--collect", "/bin/bash", "-c", startCmd)
	if err := e.run(ctx, runArgs[0], runArgs[1:]...); err != nil {
		return nil, fmt.Errorf("systemd-run: %w", err)
	}
	if err := e.run(ctx, "systemctl", "enable", unit); err != nil {
		slog.Warn("systemctl enable app unit", "unit", unit, "err", err)
	}
	slog.Info("app started", "website", p.WebsiteID, "unit", unit, "port", p.InternalPort)
	return &AppOutcome{AppRoot: appRoot, Log: "started as " + unit + " on 127.0.0.1:" + strconv.Itoa(p.InternalPort)}, nil
}

// StopApp stops and removes the transient unit.
func (e *Executor) StopApp(ctx context.Context, websiteID string) error {
	unit := AppServiceName(websiteID)
	if err := e.run(ctx, "systemctl", "stop", unit); err != nil {
		slog.Info("app stop", "unit", unit, "note", "already stopped")
	}
	_ = e.run(ctx, "systemctl", "reset-failed", unit)
	slog.Info("app stopped", "website", websiteID)
	return nil
}

// RestartApp = stop + start with the current spec.
func (e *Executor) RestartApp(ctx context.Context, p AppSpec) (*AppOutcome, error) {
	_ = e.StopApp(ctx, p.WebsiteID)
	return e.StartApp(ctx, p)
}

// AppStatus inspects the systemd unit + process health.
type AppStatus struct {
	UnitState string `json:"unit_state"`
	Port      int    `json:"port"`
	Healthy   bool   `json:"healthy"`
	Detail    string `json:"detail"`
}

func (e *Executor) AppStatus(ctx context.Context, websiteID string, port int) (*AppStatus, error) {
	unit := AppServiceName(websiteID)
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if err != nil {
		// systemctl exits non-zero for inactive/failed
	}
	st := &AppStatus{UnitState: state, Port: port}
	switch state {
	case "active":
		st.Healthy = true
		if port > 0 {
			conn, derr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
			if derr != nil {
				st.Healthy = false
				st.Detail = "process active but port " + strconv.Itoa(port) + " not listening"
			} else {
				conn.Close()
				st.Detail = "listening"
			}
		}
	case "failed":
		st.Detail = "service failed — check journalctl -u " + unit
	default:
		st.Detail = "state: " + state
	}
	return st, nil
}

// AppLogs returns the last N lines of journald output for the app unit.
func (e *Executor) AppLogs(ctx context.Context, websiteID string, lines int) (string, error) {
	if lines <= 0 || lines > 500 {
		lines = 100
	}
	unit := AppServiceName(websiteID)
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat").Output()
	if err != nil {
		return "", fmt.Errorf("journalctl: %w", err)
	}
	return string(out), nil
}

// CleanupApp stops the service and removes env/unit traces on site delete.
func (e *Executor) CleanupApp(ctx context.Context, websiteID string) error {
	_ = e.StopApp(ctx, websiteID)
	_ = os.Remove(filepath.Join("/srv/epicpanel/websites", websiteID, "app.env"))
	return nil
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func safeEnvKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// sanitizeEnvValue strips newlines so a value can't inject extra env lines.
func sanitizeEnvValue(v string) string {
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	if len(v) > 2000 {
		v = v[:2000]
	}
	return v
}

func tailString(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
