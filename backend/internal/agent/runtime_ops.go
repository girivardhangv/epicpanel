package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ProgressFunc receives (percent, stage) updates while a long install runs;
// the worker forwards them to the control plane for live UI feedback.
type ProgressFunc func(percent int, stage string)

// InstallRuntime installs a managed runtime version via OS packages.
// PHP: distro packages first (phpX.Y-fpm etc.); on Debian/Ubuntu fall back to
// the Ondřej Surý PPA for versions the distro does not ship. Idempotent:
// already-installed packages short-circuit to success.
// Apache and OpenLiteSpeed install as managed web servers.
func (e *Executor) InstallRuntime(ctx context.Context, runtimeID, rtType, version string, progress ...ProgressFunc) error {
	p := ProgressFunc(func(int, string) {})
	for _, fn := range progress {
		if fn != nil {
			p = fn
		}
	}
	switch rtType {
	case "php":
		return e.installPHP(ctx, runtimeID, version, p)
	case "node":
		return e.installNode(ctx, runtimeID, version, p)
	case "python":
		return e.installPython(ctx, runtimeID, version, p)
	case "go":
		return e.installGo(ctx, runtimeID, version, p)
	case "apache":
		return e.installApache(ctx, p)
	case "openlitespeed":
		return e.installOpenLiteSpeed(ctx, p)
	case "java":
		return e.installJava(ctx, runtimeID, version, p)
	default:
		return fmt.Errorf("unsupported runtime type %q", rtType)
	}
}

func (e *Executor) installApache(ctx context.Context, p ProgressFunc) error {
	// Required modules: ensure on every converge (idempotent) so pre-existing
	// Apache installs get them too.
	p(70, "Enabling proxy/fcgi modules…")
	for _, m := range []string{"proxy", "proxy_fcgi", "setenvif", "mime", "rewrite", "headers"} {
		_ = e.run(ctx, "a2enmod", m)
	}
	if _, err := exec.LookPath("apache2"); err == nil {
		p(100, "Apache already installed")
		return nil
	}
	p(20, "Installing Apache HTTP Server (apache2)…")
	if err := e.aptInstall(ctx, []string{"apache2"}, "", "2.4"); err != nil {
		return fmt.Errorf("install apache2: %w", err)
	}
	// Do not bind port 80 while nginx owns it; panel sites choose the web
	// server explicitly. Keep apache installed but stopped unless used.
	_ = e.run(ctx, "systemctl", "stop", "apache2")
	_ = e.run(ctx, "systemctl", "disable", "apache2")
	p(100, "Apache 2.4 installed")
	return nil
}

func (e *Executor) installOpenLiteSpeed(ctx context.Context, p ProgressFunc) error {
	if _, err := os.Stat("/usr/local/lsws/bin/openlitespeed"); err == nil {
		p(100, "OpenLiteSpeed already installed")
		return nil
	}
	p(10, "Adding the OpenLiteSpeed repository…")
	key := "/etc/apt/trusted.gpg.d/litespeed.gpg"
	if err := e.run(ctx, "sh", "-c", "curl -fsSL https://rpms.litespeedtech.com/debian/lst_repo.gpg | gpg --dearmor -o "+key); err != nil {
		return fmt.Errorf("litespeed repo key: %w", err)
	}
	codename := distroCodename()
	repo := "deb http://rpms.litespeedtech.com/debian/ " + codename + " main"
	if err := os.WriteFile("/etc/apt/sources.list.d/litespeed.list", []byte(repo+"\n"), 0o644); err != nil {
		return err
	}
	p(30, "Refreshing package index…")
	_ = e.pm.Update(ctx)
	p(60, "Installing OpenLiteSpeed…")
	if err := e.aptInstall(ctx, []string{"openlitespeed"}, "", "1.8"); err != nil {
		return fmt.Errorf("install openlitespeed: %w", err)
	}
	p(90, "Configuring OpenLiteSpeed…")
	_ = e.run(ctx, "systemctl", "enable", "lsws")
	_ = e.run(ctx, "systemctl", "stop", "lsws")
	p(100, "OpenLiteSpeed installed")
	return nil
}

// distroCodename returns the VERSION_CODENAME used by third-party apt repos.
func distroCodename() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "jammy"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VERSION_CODENAME=") {
			return strings.Trim(strings.TrimPrefix(line, "VERSION_CODENAME="), `"`)
		}
	}
	return "jammy"
}

func (e *Executor) installPHP(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := versionMajor(version) // "8.3" -> "8.3"; "8" -> unsupported
	if major == "" {
		return fmt.Errorf("invalid PHP version %q (want major.minor)", version)
	}
	// Core packages are required.
	core := []string{
		"php" + major + "-fpm",
		"php" + major + "-cli",
		"php" + major + "-common",
	}
	// Extensions vary across PHP versions and distro repos (e.g. opcache is
	// not a separate package for some releases) — best-effort, never fatal.
	optional := []string{
		"php" + major + "-mysql",
		"php" + major + "-pgsql",
		"php" + major + "-curl",
		"php" + major + "-gd",
		"php" + major + "-mbstring",
		"php" + major + "-xml",
		"php" + major + "-zip",
		"php" + major + "-intl",
		"php" + major + "-opcache",
	}

	if _, err := exec.LookPath("php-fpm" + major); err == nil {
		slog.Info("php already installed", "version", major)
		p(100, "PHP "+major+" already installed")
		return nil
	}
	if _, err := exec.LookPath("php-fpm" + strings.ReplaceAll(major, ".", "")); err == nil {
		slog.Info("php already installed", "version", major)
		p(100, "PHP "+major+" already installed")
		return nil
	}

	install := func() error {
		p(20, "Installing PHP "+major+" core (fpm, cli)…")
		if err := e.aptInstall(ctx, core, runtimeID, version); err != nil {
			return err
		}
		p(60, "Installing PHP "+major+" extensions (mysql, curl, gd, mbstring…)…")
		if err := e.aptInstall(ctx, optional, runtimeID, version); err != nil {
			// One or more extensions unavailable for this version — install
			// them individually and skip the missing ones.
			slog.Warn("some php extensions unavailable; installing individually", "version", major)
			for _, pkg := range optional {
				if err := e.aptInstall(ctx, []string{pkg}, runtimeID, version); err != nil {
					slog.Warn("skipping unavailable php extension", "package", pkg)
				}
			}
		}
		p(90, "Verifying PHP "+major+" FPM…")
		return nil
	}

	if err := install(); err != nil {
		// Distro repos may not carry this version. Strategy:
		//  1. ondrej PPA — but ONLY when the PPA publishes THIS distro's own
		//     suite (native builds; deps resolve). Field-proven: pinning noble
		//     packages on a newer distro deadlocks on libxml2 Depends.
		//  2. otherwise: static PHP build (self-contained binary, zero system
		//     library dependencies) — always installable.
		p(15, "Version not in distro repos — trying ondrej/php PPA…")
		slog.Warn("distro install failed, trying PPA fallback", "err", err)
		ppaErr := e.addOndrejPPA(ctx) // skips cleanly when suite not published
		if ppaErr == nil {
			if verr := e.aptPackagesVisible(ctx, core); verr == nil {
				if ierr := install(); ierr == nil {
					if err := e.verifyPHPFPM(ctx, major); err != nil {
						return err
					}
					p(100, "PHP "+major+" ready (FPM + extensions)")
					return nil
				} else {
					slog.Warn("PPA packages visible but install failed; falling to static build", "err", ierr)
				}
			} else {
				slog.Warn("PPA packages not visible; falling to static build", "err", verr)
			}
		} else {
			slog.Warn("PPA path unavailable for this distro; using static PHP build", "err", ppaErr)
		}
		p(30, "Installing static PHP "+major+" build (self-contained)…")
		if err := e.installStaticPHP(ctx, major, p); err != nil {
			return fmt.Errorf("static PHP install: %w (ppa: %v; apt: %v)", err, ppaErr, err)
		}
		if err := e.EnsureComposer(ctx); err != nil {
			slog.Warn("composer install failed; skipping", "err", err)
		}
		p(100, "PHP "+major+" ready (static build)")
		return nil
	}
	if err := e.verifyPHPFPM(ctx, major); err != nil {
		return err
	}
	if err := e.EnsureComposer(ctx); err != nil {
		// Composer is best-effort: a failed download must not fail the PHP
		// runtime install (it can be retried on the next converge).
		slog.Warn("composer install failed; skipping", "err", err)
	}
	p(100, "PHP "+major+" ready (FPM + extensions)")
	return nil
}

