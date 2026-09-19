# Cross-Distro Software Install Verification — EpicPanel Agent

**Date:** 2026-09-18 · **Method:** live installs of the agent's real installer code
(`backend/internal/agent`), executed inside proot sandboxes of 11 distro
images via the `cmd/installsim` harness, each install verified by an
independent `binary -v` probe. Raw logs: `~/epic-install-tests/results/`.

## 1. What was tested

| Runtime | Versions | Install paths exercised |
|---|---|---|
| PHP | 8.1, 8.2, 8.3, 8.4, 8.5 | distro apt → ondrej PPA (Ubuntu) / sury.org (Debian, **new**) → static-php-cli (always-available fallback) |
| Node.js | 20/22/24 | official nodejs.org tarball → per-major dir `/opt/epicpanel/node/<major>` (**new**) → NodeSource deb822 repo → distro packages (fallbacks) |
| Python | 3.12, 3.13 | distro packages → deadsnakes PPA → python-build-standalone |
| Go | 1.26 | go.dev tarball (latest patch auto-resolved) |
| Java | 21, 25 | distro OpenJDK → Adoptium Temurin tarball |
| Apache / OpenLiteSpeed | 2.4 / 1.8 | distro / litespeedtech repo |
| phpMyAdmin / Adminer | latest | files.phpmyadmin.net / adminer.org |
| DB engines | mariadb, postgresql | distro packages (apt + dnf) |

Distro matrix: Ubuntu 22.04/24.04/26.04, Debian 12/13, AlmaLinux 9/10,
Fedora, Alpine 3.22, Arch, openSUSE Leap 16.0.

## 2. Bugs found by the exercise (all fixed in `internal/agent/runtime_ops.go`)

1. **Static PHP could never validate standalone** — `installStaticPHP` wrote
   a `php-fpm.conf` including `pool.d/*.conf` but left the dir empty;
   php-fpm refuses zero-pool configs, so on distros without a repo path for
   the requested version every install failed at the mandatory `-t` check
   *and left partial symlinks behind*. Fix: a placeholder
   `epicpanel-default.conf` pool (www-data, dynamic pm) is written while the
   pool dir is empty. Site pools land beside it later.
2. **`verifyPHPFPM` only checked the binary exists** — a broken install
   (wrong arch, missing libs) passed verification. Now runs `php-fpm -v` and
   matches `PHP <major>.`.
3. **No version-repo path on Debian** — the fallback ladder jumped from
   distro repos straight to static builds, so Debian got static PHP even
   for versions sury.org packages natively (8.2/8.3 on bookworm etc.).
   Fix: `addSuryPHP` — same probe-before-pin discipline as the PPA path
   (refuses unpublished suites; keyed keyring; suite-probe over HTTPS).
4. **NodeSource via `curl | bash`** — remote script executed as root with
   `|| true` swallowing failures. Fix: `addNodeSourceRepo` writes a deb822
   `.sources` file with a pinned keyring and probes `dists/nodistro/Release`
   first; unsupported suites fall through to the official tarball instead of
   poisoning apt.
5. **installGo symlink upgrade bug** — `os.Symlink` silently fails on an
   existing link, so after a Go minor upgrade the *old* toolchain stayed on
   PATH (and "already installed" checks masked it). Fix: atomic
   `symlinkReplace` (tmp+rename), plus a functional `go version` check.
6. **Python hard-coded apt** — now routes distro packages through the
   detected PackageManager (dnf/apk/pacman/zypper work), venv subpackage
   best-effort.
7. **RPM distros had no PHP path at all** — el9+/fedora name the binary
   `php-fpm` (unversioned) and ship one version. The non-apt branch now
   recognizes a matching distro PHP and otherwise installs the static build
   directly (previously it ran the Ubuntu PPA ladder pointlessly before
   landing on static).
8. **Catalog staleness** — `runtimes/software.go` offered PHP 8.1 (EOL
   2025-12), Node 20/21/23 (21/23 EOL non-LTS), Go 1.24 (unsupported).
   Catalog now lists supported versions only (PHP 8.2–8.5, Node 22/24,
   Go 1.26/1.27); the agent still honors explicit legacy versions.
