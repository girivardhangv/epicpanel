package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PoolSpec describes a per-website PHP-FPM pool (isolated execution context
// under the shared PHP version binary — the core EpicPanel model).
type PoolSpec struct {
	WebsiteID     string
	UnixUser      string
	RuntimeVer    string // e.g. "8.3"
	DocumentRoot  string
	PrimaryDomain string
	// Limits (production-safe defaults; control plane will make these tunable)
	PMMaxChildren  int
	PMStartServers int
	PMMinSpare     int
	PMMaxSpare     int
	ProcessMemory  string // php admin_value[memory_limit]
	// OpenBaseDir overrides php_admin_value[open_basedir]: empty defaults to
	// the site tree + /tmp; the sentinel "-" omits the line entirely (used by
	// the reserved dbadmin pool, which needs the distro session save_path).
	OpenBaseDir string
	// PHPSettings are validated per-site php.ini overrides (see
	// websites.NormalizePHPSettings) rendered as php_admin_value/flag lines.
	PHPSettings map[string]string
	// RequestTerminateTimeout caps one request's wall time in seconds;
	// 0 = agent default (300).
	RequestTerminateTimeout int
}

// dbadminPoolSiteID is the reserved (non-website) pool for the panel's
// database tools; its pool file is epicpanel-dbadmin.conf.
const dbadminPoolSiteID = "dbadmin"

const fpmSocketDir = "/run/epicpanel/php-fpm"

// RenderPoolConfig renders the per-site php-fpm pool file. Deliberately
// explicit values over distro defaults: no process group sharing between
// sites, dedicated socket per site, hardened php_admin settings, and the
// per-site INI overrides from the MultiPHP-style settings editor.
func RenderPoolConfig(p PoolSpec) string {
	defaults(&p)
	socket := fmt.Sprintf("%s/%s.sock", fpmSocketDir, p.WebsiteID)
	baseDir := p.OpenBaseDir
	switch baseDir {
	case "":
		baseDir = "/srv/epicpanel/websites/" + p.WebsiteID + ":/tmp"
	case "-":
		baseDir = ""
	}
	openBaseDirLine := ""
	if baseDir != "" {
		openBaseDirLine = fmt.Sprintf("php_admin_value[open_basedir] = %s\n", baseDir)
	}

	// FPM watchdog: kill a worker whose request runs past the cap. An
	// unlimited max_execution_time (0) must also lift the watchdog.
	timeout := p.RequestTerminateTimeout
	if timeout <= 0 {
		timeout = 300
	}
	if p.PHPSettings["max_execution_time"] == "0" {
		timeout = 0
	}

	return fmt.Sprintf(`[epicpanel-%s]
user = %s
group = %s
listen = %s
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = dynamic
pm.max_children = %d
pm.start_servers = %d
pm.min_spare_servers = %d
pm.max_spare_servers = %d
pm.max_requests = 500
request_terminate_timeout = %d
request_slowlog_timeout = 10s
slowlog = /srv/epicpanel/websites/%s/logs/php-fpm-slow.log
php_admin_value[error_log] = /srv/epicpanel/websites/%s/logs/php-error.log
php_admin_flag[log_errors] = on
%s%scatch_workers_output = yes
`, p.WebsiteID, p.UnixUser, p.UnixUser, socket,
		p.PMMaxChildren, p.PMStartServers, p.PMMinSpare, p.PMMaxSpare,
		timeout, p.WebsiteID, p.WebsiteID, openBaseDirLine, renderPHPAdminSettings(p))
}

// phpFlagSettings are directives rendered as php_admin_flag (On/Off) rather
// than php_admin_value.
var phpFlagSettings = map[string]bool{
	"allow_url_fopen":             true,
	"display_errors":              true,
	"short_open_tag":              true,
	"opcache.enable":              true,
	"opcache.validate_timestamps": true,
}

// renderPHPAdminSettings emits the per-site INI overrides as php_admin_value /
// php_admin_flag lines. Secure defaults are overlaid with the validated
// website_php_settings map (keys already vetted by NormalizePHPSettings).
func renderPHPAdminSettings(p PoolSpec) string {
	vals := map[string]string{
		"memory_limit":        p.ProcessMemory,
		"upload_max_filesize": "64M",
		"post_max_size":       "64M",
		"max_execution_time":  "30",
		"max_input_time":      "60",
		"max_input_vars":      "1000",
		"allow_url_fopen":     "Off",
		"display_errors":      "Off",
		"disable_functions":   "exec,passthru,shell_exec,system,proc_open,popen",
	}
	// Isolated session + upload scratch INSIDE the site tree so they stay
	// reachable under open_basedir (the dbadmin pool opts out via "-").
	if p.WebsiteID != dbadminPoolSiteID {
		vals["session.save_path"] = "/srv/epicpanel/websites/" + p.WebsiteID + "/tmp/sessions"
		vals["upload_tmp_dir"] = "/srv/epicpanel/websites/" + p.WebsiteID + "/tmp/uploads"
	}
	for k, v := range p.PHPSettings {
		vals[k] = v
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := vals[k]
		if v == "" {
			continue
		}
		if phpFlagSettings[k] {
			fmt.Fprintf(&b, "php_admin_flag[%s] = %s\n", k, v)
		} else {
			fmt.Fprintf(&b, "php_admin_value[%s] = %s\n", k, v)
		}
	}
	return b.String()
}

func defaults(p *PoolSpec) {
	if p.PMMaxChildren <= 0 {
		p.PMMaxChildren = 10
	}
	if p.PMStartServers <= 0 {
		p.PMStartServers = 2
	}
	if p.PMMinSpare <= 0 {
		p.PMMinSpare = 1
	}
	if p.PMMaxSpare <= 0 {
		p.PMMaxSpare = 3
	}
	if p.ProcessMemory == "" {
		p.ProcessMemory = "128M"
	}
}

// EnsurePool writes the pool config atomically (tmp+rename), validates the
// whole fpm config with -t, then reloads the service. If validation fails the
// old config is restored so one bad pool can never take down other sites.
func (e *Executor) EnsurePool(ctx context.Context, p PoolSpec) error {
	// /run is tmpfs: the socket dir vanishes on every boot and FPM fails to
	// start ("unable to bind listening socket"). Ensure it exists BEFORE any
	// pool write/reload, and persist it via tmpfiles.d for early boot.
	if err := os.MkdirAll(fpmSocketDir, 0o755); err != nil {
		return fmt.Errorf("create fpm socket dir: %w", err)
	}
	_ = AtomicWriteFile("/etc/tmpfiles.d/epicpanel-fpm.conf", []byte("d /run/epicpanel/php-fpm 0755 root root -\n"), 0o644)
	_ = e.run(ctx, "systemd-tmpfiles", "--create", "/etc/tmpfiles.d/epicpanel-fpm.conf")
	defaults(&p)
	if err := e.validatePoolSpec(p); err != nil {
		return err
	}

	major := p.RuntimeVer
	poolDir := phpFpmPoolDir(major)
	poolFile := filepath.Join(poolDir, "epicpanel-"+p.WebsiteID+".conf")

	if err := os.MkdirAll(fpmSocketDir, 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		return fmt.Errorf("create pool dir %s: %w", poolDir, err)
	}
	// logs dir must exist for php_admin_value[error_log]
	logsDir := filepath.Join("/srv/epicpanel/websites", p.WebsiteID, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}
	// Per-site session + upload scratch must exist before FPM starts, or the
	// pool fails to bind them under open_basedir.
	if p.WebsiteID != dbadminPoolSiteID {
		tmpBase := filepath.Join("/srv/epicpanel/websites", p.WebsiteID, "tmp")
		for _, d := range []string{"sessions", "uploads"} {
			dir := filepath.Join(tmpBase, d)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("create %s dir: %w", d, err)
			}
			if uid, gid, err := siteOwnerIDs(p.WebsiteID); err == nil {
				_ = chownRecursive(dir, uid, gid)
			}
		}
	}

	content := RenderPoolConfig(p)

	// Idempotency: identical content -> nothing to do (but still ensure service up).
	if existing, err := os.ReadFile(poolFile); err == nil && string(existing) == content {
		return e.reloadFPM(ctx, major)
	}

	// Atomic write.
	tmp := poolFile + ".tmp"
	if err := AtomicWriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write pool tmp: %w", err)
	}

	// Validate BEFORE swapping in.
	if err := e.validateFPMConfig(ctx, major, tmp, poolFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, poolFile); err != nil {
		return fmt.Errorf("swap pool config: %w", err)
	}

	return e.reloadFPM(ctx, major)
}