// EnsureComposer installs Composer globally (/usr/local/bin/composer) if it
// is missing, running the official installer with integrity verification.
// Idempotent: an existing Composer short-circuits.
func (e *Executor) EnsureComposer(ctx context.Context) error {
	if _, err := exec.LookPath("composer"); err == nil {
		return nil
	}
	slog.Info("installing composer")
	if _, err := exec.LookPath("php"); err == nil {
		// cli is present via the runtime install
	} else {
		return fmt.Errorf("php cli required for composer")
	}
	if err := e.run(ctx, "curl", "-fsSL", "-o", "/tmp/composer-setup.php", "https://getcomposer.org/installer"); err != nil {
		return err
	}
	// Integrity check: the installer ships a SHA-384 of itself at the same URL.
	if err := e.run(ctx, "bash", "-c",
		"EXPECTED=$(curl -fsSL https://composer.github.io/installer.sig) && ACTUAL=$(sha384sum /tmp/composer-setup.php | awk '{print $1}') && [ \"$EXPECTED\" = \"$ACTUAL\" ]"); err != nil {
		return fmt.Errorf("composer installer signature mismatch: %w", err)
	}
	if err := e.run(ctx, "php", "/tmp/composer-setup.php", "--install-dir=/usr/local/bin", "--filename=composer"); err != nil {
		return err
	}
	_ = os.Remove("/tmp/composer-setup.php")
	if _, err := exec.LookPath("composer"); err != nil {
		return fmt.Errorf("composer binary missing after install")
	}
	slog.Info("composer installed")
	return nil
}

// errSuiteUnsupported: the PPA does not publish the running distro's suite.
var errSuiteUnsupported = errStr2("ppa suite not published for this distro")

type errStr2 string

func (e errStr2) Error() string { return string(e) }

// addOndrejPPA enables ppa:ondrej/php for the CURRENT distro suite only.
//
// Field-observed failure modes, all handled here:
//  1. `add-apt-repository ppa:ondrej/php` hangs forever: it fetches the GPG
//     key over hkp://keyserver.ubuntu.com:11371 — a port many cloud networks
//     block — with no timeout. → hard timeouts + noninteractive env.
//  2. Very new distro releases (e.g. codename "resolute") are not published
//     on the PPA yet → apt 404 "does not have a Release file", which then
//     poisons EVERY later apt-get update. → probe Launchpad for supported
//     suites and refuse foreign-suite pins (they deadlock on Depends).
//  3. Leftover broken PPA list files from a previous attempt. → removed
//     before writing ours (the fingerprint is fetched from Launchpad's API
//     over HTTPS — no keyserver, no hkp, no hardcoded key material).
//  4. Derivative distros (Linux Mint "zena", Pop!_OS, …): the PPA never
//     publishes the derivative codename, but DOES publish the base Ubuntu
//     suite the derivative is built against (Mint 22.x → noble, verified
//     live on this box). → ppaCodename falls back to UBUNTU_CODENAME.
func (e *Executor) addOndrejPPA(ctx context.Context) error {
	// Clean ALL previous ondrej state FIRST (stale 404 entries and
	// conflicting Signed-By keyrings are the field killers) — and never
	// after a write, so our own result is never pruned.
	if err := e.pruneOndrejSources(ctx); err != nil {
		return err
	}

	codename, err := e.ppaCodename(ctx, "ondrej/php")
	if err != nil {
		return err
	}

	// Signing key: fingerprint from Launchpad's HTTPS API (no hardcoded key
	// material), fetched from keyserver.ubuntu.com over HTTPS 443 — no hkp
	// port, so firewalled VPS networks still work.
	if err := e.run(ctx, "mkdir", "-p", "/etc/apt/keyrings"); err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.run(c, "bash", "-c",
		"set -e; FP=$(curl -fsSL --max-time 30 'https://api.launchpad.net/devel/~ondrej/+archive/ubuntu/php' | python3 -c \"import sys,json;print(json.load(sys.stdin).get('signing_key_fingerprint',''))\") && [ -n \"$FP\" ] && curl -fsSL --max-time 60 \"https://keyserver.ubuntu.com/pks/lookup?op=get&search=0x$FP\" | gpg --batch --yes --no-tty --dearmor -o /etc/apt/keyrings/ondrej-php.gpg"); err != nil {
		return fmt.Errorf("fetch ondrej signing key (fingerprint→keyserver over HTTPS): %w", err)
	}

	// Write the single canonical sources entry for the distro's own suite.
	c2, cancel2 := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"echo 'deb [signed-by=/etc/apt/keyrings/ondrej-php.gpg] https://ppa.launchpadcontent.net/ondrej/php/ubuntu "+codename+" main' > /etc/apt/sources.list.d/ondrej-php.list"); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
}

// addDeadsnakesPPA enables ppa:deadsnakes/ppa for the suite ppaCodename
// selects (derivative-safe, like addOndrejPPA) — but writes the sources
// entry directly: add-apt-repository would use the raw distro codename
// (Mint "zena"), which Launchpad does not publish for deadsnakes either.
func (e *Executor) addDeadsnakesPPA(ctx context.Context, codename string) error {
	// Prune previous deadsnakes state (same poisoning rules as ondrej).
	c0, cancel0 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel0()
	if err := e.run(c0, "bash", "-c",
		"rm -f /etc/apt/sources.list.d/deadsnakes*.list /etc/apt/sources.list.d/deadsnakes*.list.save /etc/apt/sources.list.d/deadsnakes-*.gpg 2>/dev/null || true; true"); err != nil {
		return err
	}
	if err := e.run(ctx, "mkdir", "-p", "/etc/apt/keyrings"); err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.run(c, "bash", "-c",
		"set -e; FP=$(curl -fsSL --max-time 30 'https://api.launchpad.net/devel/~deadsnakes/+archive/ubuntu/ppa' | python3 -c \"import sys,json;print(json.load(sys.stdin).get('signing_key_fingerprint',''))\") && [ -n \"$FP\" ] && curl -fsSL --max-time 60 \"https://keyserver.ubuntu.com/pks/lookup?op=get&search=0x$FP\" | gpg --batch --yes --no-tty --dearmor -o /etc/apt/keyrings/deadsnakes.gpg"); err != nil {
		return fmt.Errorf("fetch deadsnakes signing key: %w", err)
	}
	c2, cancel2 := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"echo 'deb [signed-by=/etc/apt/keyrings/deadsnakes.gpg] https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu "+codename+" main' > /etc/apt/sources.list.d/deadsnakes.list"); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
}

