package software

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// allowedExecutables is the strict command allowlist. Only these binaries may
// ever be executed by the engine, and always with provider-constructed argv —
// never a string parsed from a request. This is the core safety guarantee.
var allowedExecutables = map[string]bool{
	"apt-get": true, "apt": true, "dnf": true, "yum": true, "zypper": true,
	"systemctl": true, "service": true, "useradd": true, "getent": true,
	"winget": true, "choco": true, "sc": true, "net": true,
	"nginx": true, "php": true, "php-fpm": true, "mariadb": true, "mysql": true,
	"mysqladmin": true, "redis-server": true, "redis-cli": true, "node": true,
	"npm": true, "docker": true, "java": true, "psql": true, "pg_ctl": true,
	"httpd": true, "apachectl": true,
}

// CommandResult captures a structured execution outcome.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// OK reports whether the command exited 0.
func (r CommandResult) OK() bool { return r.ExitCode == 0 }

// Run executes an allowlisted binary with fixed arguments (no shell, no
// string interpolation of user input). Non-zero exits are returned as a
// result, not an error, so callers can inspect exit codes deliberately.
func Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	if !allowedExecutables[name] {
		return CommandResult{}, fmt.Errorf("command not permitted: %s", name)
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		return res, fmt.Errorf("command timed out: %s", name)
	case err == nil:
		res.ExitCode = 0
	case errorsAsExit(err):
		res.ExitCode = exitCode(err)
	default:
		// Binary missing / not executable.
		return res, fmt.Errorf("exec %s: %w", name, err)
	}
	return res, nil
}

func errorsAsExit(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// LookPath reports whether a binary is available (used for detection).
func LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
