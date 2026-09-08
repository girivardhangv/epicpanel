// Package isolation implements the kernel-enforced sandbox layer for
// hosting users (restricted shell + web terminal).
//
// OWNERSHIP: this package owns sandbox construction, mount policy, seccomp
// policy, resource limits inside the sandbox, and the runtime-integration
// registry (MountContract). It does NOT install or manage runtimes — the
// software/runtime agent registers executable paths through the contract.
//
// SECURITY MODEL (defense in depth, all kernel-enforced):
//
//  1. Filesystem: bubblewrap mount namespace. Only the site tree (writable,
//     at /site), its private /tmp, and read-only system dirs exist inside the
//     namespace. /srv, /root, other sites, host configs: NOT MOUNTED — they
//     are invisible, not merely hidden. Symlink escapes are impossible
//     because the namespace is built before any user code runs and mounts
//     are resolved root-side.
//
//  2. Processes: PID namespace (--pid --dev) — the user sees only their own
//     processes; `ps`, `kill` cannot reach host or other-tenant processes.
//
//  3. Privilege: no-new-privileges (blocks setuid escalation), all caps
//     dropped (--cap-drop ALL), /bin/su + /usr/bin/sudo are NOT mounted.
//
//  4. Syscalls: seccomp filter (SECCOMP_RET_ERRNO) blocks dangerous syscalls
//     (mount, umount2, unshare, setns, reboot, kexec_load, init_module,
//     keyctl, ptrace, bpf, userfaultfd, perf_event_open, swapon/off).
//
//  5. Network: shared by default (hosting users need outbound), but host-only
//     services are blocked because loopback is remapped: the sandbox gets a
//     fresh loopback (--dev brings loopback down) and host-only Unix sockets
//     are not mounted. A NetworkPolicy hook allows tightening later.
//
//  6. Resources: rlimits applied pre-exec (nproc, nofile, as, fsize, cpu).
//     Integration with the packages/cgroup layer is via ApplyResourcePolicy.
//
//  7. Fail closed: if the sandbox cannot be constructed (no bwrap, kernel
//     without unprivileged namespaces, site tree missing), the session is
//     DENIED — never downgraded to an unsandboxed shell.
package isolation