// ppaCodename picks the suite the PPA actually publishes for this system:
// VERSION_CODENAME first; when unpublished (Debian/Ubuntu derivatives like
// Mint), UBUNTU_CODENAME if the PPA carries it. Never bridges a foreign
// suite blindly — only the derivative's OWN base suite, which is what its
// packages are built against (Mint zena → noble; verified live). Foreign
// suites are the libxml2-Depends deadlock, so anything unpublished is an
// error, not a guess.
func (e *Executor) ppaCodename(ctx context.Context, ppaPath string) (string, error) {
	codename, err := e.distroCodename(ctx)
	if err != nil {
		return "", err
	}
	probe := func(suite string) bool {
		return e.ppaHasSuiteGeneral(ctx, ppaPath, suite)
	}
	if probe(codename) {
		return codename, nil
	}
	if base := e.ubuntuCodename(ctx); base != "" && base != codename && probe(base) {
		slog.Warn("derivative distro: using base-Ubuntu suite the PPA publishes",
			"distro_codename", codename, "base", base)
		return base, nil
	}
	slog.Warn("PPA does not publish this distro suite; refusing foreign-suite pin (dependency conflicts)",
		"distro", codename)
	return "", errSuiteUnsupported
}

// ubuntuCodename returns UBUNTU_CODENAME from /etc/os-release ("" when the
// file doesn't declare one — plain Debian/Ubuntu).
func (e *Executor) ubuntuCodename(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c", ". /etc/os-release && echo -n ${UBUNTU_CODENAME:-}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// distroCodename returns VERSION_CODENAME from /etc/os-release.
func (e *Executor) distroCodename(ctx context.Context) (string, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c", ". /etc/os-release && echo -n $VERSION_CODENAME")
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return "", fmt.Errorf("resolve distro codename: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ppaHasSuite probes the ondrej PPA's actual repository index (dists/) for a
// published suite — the authoritative source (the Launchpad API JSON does
// not list suites; the repo directory listing does). Kept for the test hook.
func (e *Executor) ppaHasSuite(ctx context.Context, suite string) bool {
	return e.ppaHasSuiteGeneral(ctx, "ondrej/php", suite)
}

// pruneOndrejSources removes stale/broken/duplicate ondrej list files
// (idempotent). Field case: a previous install left ondrej-php-noble.list
// pointing at a different Signed-By keyring — apt then refuses BOTH entries
// ("Conflicting values set for option Signed-By"). Every variant is removed;
// the single canonical file is written after this.
func (e *Executor) pruneOndrejSources(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return e.run(c, "bash", "-c",
		"rm -f /etc/apt/sources.list.d/ondrej-php*.list /etc/apt/sources.list.d/ondrej-php*.list.save /etc/apt/sources.list.d/ondrej-*.gpg 2>/dev/null || true; true")
}

// aptUpdateTolerant refreshes package indexes; failures are logged, not
// fatal (broken third-party repos elsewhere must not block PHP installs).
func (e *Executor) aptUpdateTolerant(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.run(c, "apt-get", "update"); err != nil {
		slog.Warn("apt-get update failed after PPA add; continuing with cache", "err", err)
	}
	return nil
}

// runEnv is e.run with extra environment variables.
func (e *Executor) runEnv(ctx context.Context, env []string, name string, args ...string) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", name, strings.Join(args, " "), tail(out, 500), err)
	}
	return nil
}

// aptPackagesVisible reports whether apt's indexes actually contain the
// packages (apt-cache show exits 100 when none resolve).
func (e *Executor) aptPackagesVisible(ctx context.Context, pkgs []string) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append([]string{"show", "-q"}, pkgs...)
	cmd := exec.CommandContext(c, "apt-cache", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-cache show %s: %s (%w)", strings.Join(pkgs, " "), tail(out, 300), err)
	}
	return nil
}

// aptInstall refreshes then installs packages via the detected package
// manager. A failing refresh (broken third-party repo) is tolerated.
func (e *Executor) aptInstall(ctx context.Context, packages []string, runtimeID, version string) error {
	if err := e.pm.Update(ctx); err != nil {
		slog.Warn("package index refresh failed; continuing with cache", "manager", e.pm.Name(), "err", err)
	}
	return e.pm.Install(ctx, packages)
}

func (e *Executor) installPython(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := versionMajorDot(version)
	// Already satisfied (distro python, previous install, or standalone)?
	if _, err := exec.LookPath("python" + major); err == nil {
		if out, err := exec.CommandContext(ctx, "python"+major, "--version").CombinedOutput(); err == nil {
			slog.Info("python already installed", "version", strings.TrimSpace(string(out)))
			p(100, "Python "+major+" already installed")
			return nil
		}
	}
	p(40, "Installing Python "+major+"…")
	// 1. Distro packages (only the distro's own suite has matching deps —
	//    same rule as PHP; python3.12 exists on noble, 3.13 on newer).
	c5, cancel5 := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel5()
	if err := e.run(c5, "apt-get", "install", "-y", "python"+major, "python"+major+"-venv"); err == nil {
		p(100, "Python "+major+" installed")
		return nil
	}
	// 2. deadsnakes PPA — only the suite ppaCodename selects (distro's own,
	//    or the derivative's base-Ubuntu suite; never a blind foreign pin).
	codename, cnErr := e.ppaCodename(ctx, "deadsnakes/ppa")
	if cnErr == nil {
		p(55, "Adding deadsnakes PPA for Python "+major+"…")
		if err := e.addDeadsnakesPPA(ctx, codename); err == nil {
			if err := e.run(c5, "apt-get", "install", "-y", "python"+major, "python"+major+"-venv"); err == nil {
				p(100, "Python "+major+" installed")
				return nil
			}
		}
	}
	// 3. Standalone build (python-build-standalone project: self-contained
	//    CPython, zero system deps) — always installable, wired onto PATH.
	p(60, "Installing standalone Python "+major+" build…")
	return e.installStandalonePython(ctx, major, p)
}

// ppaHasSuiteGeneral probes any Launchpad PPA's dists/ index for a suite.
// ppaPath is the owner/ppa form used in the launchpadcontent URL, e.g.
// "ondrej/php" or "deadsnakes/ppa".
func (e *Executor) ppaHasSuiteGeneral(ctx context.Context, ppaPath, suite string) bool {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c",
		`curl -fsSL --max-time 20 'https://ppa.launchpadcontent.net/`+ppaPath+`/ubuntu/dists/' | grep -q 'href="`+suite+`/"'`)
	return cmd.Run() == nil
}

