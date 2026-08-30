package software

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Component is the API view of one managed piece of software.
type Component struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	Installed   bool   `json:"installed"`
	Managed     bool   `json:"managed"`   // true when EpicPanel owns the copy (self-contained)
	Location    string `json:"location"`  // filesystem location of the binary
	Version     string `json:"version,omitempty"`
	Service     string `json:"service,omitempty"`
	Running     bool   `json:"running"`
	Supported   bool   `json:"supported"` // installable on this host
	Source      string `json:"source"`    // "download" | "package-manager"
}

// Manager runs software operations for the detected OS. All self-contained
// installs live under dir (EpicPanel's own software root), so the panel never
// depends on or conflicts with software the host already has.
type Manager struct {
	os  OSInfo
	reg *Registry
	log *slog.Logger
	dir string
}

// NewManager returns a manager with the platform default software root.
func NewManager(log *slog.Logger) *Manager {
	return NewManagerDir(log, DefaultSoftwareDir())
}

// NewManagerDir returns a manager rooted at an explicit software directory.
func NewManagerDir(log *slog.Logger, dir string) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if dir == "" {
		dir = DefaultSoftwareDir()
	}
	return &Manager{os: Detect(), reg: Default(), log: log, dir: filepath.Clean(dir)}
}

// DefaultSoftwareDir returns the platform default software root:
// /opt/epicpanel/software on Linux, C:\ProgramData\EpicPanel\software on
// Windows. Overridable via EPICPANEL_SOFTWARE_DIR or the -software-dir flag.
func DefaultSoftwareDir() string {
	if v := os.Getenv("EPICPANEL_SOFTWARE_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\EpicPanel\software`
	}
	return "/opt/epicpanel/software"
}

// OS returns the detected platform info.
func (m *Manager) OS() OSInfo { return m.os }

// Dir returns the software root this manager installs into.
func (m *Manager) Dir() string { return m.dir }

// compDir returns <dir>/<name>, the self-contained home for a component.
func (m *Manager) compDir(name string) string {
	return filepath.Join(m.dir, name)
}

// hasResolver reports whether the provider ships a self-contained download
// for the current platform (resolved dynamically at install time).
func (m *Manager) hasResolver(p Provider) bool {
	return p.Resolve != nil
}

// resolveRelease resolves the current latest self-contained build for a
// component. ErrNotApplicable (or a nil Release) means the component has no
// self-contained build on this platform and the package manager is used.
func (m *Manager) resolveRelease(ctx context.Context, p Provider) (*Release, error) {
	if p.Resolve == nil {
		return nil, ErrNotApplicable
	}
	return p.Resolve(ctx, m.os)
}

// binPath resolves the primary binary for a self-contained component.
func (m *Manager) binPath(name string, p Provider) string {
	return filepath.Join(m.compDir(name), binName(m.os, p.Binary))
}

// resolveBinPath returns the actual binary path inside a component dir,
// falling back to a tree search for archives with a versioned top-level dir
// (e.g. JRE builds where the binary sits under <jdk>/bin/).
func (m *Manager) resolveBinPath(name string, p Provider) (string, bool) {
	compDir := m.compDir(name)
	direct := filepath.Join(compDir, binName(m.os, p.Binary))
	if _, err := os.Stat(direct); err == nil {
		return direct, true
	}
	if rel, err := findBinary(compDir, binName(m.os, p.Binary)); err == nil {
		return filepath.Join(compDir, rel), true
	}
	return direct, false
}

// findBinary walks root and returns the relative path of the first file whose
// base name matches bin. It is bounded to a modest depth and stops at the
// first match.
func findBinary(root, bin string) (string, error) {
	if root == "" || bin == "" {
		return "", fmt.Errorf("empty search")
	}
	var found string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if found != "" || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), bin) {
			rel, rerr := filepath.Rel(root, path)
			if rerr == nil {
				found = filepath.ToSlash(rel)
			}
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if found == "" {
		return "", fmt.Errorf("binary %s not found", bin)
	}
	return found, nil
}

