// epicpanel-shell — sandboxed login shell for EpicPanel site users.
// The security boundary is the kernel sandbox built by internal/isolation
// (bubblewrap mount+PID namespaces, no-new-privs, rlimits). The shell is a
// convenience layer on top; isolation does not depend on it.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/epicbyte/epicpanel/backend/internal/isolation"
)

func main() {
	if os.Getuid() == 0 {
		fmt.Fprintln(os.Stderr, "epicpanel-shell: refusing to run as root")
		os.Exit(1)
	}

	// ALREADY SANDBOXED? The web-terminal path (isolation.Command from the
	// panel) pre-builds the kernel sandbox and starts this shell INSIDE it —
	// /srv is not mounted there, so re-resolving would fail. In that case
	// this binary is simply the interactive shell: no second sandbox.
	if os.Getenv("EPICPANEL_SANDBOX") == "1" {
		if len(os.Args) >= 3 && os.Args[1] == "-c" {
			runPlain(os.Args[2], true)
			return
		}
		runPlainInteractive()
		return
	}

	spec, siteBase, err := resolveSpec()
	_ = siteBase
	if err != nil {
		// fail closed: deny instead of unsandboxed shell
		fmt.Fprintln(os.Stderr, "epicpanel-shell: cannot build sandbox:", err)
		os.Exit(126)
	}

	// sshd command execution: epicpanel-shell -c "command"
	if len(os.Args) >= 3 && os.Args[1] == "-c" {
		runSandboxed(spec, os.Args[2], true)
		return
	}

	// Interactive session.
	fmt.Println("EpicPanel sandboxed shell — only your site files are visible. Type 'exit' to quit.")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for {
		fmt.Print("$ ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "logout" {
			return
		}
		runSandboxed(spec, line, false)
	}
}

// resolveSpec locates the site tree owned by our uid and loads its sandbox
// spec (fail closed to the narrow default when no config exists).
func resolveSpec() (spec isolation.Spec, siteBase string, err error) {
	uid := os.Getuid()
	matches, gerr := filepath.Glob("/srv/epicpanel/websites/*")
	if gerr != nil || len(matches) == 0 {
		return isolation.Spec{}, "", fmt.Errorf("no site tree found")
	}
	for _, m := range matches {
		info, statErr := os.Stat(m)
		if statErr != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid {
			websiteID := filepath.Base(m)
			spec = isolation.Load(websiteID, m, "", uid, int(st.Gid))
			// Never trust the spec's uid — the kernel sandbox must always
			// run as the user that owns this tree.
			spec.UID = uid
			spec.GID = int(st.Gid)
			spec.SiteBase = m
			spec.WebsiteID = websiteID
			return spec, m, nil
		}
	}
	// sshd may race a site rename/delete; include uid for supportability.
	return isolation.Spec{}, "", fmt.Errorf(
		"no site tree found for uid %d (site may have been deleted or not yet provisioned)", uid)
}

// runPlain executes a command directly (already inside the panel's sandbox).
func runPlain(cmdline string, propagate bool) {
	cmd := exec.Command("/bin/bash", "-c", cmdline)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && propagate {
			os.Exit(exitErr.ExitCode())
		}
	}
}

// runPlainInteractive is the prompt loop when already sandboxed by the panel.
func runPlainInteractive() {
	fmt.Println("EpicPanel shell — only your site files are visible. Type 'exit' to quit.")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for {
		fmt.Print("$ ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "logout" {
			return
		}
		runPlain(line, false)
	}
}

func runSandboxed(spec isolation.Spec, cmdline string, propagate bool) {
	cmd, err := isolation.SelfCommand(spec, cmdline)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox unavailable:", err)
		if propagate {
			os.Exit(126)
		}
		return
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && propagate {
			os.Exit(exitErr.ExitCode())
		}
		// interactive: non-zero exits don't kill the session
	}
}