// installStandalonePython installs python-build-standalone (astral-sh
// releases: self-contained CPython with bundled pip/venv support, zero
// system deps) into /opt/epicpanel/python-standalone/<major>, and wires it
// onto PATH so `python<major>` works for app builds (app_ops.go runs
// `python3.12 -m venv` directly) — field case: apt had no python3.12.
func (e *Executor) installStandalonePython(ctx context.Context, major string, p ProgressFunc) error {
	base := "/opt/epicpanel/python-standalone/" + major
	bin := filepath.Join(base, "bin", "python"+major)
	if _, err := os.Stat(bin); err == nil {
		p(100, "standalone Python "+major+" already installed")
		return nil
	}
	// GitHub asset naming uses x86_64 / aarch64 (NOT go's amd64/arm64) and
	// the no-suffix install_only variant for CPython (verified live asset:
	// cpython-3.12.14+20260901-x86_64-unknown-linux-gnu-install_only.tar.gz).
	arch := "x86_64"
	if goArch() == "arm64" {
		arch = "aarch64"
	}
	// Asset naming (verified live): cpython-3.12.14+20260901-x86_64-
	// unknown-linux-gnu-install_only.tar.gz — the '*' expands over
	// "<patch>+<builddate>". The leading '-' in the suffix keeps the
	// freethreaded ("+x86_64") and *_stripped variants out of the match.
	pattern := "https://github.com/astral-sh/python-build-standalone/releases/latest/download/" +
		"cpython-" + major + "*" + arch + "-unknown-linux-gnu-install_only.tar.gz"
	p(65, "Resolving standalone CPython "+major+" release…")
	resolved, err := e.resolveGitHubGlob(ctx, pattern)
	if err != nil {
		return fmt.Errorf("resolve standalone python release: %w", err)
	}
	tmp := filepath.Join("/tmp", fmt.Sprintf("cpython-%s.tar.gz", major))
	c1, cancel1 := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel1()
	if err := e.run(c1, "curl", "-fsSL", "--retry", "2", "-o", tmp, resolved); err != nil {
		return fmt.Errorf("download %s: %w", resolved, err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	// Tarball layout: install/{bin,lib,...} → strip the install/ prefix.
	if err := e.run(c2, "tar", "-xzf", tmp, "-C", base, "--strip-components=1"); err != nil {
		return fmt.Errorf("extract standalone python: %w", err)
	}
	_ = os.Remove(tmp)
	// Alias python<major>: the tarball ships bin/python3.11 (real binary)
	// plus bin/python3 → python3.11. Only create the alias when the real
	// binary is absent — clobbering it with a symlink to python3 makes a
	// self-referencing loop ("too many levels of symbolic links").
	if fi, err := os.Lstat(bin); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(bin)
		if err := os.Symlink("python3", bin); err != nil {
			return fmt.Errorf("link python%s: %w", major, err)
		}
	}
	_ = e.symlinkIfAbsent(bin, "/usr/local/bin/python"+major)
	// app_ops venvs need pip: ensurepip ships enabled in these builds.
	_ = e.run(ctx, filepath.Join(base, "bin", "python3"), "-m", "ensurepip", "--upgrade")
	if out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("standalone python not functional: %s (%v)", tail([]byte(out), 200), err)
	}
	p(100, "Python "+major+" installed (standalone)")
	return nil
}

// resolveGitHubGlob expands one '*' in a latest-release URL via the GitHub
// API (asset names for the newest release). Pure Go: no python3/jq on the
// box required (the agent must not depend on the thing it is installing).
func (e *Executor) resolveGitHubGlob(ctx context.Context, pattern string) (string, error) {
	star := strings.Index(pattern, "*")
	if star < 0 {
		return pattern, nil
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	// The pattern is a URL; the wildcard match itself runs on asset NAMES,
	// so the prefix part is everything after the last '/'.
	namePrefix := prefix[strings.LastIndex(prefix, "/")+1:]
	apiURL := "https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest"
	client := &http.Client{Timeout: 40 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, namePrefix) && strings.HasSuffix(a.Name, suffix) {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("no release asset matches %s", pattern)
}

// RemoveRuntime removes OS packages for a runtime version. Called only when
// refcount is zero (enforced control-plane side).
func (e *Executor) RemoveRuntime(ctx context.Context, runtimeID, rtType, version string) error {
	switch rtType {
	case "php":
		major := versionMajorDot(version)
		// Static build? Remove its tree + unit + symlinks (idempotent).
		if _, err := os.Stat("/opt/epicpanel/php-static/" + major + "/sbin/php-fpm"); err == nil {
			return e.removeStaticPHP(ctx, major)
		}
		packages := []string{"php" + major + "-fpm", "php" + major + "-cli", "php" + major + "-common"}
		args := append([]string{"purge", "-y"}, packages...)
		if err := e.run(ctx, "apt-get", args...); err != nil {
			return fmt.Errorf("apt-get purge: %w", err)
		}
		// Purging phpX.Y-fpm stops+disables its service; nothing else to do.
		slog.Info("runtime removed", "type", rtType, "version", version)
		return nil
	case "python":
		major := versionMajorDot(version)
		base := "/opt/epicpanel/python-standalone/" + major
		if _, err := os.Stat(base); err == nil {
			if err := e.run(ctx, "rm", "-rf", base); err != nil {
				return err
			}
			_ = os.Remove("/usr/local/bin/python" + major)
		}
		return nil
	case "node":
		// Official tarball layout present? Remove it; distro packages stay
		// managed by apt (purging the panel's own nodejs is not our call).
		entries, _ := os.ReadDir("/usr/local/lib/nodejs")
		if len(entries) > 0 {
			if err := e.run(ctx, "rm", "-rf", "/usr/local/lib/nodejs"); err != nil {
				return err
			}
			for _, name := range []string{"node", "npm", "npx"} {
				_ = os.Remove("/usr/local/bin/" + name)
			}
		}
		return nil
	case "java":
		return e.RemoveJava(ctx, version)
	default:
		return fmt.Errorf("removal for %s not implemented", rtType)
	}
}

// removeStaticPHP tears down a static PHP install: stop+disable the unit,
// remove unit file, config tree, binaries, and compat symlinks.
func (e *Executor) removeStaticPHP(ctx context.Context, major string) error {
	_ = e.run(ctx, "systemctl", "stop", "php"+major+"-fpm")
	_ = e.run(ctx, "systemctl", "disable", "php"+major+"-fpm")
	_ = os.Remove("/etc/systemd/system/php" + major + "-fpm.service")
	_ = e.run(ctx, "systemctl", "daemon-reload")
	_ = os.RemoveAll("/opt/epicpanel/php-static/" + major)
	_ = os.RemoveAll("/etc/php/" + major)
	for _, link := range []string{
		"/usr/local/bin/php-fpm" + major,
		"/usr/local/bin/php-fpm" + strings.ReplaceAll(major, ".", ""),
		"/usr/sbin/php-fpm" + major,
		"/usr/local/bin/php",
	} {
		_ = os.Remove(link)
	}
	slog.Info("static php runtime removed", "major", major)
	return nil
}

func (e *Executor) verifyPHPFPM(ctx context.Context, major string) error {
	bin := phpFpmBinary(major)
	if _, err := exec.LookPath(bin); err != nil {
		// Some distros name it php-fpm{dotless}.
		alt := "php-fpm" + strings.ReplaceAll(major, ".", "")
		if _, err2 := exec.LookPath(alt); err2 != nil {
			return fmt.Errorf("php-fpm binary %q not found after install", bin)
		}
		bin = alt
	}
	return nil
}

// phpFpmBinary resolves the distro-specific PHP-FPM binary name for a version.
func phpFpmBinary(major string) string {
	return "php-fpm" + major
}

// phpFpmService is the systemd unit name for the version.
func phpFpmService(major string) string {
	return "php" + major + "-fpm"
}

// phpFpmPoolDir is where version-specific pool configs live. The base is
// injectable (phpEtcBase) so Phase 9 enforcement tests can fake pools.
var phpEtcBase = "/etc/php"

func phpFpmPoolDir(major string) string {
	return phpEtcBase + "/" + major + "/fpm/pool.d"
}

func versionMajor(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) != 2 {
		return ""
	}
	for _, p := range parts {
		if p == "" {
			return ""
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return ""
			}
		}
	}
	return version
}

