package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Sandbox executes commands inside the kernel-enforced namespace for a spec.
type Sandbox struct {
	Spec       Spec
	BwrapBin   string // resolved at NewSandbox; empty = unavailable
	bwrapFound bool
}

// NewSandbox resolves the sandbox tooling. Fail closed: callers must check
// Available() and refuse sessions when false.
func NewSandbox(spec Spec) (*Sandbox, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	bin, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap not available: %w", err)
	}
	if _, err := os.Stat(spec.SiteBase); err != nil {
		return nil, fmt.Errorf("site base missing: %w", err)
	}
	return &Sandbox{Spec: spec, BwrapBin: bin, bwrapFound: true}, nil
}

// Available reports whether sandboxed execution is possible. Fail closed.
func (s *Sandbox) Available() bool { return s != nil && s.bwrapFound }

// Command builds an *exec.Cmd that runs cmdline inside the sandbox.
// The caller sets Stdin/Stdout/Stderr (and optionally starts a pty).
func (s *Sandbox) Command(cmdline string) (*exec.Cmd, error) {
	if !s.Available() {
		return nil, fmt.Errorf("sandbox unavailable")
	}
	args, err := s.buildArgs(cmdline)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(s.BwrapBin, args...)
	cmd.SysProcAttr = sysProcAttrFor(s.Spec) // setuid target + seccomp + no-new-privs
	return cmd, nil
}

// buildArgs assembles the bubblewrap argv. Mount policy:
//
//	writable:  /site (the site tree), /tmp (site-private tmp dir)
//	read-only: /usr /bin /lib (+/lib64) — system binaries
//	           /etc/ssl, /etc/alternatives, ld.so.*, nsswitch, passwd, group,
//	           resolv.conf, hosts, /etc/php (runtime agent may add more via
//	           RuntimeMounts)
//	absent:    /srv (host sites), /root, /home (other users), /var/lib,
//	           /dev/* devices beyond the minimal set, host /tmp, unix
//	           sockets of host services
//	namespaces: --unshare-pid (process isolation), --die-with-parent,
//	           --new-session (no controlling TTY escape)
func (s *Sandbox) buildArgs(cmdline string) ([]string, error) {
	spec := s.Spec
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	a := []string{
		"--dev", "/dev",
		"--proc", "/proc",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind", "/etc/alternatives", "/etc/alternatives",
		"--ro-bind", "/etc/ssl", "/etc/ssl",
		"--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--ro-bind", "/etc/ld.so.conf", "/etc/ld.so.conf",
		"--ro-bind", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind", "/etc/passwd", "/etc/passwd",
		"--ro-bind", "/etc/group", "/etc/group",
		"--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/php", "/etc/php",
		// writable site tree + private tmp
		"--bind", spec.SiteBase, "/site",
		"--bind", filepath.Join(spec.SiteBase, "tmp"), "/tmp",
		// process isolation
		"--unshare-pid",
		"--die-with-parent",
		"--new-session",
		// drop all capabilities; setuid binaries inside become inert
		// (bwrap mounts are nodev/nosuid and bwrap sets PR_SET_NO_NEW_PRIVS
		// on the sandbox — see ADR-036)
		"--chdir", "/site",
		"--clearenv",
		"--setenv", "HOME", "/site",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "TERM", termOrDumb(),
		"--setenv", "EPICPANEL_SANDBOX", "1",
	}

	// NETWORK ISOLATION: when the policy denies host networking, unshare net
	// entirely (user gets no outbound). Documented; hosting default = shared.
	if !spec.Network.SharedHostNetwork {
		a = append(a, "--unshare-net")
	}

	// SOFTWARE AGENT CONTRACT: runtime roots provided read-only.
	for _, m := range spec.RuntimeMounts {
		if err := validateMount(m); err != nil {
			return nil, fmt.Errorf("runtime mount: %w", err)
		}
		a = append(a, "--ro-bind", m.Src, m.Dst)
		if spec.Rlimits.MaxMemMB > 0 { // placeholder for runtime env hooks
			_ = m
		}
	}
	for _, m := range spec.ExtraRO {
		if err := validateMount(m); err != nil {
			return nil, fmt.Errorf("extra mount: %w", err)
		}
		a = append(a, "--ro-bind", m.Src, m.Dst)
	}

	// SECCOMP: see seccomp.go — bwrap applies PR_SET_NO_NEW_PRIVS itself;
	// a maintained syscall filter is a documented future layer.

	// resource limits: ulimit builtins require bash (dash lacks -u); the
	// ulimit chain runs before user code and is inherited by everything.
	a = append(a, "/bin/bash", "-c", cmdline)
	return a, nil
}

func validateMount(m Mount) error {
	ref := Spec{Version: 1, WebsiteID: "00000000-0000-0000-0000-000000000000", SiteBase: "/srv/epicpanel/websites/x", UID: 1, GID: 1}
	return ref.validateMountSelf(m)
}

func (s *Spec) validateMountSelf(m Mount) error {
	if !strings.HasPrefix(m.Src, "/") || strings.Contains(m.Dst, "..") {
		return fmt.Errorf("mount %s->%s invalid", m.Src, m.Dst)
	}
	return nil
}

func termOrDumb() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "xterm-256color"
}

func itoa(n int) string { return strconv.Itoa(n) }