// binName resolves the platform binary filename (Windows adds .exe).
func binName(os OSInfo, bin string) string {
	if os.Family == "windows" && !strings.HasSuffix(strings.ToLower(bin), ".exe") {
		return bin + ".exe"
	}
	return bin
}

// List detects every registered component. Detection is live (no stale
// cache): a component is Installed when its binary exists in our software dir
// (Managed=true) or on PATH (system copy, Managed=false).
func (m *Manager) List(ctx context.Context) []Component {
	out := []Component{}
	for _, name := range m.reg.Names() {
		p, _ := m.reg.Get(name)
		c := Component{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Category:    p.Category,
			Service:     p.Service,
		}
		if m.hasResolver(p) {
			c.Supported = true
			c.Source = "download"
		} else if len(p.Packages[m.os.PackageManager]) > 0 {
			c.Supported = true
			c.Source = "package-manager"
		}

		// 1) self-contained copy we own
		ownedBin, ok := m.resolveBinPath(name, p)
		if ok {
			c.Installed = true
			c.Managed = true
			c.Location = ownedBin
			c.Version = m.detectVersion(ctx, ownedBin, p)
			c.Running = m.serviceActive(ctx, p)
			// Clear the service name if the platform has no actual service
			// for this component (e.g. PHP on Windows has no Windows service).
			if c.Service != "" {
				if spec, serr := m.serviceSpec(p); serr == nil && spec.mode == ServiceNone {
					c.Service = ""
				}
			}
			out = append(out, c)
			continue
		}
		// 2) system copy on PATH (report honestly, we do not own it)
		if LookPath(p.Binary) {
			c.Installed = true
			c.Managed = false
			c.Version = m.detectVersion(ctx, p.Binary, p)
			c.Running = m.serviceActive(ctx, p)
		}
		out = append(out, c)
	}
	return out
}

func (m *Manager) detectVersion(ctx context.Context, bin string, p Provider) string {
	res, err := RunPath(ctx, bin, p.VersionArgs...)
	if err != nil || !res.OK() {
		return ""
	}
	src := res.Stdout
	if p.VersionFromStderr {
		src = res.Stderr
	}
	for _, line := range strings.Split(src, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return truncate(l, 80)
		}
	}
	return ""
}

// Install ensures a component is present. For components with a self-contained
// resolver it downloads the latest official prebuilt build into our own
// directory; otherwise it falls back to the host package manager. It then
// starts the component's service where applicable.
func (m *Manager) Install(ctx context.Context, name string) (CommandResult, error) {
	p, ok := m.reg.Get(name)
	if !ok {
		return CommandResult{}, fmt.Errorf("unknown component %q", name)
	}

	installed := false
	if m.hasResolver(p) {
		rel, rerr := m.resolveRelease(ctx, p)
		if rerr == nil && rel != nil {
			m.log.Info("installing self-contained component", "component", name, "dir", m.compDir(name), "version", rel.Version)
			if p.Build != nil && m.os.Family != "windows" {
				// Linux: no official prebuilt binary — compile from source.
				m.log.Info("compiling from source", "component", name, "version", rel.Version)
				if err := m.compileFromSource(ctx, p, *rel); err != nil {
					return CommandResult{}, err
				}
			} else if err := m.installRelease(ctx, p, *rel); err != nil {
				return CommandResult{}, err
			}
			installed = true
		} else if rerr != nil && !errors.Is(rerr, ErrNotApplicable) {
			return CommandResult{}, fmt.Errorf("resolve %s: %w", name, rerr)
		}
	}
	if !installed {
		argv, ok := p.Packages[m.os.PackageManager]
		if !ok || len(argv) == 0 {
			return CommandResult{}, fmt.Errorf("%s is not installable on this host", name)
		}
		m.log.Info("installing via package manager", "component", name, "manager", m.os.PackageManager)
		res, err := Run(ctx, argv[0], argv[1:]...)
		if err != nil {
			return res, err
		}
		if !res.OK() {
			detail := strings.TrimSpace(res.Stderr)
			if detail == "" {
				detail = strings.TrimSpace(res.Stdout)
			}
			return res, fmt.Errorf("install failed: %s", lastLines(detail, 3))
		}
	}

	// For self-contained components on Linux, register a dedicated systemd
	// unit so the panel controls our copy (never the distro's).
	if installed && p.Service != "" && m.os.Family != "windows" {
		if bin, ok := m.resolveBinPath(name, p); ok {
			if err := m.ensureManagedUnit(ctx, p, bin); err != nil {
				m.log.Warn("could not register systemd unit", "component", name, "err", err)
			}
		}
	}

	// Best-effort service start; failures are surfaced but not fatal to install.
	if p.Service != "" {
		if err := m.serviceStart(ctx, p); err != nil {
			m.log.Warn("component installed but service did not start", "component", name, "err", err)
		}
	}
	return CommandResult{ExitCode: 0}, nil
}

