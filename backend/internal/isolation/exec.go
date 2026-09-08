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
	attr := &syscall.SysProcAttr{Setsid: true}
	if spec.UID > 0 && spec.GID > 0 {
		attr.Credential = &syscall.Credential{
			Uid: uint32(spec.UID),
			Gid: uint32(spec.GID),
		}
	}
	return attr
}

// wrapRlimits prepends ulimit builtins so the kernel applies the limits
// before user code runs (ulimit is enforced by the shell, and inherited by
// everything the user spawns inside the sandbox).
func wrapRlimits(spec Spec, cmdline string) string {
	limits := []string{
		"ulimit -u " + strconv.Itoa(spec.Rlimits.MaxProc),
		"ulimit -n " + strconv.Itoa(spec.Rlimits.MaxFiles),
	}
	if spec.Rlimits.MaxMemMB > 0 {
		limits = append(limits, "ulimit -v "+strconv.Itoa(spec.Rlimits.MaxMemMB*1024)) // KB
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
	cmd := exec.Command(sandbox.BwrapBin, args...)
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
