# EpicPanel Isolation Layer — Security Model

Owner: **shell/isolation module** (`internal/isolation`, `cmd/agent-shell`,
`internal/terminal`, `internal/sshkeys`).

NOT owned by this module: software/runtime provisioning (`internal/runtimes`,
`internal/packages`, `internal/agent/runtime_ops.go` — the software agent's
work). This module only *consumes* runtime data through the contract in
§7 and ADR-037.

---

## 1. Sandbox construction

Every hosting-user session (web terminal OR SSH) runs inside one command
chain, built by `isolation.Command()` / `isolation.SelfCommand()`:

```
[panel/root path]   setpriv --reuid UID --regid GID --clear-groups
[shell path]        (already the site user)
                    bwrap  <namespaces>  <mounts>  <seccomp note>  sh -c <cmd>
```

### Mount namespace (filesystem isolation)

| Mount | Mode | Purpose |
|---|---|---|
| `/site` ← site tree | **read-write** | the customer's files (public, logs, tmp) |
| `/tmp` ← `<site>/tmp` | **read-write** | private tmp; host /tmp NOT mounted |
| `/usr`, `/bin`, `/lib`, `/lib64` | read-only | system + runtime binaries |
| `/etc/ssl`, `/etc/php`, `/etc/alternatives` | read-only | configs (read-only, non-sensitive) |
| `/etc/{passwd,group,resolv.conf,hosts,nsswitch,ld.so.*}` | read-only | enough for binaries to work |
| `/dev`, `/proc` | minimal | bwrap's restricted /dev + own proc |

**Not mounted at all:** `/srv` (host sites), `/root`, `/home` (other users),
`/var`, host `/tmp`, Docker/container sockets, `/etc/shadow`, sshd configs.
Other customers' site trees are invisible *in the namespace* (not merely
hidden), and their directories are `0750` (owner-only) as defense-in-depth
outside the sandbox too.

### PID namespace (process isolation)

`--unshare-pid`: the customer sees only their own processes. `ps` cannot
list host or other-tenant processes; `kill` cannot address them.

### Privilege isolation

* Session runs as the site's dedicated Unix user (never root).
* bwrap mounts are **nosuid/nodev**; bwrap sets **PR_SET_NO_NEW_PRIVS** on
  the sandbox — setuid binaries are inert even if one is bound in.
* `/bin/su`, `/usr/bin/sudo` are NOT mounted.
* All capabilities are dropped for the session user (non-root by design).
* Cron editing inside the sandbox is impossible (`crontab` setuid is inert);
  cron is managed via the panel's cron API.

### Syscall restrictions

bwrap applies PR_SET_NO_NEW_PRIVS. A maintained libseccomp deny/allow profile
is a documented future layer (`internal/isolation/seccomp.go` states the
rationale for not shipping hand-rolled BPF today).

### Network isolation

Shared host network by default (hosting users need outbound: git, composer,
npm). Mitigations for host-only services:

* Databases bind to localhost with password auth — reachable only with
  valid credentials, which the sandbox cannot read (no panel DB creds).
* Panel API requires session/token auth.
* `NetworkPolicy.SharedHostNetwork=false` in the spec switches to
  `--unshare-net` (no networking) when a plan requires it.
* Future: per-site netns + proxy is a documented extension of NetworkPolicy.

### Resource isolation

Pre-exec rlimits inherited by the whole sandbox tree (from the site's
hosting package where applicable):

| Limit | Default |
|---|---|
| RLIMIT_NPROC (processes) | 256 |
| RLIMIT_NOFILE (file descriptors) | 1024 |
| RLIMIT_AS (address space) | 2048 MB |
| RLIMIT_FSIZE (max file) | 2048 MB |
| RLIMIT_CPU | unlimited (cgroups own CPU) |

FPM pools carry the hard per-site memory/process limits (packages module);
disk quota usage is reported by the agent's `apply_quota`.

### Fail closed

No bubblewrap, kernel without unprivileged namespaces, missing spec file is
OK (narrow default spec), but a *broken* spec file, missing site tree, or
sandbox construction error always **deny the session** — never downgrade to
an unsandboxed shell.

---

## 2. SSH integration

```
SSH connection (key auth — managed per site via ssh_keys)
      ↓ sshd starts the site Unix user's login shell: epicpanel-shell
      ↓ epicpanel-shell resolves the site tree by uid, loads the sandbox spec
      ↓ builds the bwrap sandbox (fail closed)
      ↓ interactive shell / sshd -c command runs INSIDE the sandbox
```

* `ssh user@host command` → sshd runs `epicpanel-shell -c "command"` → the
  command is sandboxed identically (same code path; tested).
* authorized_keys carry `no-port-forwarding,no-X11-forwarding,no-agent-forwarding`.
* Zero keys ⇒ shell is reset to nologin (SSH disabled).
* SFTP uses the same sandbox via the shell -c path.

---

## 3. SOFTWARE AGENT INTEGRATION CONTRACT

The isolation module owns the sandbox; the software agent owns runtimes.
The boundary is the **sandbox spec file**:

```
/etc/epicpanel/sandbox/<websiteID>.json
{
  "version": 1,
  "website_id": "<uuid>",
  "site_base": "/srv/epicpanel/websites/<uuid>",
  "host_username": "ep-<org8>-<name>",
  "uid": 982, "gid": 976,
  "runtime_mounts": [
    {"src": "/usr/local/go-1.22.2", "dst": "/usr/local/go-1.22.2"}
  ],
  "rlimits": {...},
  "network": {"shared_host_network": true}
}
```