func versionMajorDot(version string) string {
	return versionMajor(version)
}

func (e *Executor) run(ctx context.Context, name string, args ...string) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", name, strings.Join(args, " "), tail(out, 500), err)
	}
	return nil
}

func tail(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// installStaticPHP installs a self-contained PHP build (static-php-cli
// project: php + fpm + common extensions compiled into static binaries, no
// system library dependencies) — the last-resort path when neither distro
// repos nor the ondrej PPA can resolve dependencies (field case: a newer
// distro where the PPA's packages deadlock on libxml2 Depends).
//
// Verified upstream layout (dl.static-php.dev/static-php-cli/common/):
// the cli and fpm bundles are SEPARATE files, each a single flat binary:
//
//	php-8.3.9-cli-linux-x86_64.tar.gz  → ./php
//	php-8.3.9-fpm-linux-x86_64.tar.gz  → ./php-fpm
//
// Install layout:
//
//	/opt/epicpanel/php-static/<major>/bin/php        (cli)
//	/opt/epicpanel/php-static/<major>/sbin/php-fpm   (fpm master)
//	/etc/php/<major>/fpm/php-fpm.conf + pool.d/      (shared pool machinery)
//	/etc/systemd/system/php<major>-fpm.service       (unit name php<major>-fpm)
//
// The fpm.go pool engine hard-requires all three: fpmMainConfig for `-t`
// validation, phpFpmPoolDir for pool files, phpFpmService for reload —
// without the unit + main config, EnsurePool fails even with a good binary.
func (e *Executor) installStaticPHP(ctx context.Context, major string, p ProgressFunc) error {
	base := "/opt/epicpanel/php-static/" + major
	fpmBin := filepath.Join(base, "sbin", "php-fpm")
	if _, err := os.Stat(fpmBin); err == nil {
		p(100, "static PHP "+major+" already installed")
		return nil
	}
	bin := "x86_64"
	if goArch() == "arm64" {
		bin = "aarch64"
	}

	// Resolve the newest published full version for this major (8.3 → 8.3.9).
	p(35, "Resolving static PHP "+major+" build…")
	asset, err := e.resolveStaticPHPAsset(ctx, major, bin)
	if err != nil {
		return err
	}

	// Download + extract. Each tarball holds one flat binary at the root.
	extract := func(url, dest string) error {
		tmp := filepath.Join("/tmp", filepath.Base(url))
		c1, cancel1 := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel1()
		if err := e.run(c1, "curl", "-fsSL", "--retry", "2", "-o", tmp, url); err != nil {
			return fmt.Errorf("download %s: %w", url, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		c2, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel2()
		// Single-file archive: stream the one entry straight to dest
		// (tar auto-detects gzip; -O dumps the sole member to stdout).
		if err := e.run(c2, "bash", "-c",
			"tar -xf "+tmp+" -O --to-stdout > "+dest+" && chmod 0755 "+dest); err != nil {
			return fmt.Errorf("extract %s: %w", url, err)
		}
		_ = os.Remove(tmp)
		return nil
	}
	if err := extract(asset, fpmBin); err != nil {
		return err
	}
	// cli: same version, sibling bundle (best-effort — composer/wp-cli want it).
	cliAsset := strings.Replace(asset, "-fpm-linux-", "-cli-linux-", 1)
	_ = extract(cliAsset, filepath.Join(base, "bin", "php"))
	p(70, "Static PHP "+major+" binaries in place")

	// Shared pool-machinery scaffolding (idempotent).
	mainCfg := fpmMainConfig(major)
	if _, err := os.Stat(mainCfg); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(mainCfg), 0o755)
		_ = os.MkdirAll(phpFpmPoolDir(major), 0o755)
		_ = os.MkdirAll("/var/log/epicpanel", 0o755)
		conf := "[global]\n" +
			"pid = /run/epicpanel/php-fpm-" + major + ".pid\n" +
			"error_log = /var/log/epicpanel/php-fpm-" + major + ".log\n" +
			"daemonize = no\n" +
			"include=" + phpFpmPoolDir(major) + "/*.conf\n"
		if err := AtomicWriteFile(mainCfg, []byte(conf), 0o644); err != nil {
			return fmt.Errorf("write static php-fpm.conf: %w", err)
		}
	}

	// systemd unit so phpFpmService(major) (enable/start/reload) works.
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		unit := "/etc/systemd/system/php" + major + "-fpm.service"
		if _, err := os.Stat(unit); os.IsNotExist(err) {
			content := "[Unit]\n" +
				"Description=PHP " + major + " FastCGI Process Manager (EpicPanel static)\n" +
				"After=network.target\n\n[Service]\n" +
				"Type=simple\n" +
				"ExecStart=" + fpmBin + " --nodaemonize --fpm-config " + mainCfg + "\n" +
				"ExecReload=/bin/kill -USR2 $MAINPID\n" +
				"Restart=on-failure\n\n[Install]\n" +
				"WantedBy=multi-user.target\n"
			if err := AtomicWriteFile(unit, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write static fpm unit: %w", err)
			}
			_ = e.run(ctx, "systemctl", "daemon-reload")
		}
	}

	// Compatibility symlinks: LookPath(php-fpm<major>) for validation/reload
	// and `php` on PATH for composer/wp-cli. Never clobber a real binary.
	_ = e.symlinkIfAbsent(fpmBin, "/usr/local/bin/php-fpm"+major)
	_ = e.symlinkIfAbsent(fpmBin, "/usr/local/bin/php-fpm"+strings.ReplaceAll(major, ".", ""))
	_ = e.symlinkIfAbsent(fpmBin, "/usr/sbin/php-fpm"+major)
	if cli, err := os.Stat(filepath.Join(base, "bin", "php")); err == nil && cli.Mode().IsRegular() {
		_ = e.symlinkIfAbsent(filepath.Join(base, "bin", "php"), "/usr/local/bin/php")
	}

	// Prove the whole chain works the way fpm.go will use it: binary + main
	// config + include dir, exactly the args validateFPMConfig passes.
	if err := ValidateCmd(ctx, 60*time.Second, fpmBin, "--fpm-config", mainCfg, "--test"); err != nil {
		return fmt.Errorf("static php-fpm config test failed: %w", err)
	}
	if out, err := exec.CommandContext(ctx, fpmBin, "-v").CombinedOutput(); err != nil ||
		!strings.Contains(string(out), major) {
		return fmt.Errorf("static php-fpm not functional: %s (%v)", tail([]byte(out), 200), err)
	}
	slog.Info("static PHP installed", "major", major, "asset", filepath.Base(asset))
	return nil
}