// installRelease downloads, verifies and extracts the resolved build into our
// software dir. Extraction is atomic-ish: we extract to a temp dir and rename
// into place so a failed download never leaves a half-installed tree.
func (m *Manager) installRelease(ctx context.Context, p Provider, rel Release) error {
	compDir := m.compDir(p.Name)
	if rel.URL == "" {
		return fmt.Errorf("%s has no download source configured", p.Name)
	}
	archivePath, err := download(rel.URL, rel.SHA256)
	if err != nil {
		return fmt.Errorf("download %s: %w", p.Name, err)
	}
	defer os.Remove(archivePath)

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.dir, ".tmp-"+p.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := extractTo(archivePath, rel.Format, rel.Prefix, tmp); err != nil {
		return fmt.Errorf("extract %s: %w", p.Name, err)
	}

	// The binary must actually be present after extraction.
	binRel := binName(m.os, p.Binary)
	if rel.Bin != "" {
		binRel = binName(m.os, rel.Bin)
	}
	if _, err := os.Stat(filepath.Join(tmp, binRel)); err != nil {
		// Some archives (e.g. JRE builds) have a versioned top-level dir, so
		// search the extracted tree for the binary before declaring failure.
		found, ferr := findBinary(tmp, binName(m.os, p.Binary))
		if ferr != nil {
			return fmt.Errorf("%s archive did not contain %s (extracted tree is invalid)", p.Name, binRel)
		}
		binRel = found
	}

	// Swap into place: remove a stale tree, then rename the fresh one.
	_ = os.RemoveAll(compDir)
	if err := os.Rename(tmp, compDir); err != nil {
		return err
	}
	return nil
}

// Remove uninstalls a component. Self-contained components are removed by
// deleting our own directory tree; package-manager components by transforming
// their install command into a remove command.
func (m *Manager) Remove(ctx context.Context, name string) (CommandResult, error) {
	p, ok := m.reg.Get(name)
	if !ok {
		return CommandResult{}, fmt.Errorf("unknown component %q", name)
	}
	// Stop the service first so files are not in use.
	if p.Service != "" {
		_, _ = m.serviceStop(ctx, p)
	}

	// Check if we have a managed (self-contained) copy installed.
	if _, ok := m.resolveBinPath(name, p); ok {
		compDir := m.compDir(name)
		m.log.Info("removing self-contained component", "component", name, "dir", compDir)
		if err := os.RemoveAll(compDir); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{ExitCode: 0}, nil
	}

	argv, ok := p.Packages[m.os.PackageManager]
	if !ok || len(argv) == 0 {
		return CommandResult{}, fmt.Errorf("%s is not manageable via %s", name, m.os.PackageManager)
	}
	remove := removeArgs(m.os.PackageManager, argv)
	m.log.Info("removing via package manager", "component", name)
	return Run(ctx, remove[0], remove[1:]...)
}