**How runtimes reach the sandbox:**

1. The software agent installs a runtime (its own module) and records the
   install in its `runtimes` table.
2. The agent daemon reconciles sandbox specs (`agent.SandboxSync.SyncSite`,
   run hourly + after runtime installs): it maps each installed runtime to a
   root path via `runtimeRoot()`:
   * apt/PPA runtimes (php, node, python) → `/usr` (already inside the
     read-only system bind; no extra mount needed)
   * out-of-tree toolchains (Go) → read-only bind of `/usr/local/go-X.Y.Z`
3. `spec.RuntimeMounts` are validated (`Validate()`) then mounted read-only.
4. The runtime becomes available inside the sandbox at its normal path —
   e.g. `php 8.5` at `/usr/bin/php8.5`, `go` at `/usr/local/bin/go`.

**The isolation module never:** installs runtimes, downloads packages,
writes under runtime roots, or assumes a runtime exists at a fixed path
beyond `/usr` (which is a mount, not an assumption of content).

**Adding a new runtime type:** extend `runtimeRoot()` in
`internal/agent/sandbox_sync.go` with the root path — that is the whole
integration. (That function belongs to this module but only *reads* the
layout the software agent created.)

---

## 4. Tests (internal/isolation)

| Test | Protects against |
|---|---|
| `TestValidateRejectsRootUID` | sandbox configured as root |
| `TestValidateRejectsBadSiteBase` | site_base outside /srv/epicpanel, traversal |
| `TestValidateRejectsBadRuntimeMounts` | mounts exposing /root, /etc/shadow, relative dst, overlapping /site |
| `TestSpecPathTraversalRejected` | `..` in mount destinations |
| `TestLoadFailsClosedToDefault` | corrupted spec ⇒ narrow default, never open |
| `TestSaveLoadRoundTrip` | runtime mounts survive persist/reload |
| `TestBuildArgsContainCoreSandbox` | core namespaces/binds present in argv |
| `TestBuildArgsNetworkUnshare` | network policy honored |
| `TestBuildArgsRuntimeMounts` | contract mounts appear in argv |
| `TestWrapRlimits` | rlimits applied before user code |
| `TestStatusRecorderImplementsHijacker` (httpapi) | middleware wrapper must not break WS upgrades |
| **`TestLiveSandboxEscapeAttempts`** (root-only, real bwrap) | the escape matrix below |

### Live escape matrix (real sandbox, uid 1500)

| Attempt | Result |
|---|---|
| `cat /etc/shadow` | not visible |
| `ls /root` | not visible |
| read another site's tree | not visible (also 0750 outside) |
| `bash -c 'cat /etc/shadow'` | still sandboxed |
| `ps aux` | only sandbox-internal processes |
| `mount --bind /tmp /site` | denied |
| symlink `/site/evil -> /etc` | target absent in namespace |
| write/delete own files | works |

---

## 5. Threat model

Assume the customer is malicious: valid SSH creds, normal user permissions,
ability to run/upload arbitrary code.

| Attack surface | Mitigation |
|---|---|
| Read/modify other customers' files | mount namespace (other trees absent) + 0750 dirs + distinct uid per site |
| Read host files (/etc, /root, /var) | not mounted; only minimal non-sensitive /etc files |
| Escape via symlink | namespace resolution happens before user code; /etc targets absent |
| Escape via `..` traversal | namespace is a real rootfs view, not a filter |
| Kill host/other processes | PID namespace; foreign pids unaddressable |
| Root escalation via setuid | no-new-privs + nosuid mounts + caps dropped |
| sudo/su escape | binaries not mounted |
| Install/modify system packages | /usr read-only |
| Access Docker/container sockets | sockets not mounted |
| Access panel DB/API on localhost | localhost services require credentials; NetworkPolicy can unshare net |
| Access other users' SSH keys | per-site authorized_keys under 0750/0700 dirs owned by the site user |
| SSH forced-command bypass | same sandbox code path for `-c` commands (tested) |
| Alternate shell escape (`ssh host bash`) | sshd always runs epicpanel-shell; argv is sandboxed identically |
| Resource exhaustion | rlimits pre-exec + packages layer (FPM/process limits) + agent apply_quota |
| Cron abuse | setuid crontab inert in sandbox; panel cron API with validation |

Residual risks (documented, accepted for V1): shared network namespace
(host localhost services protected only by their own auth); kernel
vulnerabilities in namespaces/bwrap (mitigate: keep bwrap/kernel updated);
seccomp filter not yet shipped.

---

## 6. Limitations

* Requires kernel ≥ 5.x with unprivileged user namespaces enabled (Ubuntu
  default) and `bubblewrap` installed; **fail closed** otherwise.
* Loopback is shared with the host (see NetworkPolicy for the opt-in
  `--unshare-net`).
* Disk quota is usage-accounted (agent `apply_quota`), not a hard fs quota;
  hard quotas need project-quota filesystem support (ext4/XFS) — future.
* Seccomp filter is future work (seccomp.go documents the integration).
* The sandbox bind layout assumes the repo's site-tree layout
  (`/srv/epicpanel/websites/<id>/{public,logs,tmp}`); changing the layout
  requires updating `Spec.SiteBase` prefix validation and `isSystemPath()`.
