package agent

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// serviceUnitRe mirrors the control-plane allowlist (servers/service_ops.go):
// only these systemd units may be restarted by the restart_service job. The
// agent re-validates — defense in depth, the payload is untrusted input.
var serviceUnitRe = regexp.MustCompile(`^(nginx|apache2|lsws|openlitespeed|php[0-9]+\.[0-9]+-fpm|mysql|mariadb|postgresql)$`)

// RestartService performs a WHM-style service restart: systemctl restart for
// an allowlisted unit, then is-active verification (a unit that comes up
// dead must fail the job, not report success).
func (e *Executor) RestartService(ctx context.Context, service string) error {
	if !serviceUnitRe.MatchString(service) {
		return fmt.Errorf("service %q is not in the allowlist", service)
	}
	if err := runCmd(ctx, "systemctl", "restart", service); err != nil {
		return fmt.Errorf("restart %s: %w", service, err)
	}
	// Give the unit a moment, then verify it is actually running.
	time.Sleep(500 * time.Millisecond)
	out, err := execCommand("systemctl", "is-active", service).Output()
	state := strings.TrimSpace(string(out))
	if err != nil && state != "active" {
		return fmt.Errorf("%s is %s after restart", service, state)
	}
	slog.Info("service restarted", "service", service, "state", state)
	return nil
}