// symlinkIfAbsent creates link → target only when link does not exist
// (dangling links count as absent: os.Stat follows symlinks).
func (e *Executor) symlinkIfAbsent(target, link string) error {
	if _, err := os.Stat(link); err == nil {
		return nil
	}
	_ = os.Remove(link)
	return os.Symlink(target, link)
}

// resolveStaticPHPAsset picks the newest published fpm tarball for a
// major.minor by scraping the static-php-cli download index. Live-verified
// listing entries: php-8.3.9-fpm-linux-x86_64.tar.gz (cli/fpm/micro flavors,
// x86_64 + aarch64). sort -V handles 8.3.9 < 8.3.10 correctly.
func (e *Executor) resolveStaticPHPAsset(ctx context.Context, major, bin string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	script := "curl -fsSL --max-time 40 'https://dl.static-php.dev/static-php-cli/common/' | " +
		"grep -oE 'php-" + major + "\\.[0-9]+-fpm-linux-" + bin + "\\.tar\\.gz' | sort -uV | tail -1"
	cmd := exec.CommandContext(c, "bash", "-c", script)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("fetch static-php-cli index: %w", err)
	}
	asset := strings.TrimSpace(string(out))
	if !strings.HasPrefix(asset, "php-") || !strings.HasSuffix(asset, ".tar.gz") {
		return "", fmt.Errorf("no static-php-cli fpm build published for %s (%s)", major, bin)
	}
	return "https://dl.static-php.dev/static-php-cli/common/" + asset, nil
}

// installNode installs Node.js via NodeSource. The setup script itself runs
// apt-get update which may fail on broken third-party repos — tolerate that
// (the script still provisions the repo), then install nodejs.
func (e *Executor) installNode(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := nodeMajor(version)
	if major == "" {
		return fmt.Errorf("invalid Node.js version %q (want major, e.g. 22)", version)
	}
	// Node versions in the registry are single-major ("22", "20"); the DB
	// check constraint wants major.minor, so display version is <major>.0.
	if _, err := exec.LookPath("node"); err == nil {
		out, _ := exec.CommandContext(ctx, "node", "--version").Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), "v"+major+".") {
			slog.Info("node already installed", "version", string(out))
			p(100, "Node.js "+major+" already installed")
			return nil
		}
	}
	p(30, "Adding the NodeSource repository for Node "+major+"…")
	if err := e.run(ctx, "bash", "-c",
		fmt.Sprintf("curl -fsSL https://deb.nodesource.com/setup_%s.x | bash - || true", major)); err != nil {
		return fmt.Errorf("nodesource setup: %w", err)
	}
	p(70, "Installing Node.js "+major+"…")
	if err := e.aptInstall(ctx, []string{"nodejs"}, runtimeID, version); err != nil {
		// NodeSource publishes per-distro-suite repos; on brand-new distro
		// releases the suite is missing and apt install fails ("Unable to
		// locate package nodejs"). Fall back to the official prebuilt
		// binaries from nodejs.org — always published, zero repo needed.
		slog.Warn("nodesource install failed; falling back to official nodejs.org binaries", "err", err)
		p(75, "Installing official Node.js "+major+" binaries…")
		if tarErr := e.installNodeTarball(ctx, major, p); tarErr != nil {
			return fmt.Errorf("nodesource install: %v; official tarball: %w", err, tarErr)
		}
	}
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node binary missing after install")
	}
	p(100, "Node.js "+major+" installed")
	return nil
}

// installNodeTarball installs the official prebuilt Node.js binaries
// (nodejs.org/dist/latest-v<major>.x) into /usr/local/lib/nodejs and symlinks
// node/npm/npx onto PATH. Used when NodeSource does not publish a repo for
// the running distro suite.
func (e *Executor) installNodeTarball(ctx context.Context, major string, p ProgressFunc) error {
	// nodejs.org asset naming: node-v22.23.2-linux-x64.tar.xz (x64 | arm64).
	arch := "x64"
	if goArch() == "arm64" {
		arch = "arm64"
	}
	p(78, "Resolving latest Node.js "+major+" release…")
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c",
		"curl -fsSL --max-time 40 'https://nodejs.org/dist/latest-v"+major+".x/' | grep -oE 'node-v"+major+
			"\\.[0-9]+\\.[0-9]+-linux-"+arch+"\\.tar\\.xz' | head -1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list nodejs.org releases: %w", err)
	}
	file := strings.TrimSpace(string(out))
	if !strings.HasPrefix(file, "node-v") || !strings.HasSuffix(file, ".tar.xz") {
		return fmt.Errorf("no official Node.js %s build published for %s", major, arch)
	}
	url := "https://nodejs.org/dist/latest-v" + major + ".x/" + file
	tmp := filepath.Join("/tmp", file)
	c1, cancel1 := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel1()
	if err := e.run(c1, "curl", "-fsSL", "--retry", "2", "-o", tmp, url); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	dest := "/usr/local/lib/nodejs"
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	// Extracts to node-v<full>-linux-<arch>/ containing bin/{node,npm,npx}.
	if err := e.run(c2, "tar", "-xf", tmp, "-C", dest); err != nil {
		return fmt.Errorf("extract nodejs tarball: %w", err)
	}
	_ = os.Remove(tmp)
	stem := strings.TrimSuffix(file, ".tar.xz")
	root := filepath.Join(dest, stem)
	// Symlink onto PATH; node symlink replaces nothing (this path only runs
	// when node is absent or the wrong major — remove stale link first).
	for _, name := range []string{"node", "npm", "npx"} {
		_ = os.Remove("/usr/local/bin/" + name)
		if err := os.Symlink(filepath.Join(root, "bin", name), "/usr/local/bin/"+name); err != nil {
			return fmt.Errorf("link %s: %w", name, err)
		}
	}
	out2, err := exec.CommandContext(ctx, "/usr/local/bin/node", "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out2)), "v"+major+".") {
		return fmt.Errorf("node tarball not functional: %s (%v)", tail([]byte(out2), 200), err)
	}
	slog.Info("official nodejs tarball installed", "major", major, "file", file)
	return nil
}

// installGo downloads and unpacks the latest patch release of a Go minor
// version (e.g. "1.22" -> go1.22.12) to /usr/local, idempotent per minor.
func (e *Executor) installGo(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := goMajor(version)
	if major == "" {
		return fmt.Errorf("invalid Go version %q (want major.minor, e.g. 1.22)", version)
	}
	// Already satisfied?
	if out, err := exec.CommandContext(ctx, "go", "version").Output(); err == nil {
		if strings.Contains(string(out), "go"+major+".") {
			slog.Info("go already installed", "want", major, "have", strings.TrimSpace(string(out)))
			p(100, "Go "+major+" already installed")
			return nil
		}
	}
	p(25, "Resolving latest Go "+major+" patch release…")
	full, err := latestGoVersion(major)
	if err != nil {
		return fmt.Errorf("resolve go version: %w", err)
	}
	tarURL := fmt.Sprintf("https://go.dev/dl/go%s.linux-%s.tar.gz", full, goArch())
	tarFile := filepath.Join("/tmp", fmt.Sprintf("go%s.linux-%s.tar.gz", full, goArch()))
	if err := e.run(ctx, "curl", "-fsSL", "-o", tarFile, tarURL); err != nil {
		return fmt.Errorf("download go: %w", err)
	}
	dest := "/usr/local/go-" + full
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := e.run(ctx, "tar", "-C", "/usr/local", "-xzf", tarFile); err != nil {
			return fmt.Errorf("extract go: %w", err)
		}
		// tar extracts to /usr/local/go; rename to versioned dir.
		if err := os.Rename("/usr/local/go", dest); err != nil {
			return fmt.Errorf("version go dir: %w", err)
		}
	}
	_ = os.Remove(tarFile)
	// Symlinks into the versioned dir (idempotent).
	_ = os.Symlink(dest+"/bin/go", "/usr/local/bin/go")
	_ = os.Symlink(dest+"/bin/gofmt", "/usr/local/bin/gofmt")
	return nil
}

