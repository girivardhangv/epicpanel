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
	"winget": true, "choco": true, "sc": true, "net": true, "taskkill": true, "tasklist": true,
	"nginx": true, "php": true, "php-fpm": true, "mariadb": true, "mariadbd": true,
	"mariadb-install-db": true, "mysql": true, "mysqladmin": true, "redis-server": true, "redis-cli": true, "node": true,
	"npm": true, "docker": true, "java": true, "psql": true, "pg_ctl": true,
	"httpd": true, "apachectl": true,
	// Source build toolchain (Linux nginx/PHP compilation).
	"make": true, "configure": true, "gcc": true, "cc": true, "g++": true,
	"c++": true, "pkg-config": true, "autoconf": true, "ld": true, "strip": true,
}

// CommandResult captures a structured execution outcome.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// OK reports whether the command exited 0.
func (r CommandResult) OK() bool { return r.ExitCode == 0 }

// Run executes an allowlisted binary (resolved via PATH) with fixed arguments
// (no shell, no string interpolation of user input). Non-zero exits are
// returned as a result, not an error, so callers can inspect exit codes.
func Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	if !allowedExecutables[name] {
		return CommandResult{}, fmt.Errorf("command not permitted: %s", name)
	}
	return runCmd(ctx, name, args...)
}

// RunPath executes a binary at an absolute path (not PATH-resolved) with the
// same allowlist and safety guarantees as Run. The base name of the path must
// be in the allowlist.
func RunPath(ctx context.Context, path string, args ...string) (CommandResult, error) {
	base := cmdBase(path)
	if !allowedExecutables[base] {
		return CommandResult{}, fmt.Errorf("command not permitted: %s", base)
	}
	return runCmd(ctx, path, args...)
}

func cmdBase(path string) string {
	// filepath.Base on Windows keeps the ext; we compare against the
	// allowlist which has no ext, so strip it.
	b := path
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '/' || b[i] == '\\' {
			b = b[i+1:]
			break
		}
	}
	if len(b) > 4 && b[len(b)-4:] == ".exe" {
		b = b[:len(b)-4]
	}
	return b
}

func runCmd(ctx context.Context, name string, args ...string) (CommandResult, error) {
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
		return res, fmt.Errorf("exec %s: %w", name, err)
	}
	return res, nil
}

// runCmdLong executes a command with a long (build-sized) timeout. Used for
// source compilation where configure/make can take well over 15 minutes.
func runCmdLong(ctx context.Context, name string, args ...string) (CommandResult, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
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

// LookPath reports whether a binary is available on PATH.
func LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}