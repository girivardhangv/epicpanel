package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/google/uuid"
)

// QuotaOutcome reports disk usage + applied limits for the site.
type QuotaOutcome struct {
	WebsiteID     string  `json:"website_id"`
	MaxDiskMB     int     `json:"max_disk_mb"`
	UsedMB        int64   `json:"used_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	CPUCores      float64 `json:"cpu_cores"`
}

// ApplyQuota enforces the org's package limits for one site:
//   - FPM pool: rewrite memory_limit in the site's pool file (strict,
//     per-process cap — the package's memory limit "no matter what");
//     pm.max_children is derived automatically from the limit
//   - cgroup v2: the site's unix user is placed into a dedicated slice with
//     cpu.max + memory.max, so CPU (e.g. 0.5 core) and memory are hard
//     limits enforced by the kernel. A timer re-captures respawned workers.
//   - disk usage: measured and reported back (soft accounting; enforced
//     control-plane side)
func (e *Executor) ApplyQuota(ctx context.Context, p QuotaJobPayload) (*QuotaOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}

	out := &QuotaOutcome{WebsiteID: p.WebsiteID, MaxDiskMB: p.MaxDiskMB, MemoryLimitMB: p.MemoryLimitMB, CPUCores: p.CPUCores}
	siteBase := filepath.Join(e.docRootBase, p.WebsiteID)
	out.UsedMB = dirSizeMB(siteBase)

	// PHP memory limit: strictly cap each request at the plan limit.
	// Phase 9: pool sizing comes from the unified engine (resources.
	// PoolLimitsFor) — the single derivation shared with the control plane's
	// display path, so the pool bound and the plan RAM cannot drift.
	memLimit := p.MemoryLimitMB
	if memLimit <= 0 {
		memLimit = 128
	}
	pool := resources.PoolLimitsFor(int64(memLimit))
	children := pool.MaxChildren
	if children <= 0 {
		children = 2
	}
	if pool.MemoryLimitMB > 0 {
		memLimit = pool.MemoryLimitMB
	}

	// Patch every epicpanel pool file for this site across php versions.
	matches, _ := filepath.Glob(phpEtcBase + "/*/fpm/pool.d/epicpanel-" + p.WebsiteID + ".conf")
	for _, poolFile := range matches {
		v := versionFromPoolPath(poolFile)
		fpmBin := ""
		mainCfg := ""
		if v != "" {
			if bin, lookErr := exec.LookPath(phpFpmBinary(v)); lookErr == nil {
				fpmBin = bin
				mainCfg = phpEtcBase + "/" + v + "/fpm/php-fpm.conf"
			}
		}
		if err := patchPoolLimits(ctx, poolFile, fmt.Sprintf("%dM", memLimit), children, fpmBin, mainCfg); err != nil {
			return nil, fmt.Errorf("patch %s: %w", poolFile, err)
		}
		if v != "" {
			_ = e.run(ctx, "systemctl", "reload", phpFpmService(v))
		}
		slog.Info("pool limits applied", "site", p.WebsiteID, "memory_limit_mb", memLimit, "children", children)
	}

	// Kernel-enforced CPU + memory for everything this site's unix user runs.
	// Phase 9: the plan RAM is now the hard aggregate cap (no 4× headroom —
	// PoolLimitsFor bounds the pool aggregate to the plan RAM), so what the
	// panel displays as the memory cap IS memory.max.
	memBytes := resources.CgroupMemoryBytes(int64(p.MemoryLimitMB))
	quotaUsec, periodUsec, _ := resources.CgroupCPUMax(p.CPUCores * 100)
	if err := e.ApplyUserLimitsV2(ctx, siteUnixUser(p.WebsiteID), siteSlicePath(siteUnixUser(p.WebsiteID)),
		memBytes, quotaUsec, periodUsec, 0, 0); err != nil {
		slog.Warn("cgroup limits failed (continuing with php-level caps)", "site", p.WebsiteID, "err", err)
	}
	return out, nil
}

// patchPoolLimits rewrites fpm memory_limit + max_children lines atomically.
func patchPoolLimits(ctx context.Context, poolFile, memory string, children int, fpmBin, mainConfig string) error {
	b, err := os.ReadFile(poolFile)
	if err != nil {
		return err
	}
	if children < 2 {
		children = 2
	}
	backup := string(b)
	startServers := children / 2
	if startServers < 1 {
		startServers = 1
	}
	minSpare := startServers
	maxSpare := children - 1
	if maxSpare < minSpare {
		maxSpare = minSpare
	}

	var out []string
	for _, line := range strings.Split(backup, "\n") {
		switch {
		case strings.HasPrefix(line, "php_admin_value[memory_limit]"):
			out = append(out, "php_admin_value[memory_limit] = "+memory)
		case strings.HasPrefix(line, "pm.max_children"):
			out = append(out, fmt.Sprintf("pm.max_children = %d", children))
		case strings.HasPrefix(line, "pm.start_servers"):
			out = append(out, fmt.Sprintf("pm.start_servers = %d", startServers))
		case strings.HasPrefix(line, "pm.min_spare_servers"):
			out = append(out, fmt.Sprintf("pm.min_spare_servers = %d", minSpare))
		case strings.HasPrefix(line, "pm.max_spare_servers"):
			out = append(out, fmt.Sprintf("pm.max_spare_servers = %d", maxSpare))
		default:
			out = append(out, line)
		}
	}
	tmp := poolFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, poolFile); err != nil {
		return err
	}
	if fpmBin != "" {
		if out2, verr := exec.CommandContext(ctx, fpmBin, "--fpm-config", mainConfig, "--test").CombinedOutput(); verr != nil {
			_ = os.WriteFile(poolFile, []byte(backup), 0o644)
			return fmt.Errorf("fpm validation failed (config restored): %s", tail(out2, 200))
		}
	}
	return nil
}

// dirSizeMB measures a tree in megabytes (disk accounting).
func dirSizeMB(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}

// EnsurePrivateTmp hardens a site's tmp usage: creates a site-owned tmp dir
// and (when systemd-run is available) it is used by the pool via open_basedir.
// We ensure the dir exists here; FPM pools already include it via open_basedir.
func (e *Executor) EnsurePrivateTmp(ctx context.Context, websiteID string) error {
	if _, err := uuid.Parse(websiteID); err != nil {
		return fmt.Errorf("invalid website id")
	}
	dir := filepath.Join(e.docRootBase, websiteID, "tmp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if uid, gid, err := siteOwnerIDs(websiteID); err == nil {
		_ = os.Chown(dir, uid, gid)
	}
	return nil
}

// ============================================================================
// CGROUP V2 SITE LIMITS — kernel-enforced CPU + memory per site user.
// The site's unix user owns all of its FPM workers (per-site pools) plus any
// cron/shell processes, so constraining the user's cgroup constrains the
// whole site. A systemd timer re-runs the sync script to capture respawned
// workers (php-fpm spawns new children outside the slice).
// ============================================================================

// cgroupRoot is the unified hierarchy mount; overridable in tests (Phase 9
// enforcement reads/writes it through this single handle — one source of
// truth for both the collector and the enforcement arm).
var cgroupRoot = "/sys/fs/cgroup"

var limitsStateFile = "/etc/epicpanel/limits.json"

const limitsSyncScript = "/usr/local/bin/epicpanel-cgroup-sync"
const limitsSyncTimer = "epicpanel-cgroup-sync.timer"

// ApplyUserLimits applies the plan's CPU + memory caps to the site's unix
// user slice (Phase 9: exact plan RAM via resources.CgroupMemoryBytes — the
// displayed cap and the enforced cap are derived from the same number; the
// old 4× FPM headroom is gone because PoolLimitsFor now sizes children so
// the pool aggregate fits the plan RAM).
func (e *Executor) ApplyUserLimits(ctx context.Context, websiteID string, memLimitMB float64, cpuCores float64) error {
	user := siteUnixUser(websiteID)
	slice := siteSlicePath(user)
	if slice == "" {
		return fmt.Errorf("unix user not found for site")
	}
	var quota, period int64
	if cpuCores > 0 {
		quota, period, _ = resources.CgroupCPUMax(cpuCores * 100)
	}
	return e.ApplyUserLimitsV2(ctx, user, slice, resources.CgroupMemoryBytes(int64(memLimitMB)), quota, period, 0, 0)
}

func enableControllers(dir string) error {
	b, err := os.ReadFile(dir + "/cgroup.controllers")
	if err != nil {
		return err
	}
	var out strings.Builder
	for _, c := range strings.Fields(string(b)) {
		out.WriteString("+")
		out.WriteString(c)
		out.WriteString(" ")
	}
	return os.WriteFile(dir+"/cgroup.subtree_control", []byte(strings.TrimSpace(out.String())), 0o644)
}

// moveUserProcesses moves every process of the unix user into the slice.
// Uses the injectable psCommand (overridden in tests).
func moveUserProcesses(user, siteSlice string) {
	out, err := psCommand("ps", "-u", user, "-o", "pid=").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		pid := strings.TrimSpace(line)
		if pid == "" || pid == "1" {
			continue
		}
		_ = os.WriteFile(siteSlice+"/cgroup.procs", []byte(pid), 0o644)
	}
}

func siteUnixUser(websiteID string) string {
	matches, _ := filepath.Glob(phpEtcBase + "/*/fpm/pool.d/epicpanel-" + websiteID + ".conf")
	for _, f := range matches {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "user =") {
				return strings.TrimSpace(strings.TrimPrefix(line, "user ="))
			}
		}
	}
	return ""
}

// psCommand builds the process listing command (injectable in tests).
var psCommand = exec.Command

// uidOf returns the uid of a unix user (injectable lookup in tests).
var uidOfFn = uidOf

func uidOf(user string) (string, error) {
	out, err := exec.Command("id", "-u", user).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// upsertLimitState records limits for the sync script (JSON list).
func upsertLimitState(user string, quotaPer100ms int, memBytes int64) error {
	_ = os.MkdirAll(filepath.Dir(limitsStateFile), 0o700)
	var state map[string]map[string]any
	if b, err := os.ReadFile(limitsStateFile); err == nil && json.Unmarshal(b, &state) == nil && state != nil {
		// keep
	} else {
		state = map[string]map[string]any{}
	}
	state[user] = map[string]any{"cpu_quota": quotaPer100ms, "memory_bytes": memBytes}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(limitsStateFile, b, 0o600)
}

// ensureLimitsSync installs the script + timer that re-captures respawned
// FPM workers into their slices every minute.
var ensureLimitsSyncFn = ensureLimitsSync

func ensureLimitsSync(ctx context.Context) error {
	script := `#!/bin/bash
# Managed by EpicPanel — re-captures site processes into their cgroup slices.
STATE=/etc/epicpanel/limits.json
[ -f "$STATE" ] || exit 0
for U in $(python3 -c "import json,sys;print(' '.join(json.load(open('$STATE')).keys()))" 2>/dev/null); do
  SLICE=/sys/fs/cgroup/epicpanel.slice/epicpanel-$U.slice
  [ -d "$SLICE" ] || continue
  for PID in $(ps -u "$U" -o pid= 2>/dev/null); do
    CG=$(cat /proc/$PID/cgroup 2>/dev/null | head -1)
    case "$CG" in *epicpanel-$U.slice*) continue ;; esac
    echo $PID > $SLICE/cgroup.procs 2>/dev/null || true
  done
done
`
	if err := os.WriteFile(limitsSyncScript, []byte(script), 0o755); err != nil {
		return err
	}
	_ = os.WriteFile("/etc/systemd/system/"+limitsSyncTimer[:len(limitsSyncTimer)-6]+".service", []byte(`[Unit]
Description=EpicPanel cgroup limits sync

[Service]
Type=oneshot
ExecStart=`+limitsSyncScript+`
`), 0o644)
	_ = os.WriteFile("/etc/systemd/system/"+limitsSyncTimer, []byte(`[Unit]
Description=EpicPanel cgroup limits sync timer

[Timer]
OnBootSec=60
OnUnitActiveSec=60

[Install]
WantedBy=timers.target
`), 0o644)
	_ = e_run(context.Background(), "systemctl", "daemon-reload")
	_ = e_run(context.Background(), "systemctl", "enable", "--now", limitsSyncTimer)
	return nil
}

func e_run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