// latestGoVersion asks go.dev for the newest patch of a minor line.
func latestGoVersion(minor string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get("https://go.dev/dl/?mode=json&include=all")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}
	want := "go" + minor + "."
	var best string
	for _, r := range releases {
		if strings.HasPrefix(r.Version, want) && r.Version > best {
			best = r.Version
		}
	}
	if best == "" {
		return "", fmt.Errorf("no Go release found for %s", minor)
	}
	return strings.TrimPrefix(best, "go"), nil
}

func goArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// InstallDatabaseTools deploys phpMyAdmin + Adminer behind a dedicated nginx
// vhost on port 8081, served through the panel's PHP-FPM runtime.
func (e *Executor) InstallDatabaseTools(ctx context.Context) (ip string, ssoKey string, err error) {
	// PHP-FPM is required to serve both tools.
	if err := e.InstallRuntime(ctx, "", "php", latestInstalledPHPMinor(e)); err != nil {
		// no PHP at all: install the default managed version
		if err := e.InstallRuntime(ctx, "", "php", "8.3"); err != nil {
			return "", "", fmt.Errorf("php runtime for db tools: %w", err)
		}
	}
	minor := latestInstalledPHPMinor(e)
	_ = e.EnsureComposer(ctx) // best-effort: composer ships with the PHP stack

	// Web root + downloads.
	root := "/srv/epicpanel/dbadmin"
	_ = os.MkdirAll(root, 0o755)
	adminerFile := filepath.Join(root, "adminer.php")
	if _, err := os.Stat(adminerFile); os.IsNotExist(err) {
		if err := e.run(ctx, "curl", "-fsSL", "-o", adminerFile, "https://www.adminer.org/latest.php"); err != nil {
			return "", "", fmt.Errorf("download adminer: %w", err)
		}
	}
	pmaDir := filepath.Join(root, "phpmyadmin")
	if _, err := os.Stat(filepath.Join(pmaDir, "index.php")); os.IsNotExist(err) {
		tarURL := "https://files.phpmyadmin.net/phpMyAdmin/5.2.2/phpMyAdmin-5.2.2-all-languages.tar.gz"
		tarFile := "/tmp/phpmyadmin.tar.gz"
		if err := e.run(ctx, "curl", "-fsSL", "-o", tarFile, tarURL); err != nil {
			return "", "", fmt.Errorf("download phpmyadmin: %w", err)
		}
		_ = os.MkdirAll(pmaDir, 0o755)
		if err := e.run(ctx, "tar", "-xzf", tarFile, "-C", pmaDir, "--strip-components=1"); err != nil {
			return "", "", fmt.Errorf("extract phpmyadmin: %w", err)
		}
		_ = os.Remove(tarFile)
		// Allow login without a blowfish secret warning blocking usage.
		_ = os.WriteFile(filepath.Join(pmaDir, "config.secret.inc.php"), []byte("<?php\n$cfg['blowfish_secret'] = 'epicpanel-default-secret-32ch!';\n"), 0o640)
	}

	// SSO key + phpMyAdmin signon mode: the panel mints one-time encrypted
	// tokens; the shim below decrypts them with this key and starts a signon
	// session — the user never types a password.
	ssoKeyPath := "/etc/epicpanel/dbadmin-sso.key"
	ssoKeyHex := ""
	if b, err := os.ReadFile(ssoKeyPath); err == nil && len(strings.TrimSpace(string(b))) == 64 {
		ssoKeyHex = strings.TrimSpace(string(b))
	} else {
		rb := make([]byte, 32)
		if _, err := rand.Read(rb); err != nil {
			return "", "", err
		}
		ssoKeyHex = hex.EncodeToString(rb)
		if err := os.MkdirAll(filepath.Dir(ssoKeyPath), 0o700); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(ssoKeyPath, []byte(ssoKeyHex+"\n"), 0o640); err != nil {
			return "", "", err
		}
	}
	// The shim runs in the dbadmin FPM pool (www-data) — make it readable.
	_ = e.run(ctx, "chgrp", "www-data", ssoKeyPath)
	_ = e.run(ctx, "chmod", "g+r", ssoKeyPath)

	if err := os.WriteFile(filepath.Join(pmaDir, "config.inc.php"), []byte(pmaSignonConfig), 0o644); err != nil {
		return "", "", err
	}
	// Remove the Debian-style file if present: PMA does not auto-load it and
	// we ship our own full config.inc.php.
	_ = os.Remove(filepath.Join(pmaDir, "config.user.inc.php"))
	// The shim is executed by the dbadmin FPM pool (www-data) — keep it
	// world-readable (no secrets inside; the key stays root+www-data only).
	if err := os.WriteFile(filepath.Join(pmaDir, "epicpanel-sso.php"), []byte(pmaSignonShim), 0o644); err != nil {
		return "", "", err
	}

	// Dedicated FPM pool for the tools (root-owned files, runs as www-data):
	// rendered + validated via the shared pool engine (PoolSpec/EnsurePool).
	// OpenBaseDir "-" omits the clamp: the tools need the distro session
	// save_path, which sits outside the site tree.
	poolSpec := PoolSpec{
		WebsiteID:      dbadminPoolSiteID,
		UnixUser:       "www-data",
		RuntimeVer:     minor,
		DocumentRoot:   root,
		PrimaryDomain:  "_",
		PMMaxChildren:  10,
		PMStartServers: 2,
		PMMinSpare:     1,
		PMMaxSpare:     3,
		ProcessMemory:  "256M",
		OpenBaseDir:    "-",
	}
	if err := e.EnsurePool(ctx, poolSpec); err != nil {
		return "", "", err
	}
	_ = os.MkdirAll(fpmSocketDir, 0o755)
	// The reserved pool renders the standard per-site error_log path; make
	// sure it exists so worker warnings are never silently dropped.
	_ = os.MkdirAll("/srv/epicpanel/websites/dbadmin/logs", 0o755)

	// nginx vhost on 8081 serving both tools: rendered by the shared
	// dbadmin-vhost renderer, swapped + validated via the shared pipeline.
	if err := ensureDbadminVhost(ctx, root, pmaDir); err != nil {
		return "", "", err
	}
	if err := reloadNginx(ctx); err != nil {
		return "", "", err
	}
	if err := e.run(ctx, "systemctl", "reload", phpFpmService(minor)); err != nil {
		slog.Warn("fpm reload after dbadmin install", "err", err)
	}

	detectedIP := firstNonLoopbackIP()
	slog.Info("database tools installed", "ip", detectedIP, "sso", true)
	return detectedIP, ssoKeyHex, nil
}

