package isolation

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// sysProcAttrFor builds the exec attributes for the sandbox child:
//   - setuid to the site user (web-terminal path; epicpanel-shell is already
//     the site user so Credential is only applied when UID is provided)
//   - rlimits applied at exec time (kernel-enforced)
//   - setsid for the pty
//
// NOTE: Go's syscall.SysProcAttr on Linux does not expose Rlimit directly;
// rlimits are applied by the shell command wrapper via a pre-exec hook using
// the prlimit helper below, executed INSIDE the sandbox as its first command
// chain. To keep the boundary kernel-enforced, rlimits are wrapped around the
// command line with the shell's ulimit builtin (runs as the site user before
// any user code).
func sysProcAttrFor(spec Spec) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// wrapRlimits prepends ulimit builtins so the kernel applies the limits
// before user code runs (ulimit is enforced by the shell, and inherited by
// everything the user spawns inside the sandbox).
//
// Deliberately NO `ulimit -v` (RLIMIT_AS): a virtual-memory cap breaks
// VM-based runtimes (V8, Go) that reserve large address space up front.
// Memory is enforced via cgroup MemoryMax instead (spec.Rlimits.MaxMemMB
// -> systemd-run scope / agent user slice).
func wrapRlimits(spec Spec, cmdline string) string {
	limits := []string{
		"ulimit -u " + strconv.Itoa(spec.Rlimits.MaxProc),
		"ulimit -n " + strconv.Itoa(spec.Rlimits.MaxFiles),
	}
	if spec.Rlimits.MaxFileMB > 0 {
		limits = append(limits, "ulimit -f "+strconv.Itoa(spec.Rlimits.MaxFileMB*1024)) // KB blocks
	}
	if spec.Rlimits.CPUSeconds > 0 {
		limits = append(limits, "ulimit -t "+strconv.Itoa(spec.Rlimits.CPUSeconds))
	}
	return strings.Join(limits, "; ") + "; " + cmdline
}

// Command builds a sandboxed command running as the site user (root-run
// web-terminal path). Equivalent to:
//
//	setpriv --reuid UID --regid GID --clear-groups --no-new-privs bwrap ...
func Command(spec Spec, cmdline string) (*exec.Cmd, error) {
	sandbox, err := NewSandbox(spec)
	if err != nil {
		return nil, err // fail closed
	}
	args, err := sandbox.buildArgs(wrapRlimits(spec, cmdline))
	if err != nil {
		return nil, err
	}
	
	// Wrap bwrap in systemd-run for cgroup limits
	sdArgs := []string{
		"--scope",
		"--quiet",
		fmt.Sprintf("--slice=epicpanel-%s.slice", spec.HostUsername),
	}
	if spec.UID > 0 && spec.GID > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--uid=%d", spec.UID), fmt.Sprintf("--gid=%d", spec.GID))
	}
	if spec.Rlimits.MaxMemMB > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("-p"), fmt.Sprintf("MemoryMax=%dM", spec.Rlimits.MaxMemMB))
	}
	if spec.Rlimits.CPUQuota > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("-p"), fmt.Sprintf("CPUQuota=%d%%", spec.Rlimits.CPUQuota))
	}
	sdArgs = append(sdArgs, sandbox.BwrapBin)
	sdArgs = append(sdArgs, args...)

	cmd := exec.Command("systemd-run", sdArgs...)
	cmd.SysProcAttr = sysProcAttrFor(spec)
	return cmd, nil
}

// SelfCommand builds a sandboxed command running as the CURRENT user
// (for epicpanel-shell, which is already the site user — no setuid).
func SelfCommand(spec Spec, cmdline string) (*exec.Cmd, error) {
	sandbox, err := NewSandbox(spec)
	if err != nil {
		return nil, err
	}
	args, err := sandbox.buildArgs(wrapRlimits(spec, cmdline))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(sandbox.BwrapBin, args...)
	return cmd, nil
}

var _ = fmt.Sprintf

func fmtSscanf(s string, format string, args ...any) (int, error) {
	return fmt.Sscanf(s, format, args...)
}