9. **Adminer download 404** — GitHub release asset `adminer-mysql-en.php`
   vanished with Adminer 5 (caught live). Now downloads
   `adminer.org/latest.php` (302 → versioned file; verified Adminer 6.1.0).
10. **Node on non-apt distros skipped the package manager** — the official
    tarball is glibc-linked and can never run on musl (Alpine). Non-apt
    distros now install `nodejs` from their package manager first (version
    must match the requested major, else tarball), tarball only as
    fallback; a stale managed `/usr/local/bin/node` symlink from a prior
    tarball attempt is removed when it does not execute (it shadowed the
    working distro node).
11. **DB-engine installer was apt-only** — `mariadb-client` does not exist
    on rpm distros (client comes with `mariadb-server`); `postgresql`
    alone on el9 is client-only. `InstallDatabaseEngine` now picks
    per-family package names and requires BOTH client and server binaries
    before declaring "already installed" (el9 caught live: psql present,
    zero server).
12. **Failed downloads poisoned retries** (caught live in production use) —
    installers left partial/stale tarballs in `/tmp` on failure; Ubuntu's
    sticky `/tmp` with `fs.protected_regular` then made re-opening the
    other-user file fail with curl exit 23 *even for root*, so every retry
    failed until the file was removed by hand. All download sites now go
    through `downloadFile`, which clears the target before and removes it
    on failure.
13. **Node installs were system-wide and single-major** (2026-09-19) — the
    registry models one install per major, but apt holds ONE nodejs and
    `/usr/local/bin/node` is a single symlink, so a Node 20 site and a
    Node 22 site could never coexist on a server. Reworked
    (`runtime_node.go`): official tarballs install per-major into
    `/opt/epicpanel/node/<major>` (the python-standalone/java pattern);
    app builds and systemd units exec the absolute managed binary and get
    the major's bin dir prepended to PATH; `NodeSource`/distro packages
    remain only as fallbacks (and the only path on musl). Convenience
    `node`/`npm`/`npx` shims are re-pointed only when panel-owned, and
    removal heals them to the highest remaining managed major.

Pre-existing (not from this change): `TestRenderVhostSuspended` and
`TestRenderMatrix` fail on the uncommitted working tree (nginx.go /
webserver_ops.go WIP); they pass on HEAD.

## 3. Verified results (2026-09-18 runs)

RESULT = agent's own install outcome; VERIFY = independent binary probe.

| Distro | Results | Notes |
|---|---|---|
| Ubuntu 22.04 | php 8.1–8.5 + node22 + py3.13 — **7/7 PASS** | 8.1/8.2 distro; 8.3–8.5 ondrej PPA; node via NodeSource; py3.13 standalone |
| Ubuntu 24.04 | (final numbers below) | |
| Ubuntu 26.04 | ships PHP 8.5 natively (first distro to do so) | sandbox needed a coreutils workaround; re-run below |
| Debian 12 | php 8.1–8.5 + node22 + py3.12 — **7/7 PASS** | 8.1/8.2 distro; 8.3–8.5 sury.org (new path); node NodeSource; py3.12 standalone |
| Debian 13 | php 8.1–8.5 + node24 + py3.13 + java25 — **8/8 PASS** | |
| AlmaLinux 9 | php 8.1–8.5 + node22 + py3.12 + java21 — **8/8 PASS** | all five PHP versions via static builds (el9 BaseOS ships no PHP) |
| AlmaLinux 10 | php 8.1 8.3 8.4 8.5 + node24 + py3.12 — **6/6 PASS** | |
| Fedora | php 8.4 8.5 + node24 — **3/3 PASS** | |
| Alpine 3.22 | (final numbers below) | |
| Arch | php 8.4 8.5 + node22 — **3/3 PASS** | pacman's native 8.4 recognized |
| openSUSE Leap 16 | php 8.3 8.4 + node22 — **3/3 PASS** | |

### 3.1 Final verified matrix (all RESULT + VERIFY green)