// CleanupOtherVersionPools removes the site pool from every PHP version dir
// other than the desired one and reloads those services best-effort. Without
// this, a version switch leaves stale workers bound to the site socket and
// the new master cannot start ("Another FPM instance seems to already listen
// on ...").
func (e *Executor) CleanupOtherVersionPools(ctx context.Context, desiredVersion, websiteID string) error {
	matches, err := filepath.Glob(phpEtcBase + "/*/fpm/pool.d/epicpanel-" + websiteID + ".conf")
	if err != nil {
		return err
	}
	desiredFile := filepath.Join(phpFpmPoolDir(desiredVersion), "epicpanel-"+websiteID+".conf")
	for _, m := range matches {
		if m == desiredFile {
			continue
		}
		if err := os.Remove(m); err != nil {
			return fmt.Errorf("remove stale pool %s: %w", m, err)
		}
		if v := versionFromPoolPath(m); v != "" {
			_ = e.run(ctx, "systemctl", "reload", phpFpmService(v))
			slog.Info("stale pool removed", "file", m, "version", v)
		}
	}
	return nil
}

// versionFromPoolPath extracts the PHP version from /etc/php/<v>/fpm/... paths.
func versionFromPoolPath(path string) string {
	parts := strings.Split(path, string(os.PathSeparator))
	if len(parts) >= 4 && parts[1] == "etc" && parts[2] == "php" {
		return parts[3]
	}
	return ""
}

