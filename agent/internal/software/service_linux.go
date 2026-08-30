//go:build linux

package software

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scServiceExists is Windows-only; on Linux services are systemd units, not
// sc-managed Windows services, so there is nothing to probe here.
func scServiceExists(name string) bool { return false }

// ensureManagedUnit writes a systemd unit for a self-contained (compiled or
// downloaded) component so the panel can start/stop/restart it via systemctl.
// The unit is idempotent: writing the same content again is a no-op.
func (m *Manager) ensureManagedUnit(ctx context.Context, p Provider, bin string) error {
	unit := "epicpanel-" + p.Name + ".service"
	path := "/etc/systemd/system/" + unit
	content := m.managedUnitContent(p, bin)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil // already up-to-date
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write systemd unit %s: %w", unit, err)
	}
	_, _ = Run(ctx, "systemctl", "daemon-reload")
	return nil
}

// managedUnitContent generates the systemd unit content for a self-contained
// component. The binary is the absolute path to the primary executable.
func (m *Manager) managedUnitContent(p Provider, bin string) string {
	desc := "EpicPanel managed " + p.DisplayName
	dir := filepath.Dir(bin)
	switch p.Name {
	case "nginx":
		return fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s -g 'daemon off;' -p %s
ExecReload=%s -s reload -p %s
Restart=on-failure
RestartSec=5
KillMode=process

[Install]
WantedBy=multi-user.target
`, desc, bin, dir, bin, dir)
	case "php":
		return fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s/sbin/php-fpm --nodaemonize --fpm-config %s/etc/php-fpm.conf
ExecReload=kill -USR2 $MAINPID
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, desc, dir, dir)
	case "redis":
		// Compiled redis installs redis-server into <dir>/bin via PREFIX.
		return fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s/bin/redis-server %s/etc/redis.conf --daemonize no
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, desc, dir, dir)
	case "apache":
		return fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s/bin/httpd -DFOREGROUND -f %s/conf/httpd.conf
ExecReload=%s/bin/httpd -k graceful -f %s/conf/httpd.conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, desc, dir, dir, dir, dir)
	default:
		return fmt.Sprintf(`[Unit]
Description=%s

[Service]
Type=simple
ExecStart=%s
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, desc, bin)
	}
}

// serviceActive reports whether a component's service is running.
func (m *Manager) serviceActive(ctx context.Context, p Provider) bool {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return false
	}
	if spec.mode == ServiceNone {
		return false
	}
	res, err := Run(ctx, "systemctl", "is-active", spec.unit)
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "active"
}

func (m *Manager) serviceStart(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode == ServiceNone {
		return nil
	}
	if _, err := Run(ctx, "systemctl", "start", spec.unit); err != nil {
		return err
	}
	return nil
}

func (m *Manager) serviceStop(ctx context.Context, p Provider) (CommandResult, error) {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return CommandResult{}, err
	}
	if spec.mode == ServiceNone {
		return CommandResult{ExitCode: 0}, nil
	}
	return Run(ctx, "systemctl", "stop", spec.unit)
}

func (m *Manager) serviceRestart(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode == ServiceNone {
		return nil
	}
	_, err = Run(ctx, "systemctl", "restart", spec.unit)
	return err
}

func (m *Manager) serviceReload(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode == ServiceNone {
		return nil
	}
	if _, err := Run(ctx, "systemctl", "reload", spec.unit); err != nil {
		_, err = Run(ctx, "systemctl", "restart", spec.unit)
		return err
	}
	return nil
}

func (m *Manager) serviceEnable(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode == ServiceNone {
		return nil
	}
	_, err = Run(ctx, "systemctl", "enable", spec.unit)
	return err
}

func (m *Manager) serviceDisable(ctx context.Context, p Provider) error {
	spec, err := m.serviceSpec(p)
	if err != nil {
		return err
	}
	if spec.mode == ServiceNone {
		return nil
	}
	_, err = Run(ctx, "systemctl", "disable", spec.unit)
	return err
}

var _ = fmt.Sprintf
