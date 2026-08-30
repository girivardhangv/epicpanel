package software

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Component is the API view of one managed piece of software.
type Component struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	Service     string `json:"service,omitempty"`
	Running     bool   `json:"running"`
	Supported   bool   `json:"supported"` // installable via this host's package manager
}

// Manager runs software operations for the detected OS.
type Manager struct {
	os  OSInfo
	reg *Registry
	log *slog.Logger
}

func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{os: Detect(), reg: Default(), log: log}
}

// OS returns the detected platform info.
func (m *Manager) OS() OSInfo { return m.os }

// List detects every registered component.
func (m *Manager) List(ctx context.Context) []Component {
	out := []Component{}
	for _, name := range m.reg.Names() {
		p, _ := m.reg.Get(name)
		c := Component{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Category:    p.Category,
			Service:     p.Service,
			Supported:   len(p.Packages[m.os.PackageManager]) > 0,
		}
		if LookPath(p.Binary) {
			c.Installed = true
			c.Version = m.detectVersion(ctx, p)
		}
		if c.Installed && p.Service != "" {
			c.Running = m.serviceActive(ctx, p.Service)
		}
		out = append(out, c)
	}
	return out
}

func (m *Manager) detectVersion(ctx context.Context, p Provider) string {
	res, err := Run(ctx, p.Binary, p.VersionArgs...)
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

// Install runs the provider's package-manager command, then enables+starts
// its service if it has one.
func (m *Manager) Install(ctx context.Context, name string) (CommandResult, error) {
	p, ok := m.reg.Get(name)
	if !ok {
		return CommandResult{}, fmt.Errorf("unknown component %q", name)
	}
	argv, ok := p.Packages[m.os.PackageManager]
	if !ok || len(argv) == 0 {
		return CommandResult{}, fmt.Errorf("%s is not installable via %s", name, m.os.PackageManager)
	}
	m.log.Info("installing software", "component", name, "manager", m.os.PackageManager)
	res, err := Run(ctx, argv[0], argv[1:]...)
	if err != nil {
		return res, err
	}
	if !res.OK() {
		return res, fmt.Errorf("install failed (exit %d): %s", res.ExitCode, firstLine(res.Stderr))
	}
	if p.Service != "" {
		_, _ = Run(ctx, "systemctl", "enable", "--now", p.Service)
	}
	return res, nil
}

// Remove uninstalls a component by transforming its install command.
func (m *Manager) Remove(ctx context.Context, name string) (CommandResult, error) {
	p, ok := m.reg.Get(name)
	if !ok {
		return CommandResult{}, fmt.Errorf("unknown component %q", name)
	}
	argv, ok := p.Packages[m.os.PackageManager]
	if !ok || len(argv) == 0 {
		return CommandResult{}, fmt.Errorf("%s is not manageable via %s", name, m.os.PackageManager)
	}
	remove := removeArgs(m.os.PackageManager, argv)
	if p.Service != "" {
		_, _ = Run(ctx, "systemctl", "stop", p.Service)
	}
	m.log.Info("removing software", "component", name)
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
	if m.os.Family == "windows" {
		netAction := ""
		switch action {
		case "start":
			netAction = "start"
		case "stop":
			netAction = "stop"
		default:
			return CommandResult{}, fmt.Errorf("only start/stop are supported on windows")
		}
		return Run(ctx, "net", netAction, p.Service)
	}
	return Run(ctx, "systemctl", action, p.Service)
}

func (m *Manager) serviceActive(ctx context.Context, service string) bool {
	res, err := Run(ctx, "systemctl", "is-active", service)
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "active"
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

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