| Distro | Installs verified |
|---|---|
| Ubuntu 22.04 | php 8.1 8.2 8.3 8.4 8.5 · node 22 · python 3.13 (7/7) |
| Ubuntu 24.04 | php 8.1–8.5 · node 22 · python 3.12 · java 21 · apache 2.4 · openlitespeed 1.8 · phpmyadmin · adminer (11/12→12/12 after adminer fix) |
| Ubuntu 26.04 | php 8.1–8.5 (8.5 native) · node 24 · python 3.13 · go 1.26 (8/8) |
| Debian 12 | php 8.1–8.5 (8.3–8.5 via sury.org) · node 22 · python 3.12 (7/7) |
| Debian 13 | php 8.1–8.5 · node 24 · python 3.13 · java 25 (8/8) |
| AlmaLinux 9 | php 8.1–8.5 (static) · node 22 · python 3.12 · java 21 (8/8) + mariadb/postgresql engines |
| AlmaLinux 10 | php 8.1 8.3 8.4 8.5 · node 24 · python 3.12 (6/6) |
| Fedora | php 8.4 8.5 · node 24 (3/3) |
| Alpine 3.22 | php 8.3 8.4 8.5 (static) · node 22 via apk (4/4) |
| Arch | php 8.4 8.5 · node 22 (3/3) |
| openSUSE Leap 16 | php 8.3 8.4 · node 22 (3/3) |
| DB engines | mariadb + postgresql server/client on apt (Ubuntu 24.04) and dnf (AlmaLinux 9), server binaries verified |

## 4. Performance: distro vs static PHP (Debian 12, PHP 8.2, same box)

Inside-sandbox run (both builds pay identical proot overhead — relative
numbers are the signal; 5 runs, spread shown):

| Build | Compute loop | Startup (60× avg) | Binary size | Shared libs |
|---|---|---|---|---|
| distro (apt) | 0.073–0.083 s | ~105 ms | 5.4 MB | 16 |
| static-php-cli | 0.092–0.096 s | ~145 ms | 11.3 MB | 0 |

The distro build is ~20% faster on the synthetic compute and ~40% faster to
start. Both ship opcache. The static build's advantage is unconditional
installability (zero library deps, any distro, any version) — but it is a
frozen snapshot: security fixes arrive by re-deploying the binary, and its
extension set is compile-time fixed. This is exactly why the ladder prefers
distro → version repo (PPA/Sury) → static.

## 5. Recommended install matrix (what the panel should steer operators to)

1. **PHP**: prefer distro packages where the version exists (Ubuntu 26.04
   ships 8.5; Debian 12 has 8.2; Arch 8.4). For older versions on newer
   distros: ondrej PPA (Ubuntu family) / sury.org (Debian) — both probed
   suite-first, keyed, pinned. Static builds are the universal fallback:
   fully verified on every tested distro, zero library deps — trade-offs in
   §4 (extensions compile-time fixed; re-deploy binary on PHP CVEs).
2. **Node.js**: NodeSource repo (deb822) on apt distros; official tarball
   everywhere else and as fallback. LTS lines only in the catalog.
3. **Python**: distro python3.X → deadsnakes (Ubuntu family) → standalone
   build.
4. **Go / Java**: upstream tarballs (go.dev JSON / Adoptium API) — rock
   solid everywhere, no repo needed.
5. **DB engines**: distro packages — apt: `mariadb-server` + `postgresql`;
   rpm: `mariadb-server` + `postgresql-server` — verified on Ubuntu 24.04
   and AlmaLinux 9, server binaries checked.

## 6. Rig notes (reproducibility)

`~/epic-install-tests/`: `pull_rootfs.py` (registry → rootfs), `ep-run.sh`
(proot wrapper; `PROOT_NO_SECCOMP=1` required on this kernel; flaky proot
assertion crash under apt's subprocess storms — mitigate with
`stubborn.sh` retry-until-converged), `installsim` (built from
`backend/internal/agent/installsim`, calls the agent's real
`InstallRuntime`/`RemoveRuntime`/`InstallDatabaseEngine`),
`run_matrix.sh` (per-distro cases + verification), `bench2.sh` (PHP build
benchmark). Ubuntu 26.04 images ship Rust uutils coreutils which crash
under proot — the rig replaces the symlinks with GNU binaries from the
22.04 image (sandbox-only artifact).