// Service starts/stops/restarts/enables/disables a component's service.
func (m *Manager) Service(ctx context.Context, name, action string) (CommandResult, error) {
	p, ok := m.reg.Get(name)
	if !ok {
		return CommandResult{}, fmt.Errorf("unknown component %q", name)
	}
	if p.Service == "" {
		return CommandResult{}, fmt.Errorf("%s has no service to control", name)
	}
	switch action {
	case "start", "stop", "restart", "reload", "enable", "disable", "status":
	default:
		return CommandResult{}, fmt.Errorf("invalid service action %q", action)
	}

	var err error
	switch action {
	case "start":
		err = m.serviceStart(ctx, p)
	case "stop":
		_, err = m.serviceStop(ctx, p)
	case "restart":
		err = m.serviceRestart(ctx, p)
	case "reload":
		err = m.serviceReload(ctx, p)
	case "enable":
		err = m.serviceEnable(ctx, p)
	case "disable":
		err = m.serviceDisable(ctx, p)
	case "status":
		active := m.serviceActive(ctx, p)
		code := 1
		if active {
			code = 0
		}
		return CommandResult{ExitCode: code}, nil
	}
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{ExitCode: 0}, nil
}

// serviceSpec builds the control spec for a provider on this host.
func (m *Manager) serviceSpec(p Provider) (serviceSpec, error) {
	if p.Service == "" {
		return serviceSpec{}, fmt.Errorf("%s has no service", p.Name)
	}
	spec := serviceSpec{unit: p.Service}
	if m.os.Family == "windows" {
		// nginx on Windows runs as a supervised process we own (only when we
		// have a managed self-contained copy; a system nginx uses its service).
		if p.Name == "nginx" {
			if _, ok := m.resolveBinPath(p.Name, p); ok {
				spec.mode = ServiceProcess
				spec.bin = filepath.Join(m.compDir(p.Name), "nginx.exe")
				spec.pidDir = filepath.Join(m.compDir(p.Name), "logs")
				return spec, nil
			}
		}
		// MariaDB runs as a supervised process when we own the self-contained
		// copy (no Windows service is registered by our download).
		if p.Name == "mariadb" {
			if _, ok := m.resolveBinPath(p.Name, p); ok {
				spec.mode = ServiceProcess
				spec.bin = filepath.Join(m.compDir(p.Name), "bin", "mariadbd.exe")
				spec.pidDir = filepath.Join(m.compDir(p.Name), "data")
				return spec, nil
			}
		}
		// Everything else on Windows expects a registered service. If no such
		// service exists (e.g. PHP has no Windows service), the component has
		// no service to control.
		if p.Service == "" || !scServiceExists(p.Service) {
			spec.mode = ServiceNone
			return spec, nil
		}
		spec.mode = ServiceWindowsService
		return spec, nil
	}
	spec.mode = ServiceSystemd
	// Self-contained (compiled/downloaded) components run under their own
	// epicpanel-<name> unit, never the distro's unit.
	if _, ok := m.resolveBinPath(p.Name, p); ok {
		spec.unit = "epicpanel-" + p.Name
	}
	return spec, nil
}

// removeArgs converts an install argv into the equivalent remove argv.
func removeArgs(manager string, argv []string) []string {
	switch manager {
	case "apt":
		return replaceVerb(argv, "install", "remove")
	case "dnf", "yum":
		return replaceVerb(argv, "install", "remove")
	case "zypper":
		return replaceVerb(argv, "install", "remove")
	case "winget":
		return replaceVerb(argv, "install", "uninstall")
	default:
		return argv
	}
}

func replaceVerb(argv []string, from, to string) []string {
	out := append([]string{}, argv...)
	for i, a := range out {
		if a == from {
			out[i] = to
			break
		}
	}
	return out
}

// lastLines returns the final n non-empty lines (package managers report the
// real error at the end), so the UI shows something actionable.
func lastLines(s string, n int) string {
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return "no output"
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
