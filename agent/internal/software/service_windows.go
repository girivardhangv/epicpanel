//go:build windows

package software

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scCache caches Windows service existence checks so serviceSpec doesn't
// hammer sc query on every List() call.
var scCacheMu sync.RWMutex
var scCache = map[string]scCacheEntry{}

type scCacheEntry struct {
	exists   bool
	expiresAt time.Time
}

func scServiceExists(name string) bool {
	scCacheMu.RLock()
	e, ok := scCache[name]
	scCacheMu.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		return e.exists
	}
	// Re-check.
	res, err := Run(context.Background(), "sc", "query", name)
	exists := err == nil && res.ExitCode == 0
	scCacheMu.Lock()
	scCache[name] = scCacheEntry{exists, time.Now().Add(60 * time.Second)}
	scCacheMu.Unlock()
	return exists
}

// serviceActive reports whether a component's service is running. Windows
// services are queried via sc; the nginx process we supervise is checked via
// its pid file.
func (m *Manager) serviceActive(ctx context.Context, p Provider) bool {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return false
	}
	switch spec.mode {
	case ServiceNone:
		return false
	case ServiceProcess:
		_, err := os.Stat(spec.pidFile())
		return err == nil
	case ServiceWindowsService:
		res, err := Run(ctx, "sc", "query", spec.unit)
		if err != nil || !res.OK() {
			return false
		}
		return strings.Contains(res.Stdout, "RUNNING") || strings.Contains(res.Stdout, "STATE")
	default:
		return false
	}
}

func (m *Manager) serviceStart(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	switch spec.mode {
	case ServiceNone:
		return nil
	case ServiceProcess:
		return startProcess(spec)
	case ServiceWindowsService:
		res, err := Run(ctx, "net", "start", spec.unit)
		if err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("start failed: %s", firstLine(res.Stderr))
		}
		return nil
	default:
		return fmt.Errorf("unsupported service mode %q", spec.mode)
	}
}

func (m *Manager) serviceStop(ctx context.Context, p Provider) (CommandResult, error) {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return CommandResult{}, err
	}
	switch spec.mode {
	case ServiceNone:
		return CommandResult{ExitCode: 0}, nil
	case ServiceProcess:
		if err := stopProcess(spec); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{ExitCode: 0}, nil
	case ServiceWindowsService:
		res, err := Run(ctx, "net", "stop", spec.unit)
		if err != nil {
			return res, err
		}
		if !res.OK() {
			return res, fmt.Errorf("stop failed: %s", firstLine(res.Stderr))
		}
		return res, nil
	default:
		return CommandResult{}, fmt.Errorf("unsupported service mode %q", spec.mode)
	}
}

func (m *Manager) serviceRestart(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	switch spec.mode {
	case ServiceNone:
		return nil
	case ServiceProcess:
		if err := stopProcess(spec); err != nil {
			return err
		}
		return startProcess(spec)
	case ServiceWindowsService:
		sres, _ := Run(ctx, "net", "stop", spec.unit)
		if !sres.OK() {
			// Service may already be stopped; that's fine.
		}
		time.Sleep(500 * time.Millisecond)
		res, err := Run(ctx, "net", "start", spec.unit)
		if err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("restart failed: %s", firstLine(res.Stderr))
		}
		return nil
	default:
		return fmt.Errorf("unsupported service mode %q", spec.mode)
	}
}

func (m *Manager) serviceReload(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode != ServiceProcess {
		// Windows services have no reload; restart is the closest semantic.
		return m.serviceRestart(ctx, p)
	}
	_, err = RunPath(ctx, spec.bin, "-s", "reload", "-p", filepath.Dir(spec.bin))
	return err
}

func (m *Manager) serviceEnable(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode != ServiceWindowsService {
		return nil
	}
	_, err = Run(ctx, "sc", "config", spec.unit, "start=", "auto")
	return err
}

func (m *Manager) serviceDisable(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode != ServiceWindowsService {
		return nil
	}
	_, err = Run(ctx, "sc", "config", spec.unit, "start=", "demand")
	return err
}

