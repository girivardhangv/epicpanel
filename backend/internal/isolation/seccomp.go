package isolation

// Seccomp is intentionally NOT shipped as a hand-rolled BPF program.
//
// Rationale: an incorrect denylist or jump-table silently disables isolation
// or breaks every syscall. bubblewrap already applies PR_SET_NO_NEW_PRIVS to
// the sandbox (setuid binaries are inert), the PID namespace prevents process
// tampering, and the mount namespace removes kernel interfaces (/sys, /dev
// beyond the minimal set).
//
// FUTURE: when a maintained filter is required, use libseccomp-golang with a
// tested default-deny profile generated per-architecture, and load it via
// bwrap --seccomp-fd <fd> with ExtraFiles on the resulting exec.Cmd. The
// buildArgs function in sandbox.go is the documented integration point.