// RemovePool deletes the pool config for a website (idempotent) and reloads.
func (e *Executor) RemovePool(ctx context.Context, runtimeVersion, websiteID string) error {
	if runtimeVersion == "" {
		return nil
	}
	poolFile := filepath.Join(phpFpmPoolDir(runtimeVersion), "epicpanel-"+websiteID+".conf")
	if _, err := os.Stat(poolFile); os.IsNotExist(err) {
		return nil
	}
	if err := os.Remove(poolFile); err != nil {
		return fmt.Errorf("remove pool config: %w", err)
	}
	return e.reloadFPM(ctx, runtimeVersion)
}

// validateFPMConfig runs php-fpm -t against the real config tree with the new
// pool file temporarily placed at its final path. On failure the previous file
// (if any) is restored by the caller via tmp not being renamed. The binary
// runs under a hard 120s timeout (ValidateCmd) so a hung php-fpm can never
// wedge the provision job.
func (e *Executor) validateFPMConfig(ctx context.Context, major, tmpPath, finalPath string) error {
	// php-fpm -t validates the loaded ini tree; simplest correct approach is
	// validating the final tree with the new file swapped in, then restoring
	// on failure.
	backup, hadBackup := "", false
	if b, err := os.ReadFile(finalPath); err == nil {
		backup = string(b)
		hadBackup = true
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("stage pool config for validation: %w", err)
	}

	bin := phpFpmBinary(major)
	if _, err := exec.LookPath(bin); err != nil {
		alt := "php-fpm" + strings.ReplaceAll(major, ".", "")
		if _, err2 := exec.LookPath(alt); err2 != nil {
			// Cannot validate (binary missing) — restore and fail.
			e.restorePool(finalPath, backup, hadBackup)
			return fmt.Errorf("php-fpm binary not found for validation")
		}
		bin = alt
	}
	if err := ValidateCmd(ctx, 120*time.Second, bin, "--fpm-config", fpmMainConfig(major), "--test"); err != nil {
		e.restorePool(finalPath, backup, hadBackup)
		return fmt.Errorf("fpm config validation failed: %w", err)
	}
	return nil
}

func (e *Executor) restorePool(finalPath, backup string, hadBackup bool) {
	if hadBackup {
		_ = os.WriteFile(finalPath, []byte(backup), 0o644)
	} else {
		_ = os.Remove(finalPath)
	}
}

func fpmMainConfig(major string) string {
	return phpEtcBase + "/" + major + "/fpm/php-fpm.conf"
}

// reloadFPM reloads (not restarts) the service — reload applies pool changes
// without dropping active connections. Service is started first if inactive.
func (e *Executor) reloadFPM(ctx context.Context, major string) error {
	svc := phpFpmService(major)
	if err := e.run(ctx, "systemctl", "enable", svc); err != nil {
		return fmt.Errorf("enable %s: %w", svc, err)
	}
	// start-if-stopped; ignore error if already running
	_ = e.run(ctx, "systemctl", "start", svc)
	if err := e.run(ctx, "systemctl", "reload", svc); err != nil {
		// Some minimal systems lack systemd; try direct reload signal via binary.
		return fmt.Errorf("reload %s: %w", svc, err)
	}
	slog.Info("php-fpm reloaded", "version", major)
	return nil
}

func (e *Executor) validatePoolSpec(p PoolSpec) error {
	if p.WebsiteID != dbadminPoolSiteID {
		if _, err := uuid.Parse(p.WebsiteID); err != nil {
			return fmt.Errorf("invalid website id: %w", err)
		}
	}
	if !validUnixUserName(p.UnixUser) {
		return fmt.Errorf("invalid unix user %q", p.UnixUser)
	}
	if !versionReMatch(p.RuntimeVer) {
		return fmt.Errorf("invalid runtime version %q", p.RuntimeVer)
	}
	return nil
}

func versionReMatch(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