// ---------------------------------------------------------------------------
// Supervised process helpers (Windows process-based services)
// ---------------------------------------------------------------------------

// ensureManagedUnit is a no-op on Windows — self-contained components are
// supervised as processes, not systemd units.
func (m *Manager) ensureManagedUnit(ctx context.Context, p Provider, bin string) error {
	return nil
}

// pidFile returns the pid file path for a supervised process.
func (s serviceSpec) pidFile() string {
	return filepath.Join(s.pidDir, "epicpanel-"+filepath.Base(s.bin)+".pid")
}

// startProcess launches a supervised binary detached and records its pid.
func startProcess(spec serviceSpec) error {
	if spec.bin == "" {
		return fmt.Errorf("no binary configured for supervised service")
	}
	// If the pid file exists, assume it is already running (best-effort).
	if _, err := os.Stat(spec.pidFile()); err == nil {
		return nil
	}
	args := spec.args
	// nginx runs relative to its own directory; pass an explicit prefix so
	// the layout is deterministic regardless of the caller's cwd.
	if strings.Contains(strings.ToLower(spec.bin), "nginx") {
		args = append([]string{"-p", filepath.Dir(spec.bin)}, args...)
	}
	// MariaDB needs an initialized datadir; create it on first start.
	if strings.Contains(strings.ToLower(spec.bin), "mariadbd") {
		if err := ensureMariaDBDataDir(spec); err != nil {
			return err
		}
		// No args by default; mariadbd reads its datadir from my.ini that
		// mariadb-install-db writes next to the binary.
	}
	cmd := exec.Command(spec.bin, args...)
	cmd.Dir = filepath.Dir(spec.bin)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", spec.bin, err)
	}
	_ = os.MkdirAll(spec.pidDir, 0o755)
	_ = os.WriteFile(spec.pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	_ = cmd.Process.Release()
	return nil
}

// ensureMariaDBDataDir initializes the MariaDB datadir on first start. The
// data dir lives at <install>/data (the pidDir of the mariadb spec). The
// install command writes my.ini next to mariadbd.exe so subsequent starts
// pick up the datadir automatically.
func ensureMariaDBDataDir(spec serviceSpec) error {
	binDir := filepath.Dir(spec.bin)
	if _, err := os.Stat(filepath.Join(spec.pidDir, "mysql")); err == nil {
		return nil // already initialized
	}
	init := filepath.Join(binDir, "mariadb-install-db.exe")
	if _, err := os.Stat(init); err != nil {
		return fmt.Errorf("mariadb-install-db.exe not found next to %s", spec.bin)
	}
	_ = os.MkdirAll(spec.pidDir, 0o755)
	res, err := RunPath(context.Background(), init, "--datadir="+spec.pidDir)
	if err != nil {
		return fmt.Errorf("initialize mariadb datadir: %w", err)
	}
	if !res.OK() {
		return fmt.Errorf("initialize mariadb datadir failed: %s", firstLine(res.Stderr))
	}
	return nil
}

// stopProcess terminates a supervised process via its pid file, escalating
// from graceful (nginx -s quit) to a hard taskkill.
func stopProcess(spec serviceSpec) error {
	pid := readPID(spec.pidFile())
	if pid <= 0 {
		_ = os.Remove(spec.pidFile())
		return nil // not running
	}
	// nginx gets a graceful stop via its own signal command first.
	if strings.Contains(strings.ToLower(spec.bin), "nginx") {
		_, _ = RunPath(context.Background(), spec.bin, "-s", "quit", "-p", filepath.Dir(spec.bin))
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if !pidAlive(pid) {
				_ = os.Remove(spec.pidFile())
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	// Hard kill: taskkill is authoritative on Windows and works even when
	// the process handle is shared across processes.
	_, _ = Run(context.Background(), "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = os.Remove(spec.pidFile())
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// readPID returns the pid stored in a pid file, or 0.
func readPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// pidAlive reports whether a process with the given pid is running.
func pidAlive(pid int) bool {
	res, err := Run(context.Background(), "taskkill", "/PID", strconv.Itoa(pid))
	if err == nil && res.ExitCode == 0 {
		return true
	}
	return false
}