// renderDbadminVhost produces the dbadmin nginx vhost (port 8081, serverless
// pool socket, both tools served).
func renderDbadminVhost(root, pmaDir string) string {
	return fmt.Sprintf(`server {
	listen 8081;
	server_name _;

	root %s;
	index index.php index.html;

	location /phpmyadmin {
		alias %s;
		location ~ ^/phpmyadmin/(.*\.php)$ {
			fastcgi_pass unix:%s/dbadmin.sock;
			include snippets/fastcgi-php.conf;
			fastcgi_param SCRIPT_FILENAME %s/$1;
		}
	}

	location /adminer.php {
		fastcgi_pass unix:%s/dbadmin.sock;
		include snippets/fastcgi-php.conf;
		fastcgi_param SCRIPT_FILENAME %s/adminer.php;
	}

	location ~ /\. {
		deny all;
	}
}
`, root, pmaDir, fpmSocketDir, pmaDir, fpmSocketDir, root)
}

// ensureDbadminVhost swaps + validates the dbadmin vhost via the shared
// pipeline (identical content short-circuits; restore-then-error preserved).
func ensureDbadminVhost(ctx context.Context, root, pmaDir string) error {
	avail := filepath.Join(nginxAvailable, "epicpanel-dbadmin.conf")
	enabled := filepath.Join(nginxEnabled, "epicpanel-dbadmin.conf")
	content := renderDbadminVhost(root, pmaDir)
	if err := SwapValidated(avail, []byte(content), 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "nginx", "-t")
	}); err != nil {
		return fmt.Errorf("nginx validation failed: %w", err)
	}
	if err := enableSiteFile(enabled, avail); err != nil {
		return err
	}
	return nil
}

// DBToolsOutcome is the install_database_tools job result: tool IP plus the
// hex SSO key so the control plane can mint one-time phpMyAdmin tokens.
type DBToolsOutcome struct {
	IP     string `json:"ip"`
	SSOKey string `json:"sso_key,omitempty"`
}

// pmaSignonConfig is the full phpMyAdmin config: signon auth driven by our shim.
const pmaSignonConfig = `<?php
// EpicPanel managed configuration — one-time SSO via epicpanel-sso.php.
$i = 0;
$i++;
$cfg['Servers'][$i]['auth_type'] = 'signon';
$cfg['Servers'][$i]['SignonSession'] = 'SignonSession';
$cfg['Servers'][$i]['SignonURL'] = 'epicpanel-sso.php';
$cfg['Servers'][$i]['LogoutURL'] = 'epicpanel-sso.php?logout=1';
$cfg['Servers'][$i]['host'] = 'localhost';
$cfg['Servers'][$i]['AllowNoPassword'] = false;
$cfg['Servers'][$i]['hide_db'] = '(information_schema|mysql|performance_schema|sys)';
$cfg['ServerDefault'] = 1;
$cfg['PmaNoRelation_DisableWarning'] = true;
`

// pmaSignonShim validates one-time EpicPanel tokens and starts a phpMyAdmin
// signon session. Token layout: base64url(nonce(12) | AES-256-GCM ct|tag) of
// JSON {engine, database, user, password, exp}; key in dbadmin-sso.key.
const pmaSignonShim = `<?php
declare(strict_types=1);
error_reporting(0);

function epic_fail(string $msg): void {
    http_response_code(401);
    echo '<!doctype html><meta charset="utf-8"><body style="font-family:system-ui;background:#0f172a;color:#e2e8f0;display:flex;align-items:center;justify-content:center;height:100vh"><div style="text-align:center"><div style="font-size:15px;font-weight:600">' . htmlspecialchars($msg) . '</div><div style="font-size:12px;opacity:.6;margin-top:6px">Close this tab and open the database again from EpicPanel.</div></div></body>';
    exit;
}

$keyHex = @file_get_contents('/etc/epicpanel/dbadmin-sso.key');
if ($keyHex === false) epic_fail('SSO is not configured');
$key = @hex2bin(trim($keyHex));
if ($key === false || strlen($key) !== 32) epic_fail('SSO key invalid');

$token = $_GET['t'] ?? '';
if ($token === '') epic_fail('Missing token');
$raw = @base64_decode(strtr($token, '-_', '+/'), true);
if ($raw === false || strlen($raw) < 25) epic_fail('Invalid token');

// single-use: consumed tokens are remembered for 5 minutes
$seen = sys_get_temp_dir() . '/epicpanel-sso-seen';
$used = @file_get_contents($seen) ?: '';
$now = time();
$lines = array_filter(explode("\n", $used), function ($l) use ($now) {
    $p = explode(' ', $l);
    return count($p) === 2 && (int)$p[1] > $now - 300;
});
if (in_array(hash('sha256', $token), $lines, true)) epic_fail('This one-time login link was already used');
@file_put_contents($seen, implode("\n", $lines) . "\n" . hash('sha256', $token) . ' ' . $now, LOCK_EX);

$nonce = substr($raw, 0, 12);
$tag = substr($raw, -16);
$ct = substr($raw, 12, -16);
$plain = openssl_decrypt($ct, 'aes-256-gcm', $key, OPENSSL_RAW_DATA, $nonce, $tag);
if ($plain === false) epic_fail('This one-time login link has expired');
$data = json_decode($plain, true);
if (!is_array($data) || !isset($data['user'], $data['password'], $data['exp']) || $data['exp'] < time()) {
    epic_fail('This one-time login link has expired');
}

if (isset($_GET['logout'])) {
    session_name('SignonSession');
    session_start();
    $_SESSION = [];
    session_destroy();
    header('Location: /phpmyadmin/index.php');
    exit;
}

session_name('SignonSession');
session_start();
$_SESSION['PMA_single_signon_user'] = (string)$data['user'];
$_SESSION['PMA_single_signon_password'] = (string)$data['password'];
$_SESSION['PMA_single_signon_host'] = 'localhost';
if (!empty($data['database']) && $data['database'] !== '') {
    $_SESSION['PMA_single_signon_db'] = (string)$data['database'];
}
header('Location: /phpmyadmin/index.php');
`

func latestInstalledPHPMinor(e *Executor) string {
	matches, _ := filepath.Glob("/etc/php/*/fpm/pool.d")
	sort.Strings(matches)
	// highest version dir wins
	for i := len(matches) - 1; i >= 0; i-- {
		v := filepath.Base(filepath.Dir(filepath.Dir(matches[i])))
		if _, err := exec.LookPath(phpFpmBinary(v)); err == nil {
			return v
		}
	}
	return "8.3"
}

func firstNonLoopbackIP() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// nodeMajor accepts "22" or "22.4".
func nodeMajor(version string) string {
	first := strings.Split(version, ".")[0]
	for _, c := range first {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if first == "" {
		return ""
	}
	return first
}

// goMajor accepts "1.22" or a full "1.22.5".
func goMajor(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts[:2] {
		if p == "" {
			return ""
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return ""
			}
		}
	}
	return parts[0] + "." + parts[1]
}
