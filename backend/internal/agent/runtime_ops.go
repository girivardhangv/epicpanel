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
	"os/user"
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
	case "phpmyadmin":
		return e.installPhpMyAdmin(ctx, p)
	case "adminer":
		return e.installAdminer(ctx, p)
	case "redis":
		return e.installRedis(ctx, runtimeID, version, p)
	default:
		return fmt.Errorf("unsupported runtime type %q", rtType)
	}
}

func (e *Executor) installPhpMyAdmin(ctx context.Context, p ProgressFunc) error {
	dest := "/opt/epicpanel/phpmyadmin"
	if _, err := os.Stat(dest); err == nil {
		p(100, "phpMyAdmin already installed")
		return nil
	}
	p(20, "Downloading phpMyAdmin…")
	tmp := "/tmp/phpmyadmin.tar.gz"
	// Download latest english version
	if err := e.downloadFile(ctx, "https://www.phpmyadmin.net/downloads/phpMyAdmin-latest-english.tar.gz", tmp, 5*time.Minute); err != nil {
		return err
	}
	p(60, "Extracting phpMyAdmin…")
	_ = os.MkdirAll("/opt/epicpanel", 0755)
	if err := e.run(ctx, "tar", "-xzf", tmp, "-C", "/opt/epicpanel"); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	// Rename extracted folder
	_ = e.run(ctx, "bash", "-c", "mv /opt/epicpanel/phpMyAdmin-*-english " + dest)
	p(100, "phpMyAdmin installed")
	return nil
}

func (e *Executor) installAdminer(ctx context.Context, p ProgressFunc) error {
	dest := "/opt/epicpanel/adminer"
	if _, err := os.Stat(dest); err == nil {
		p(100, "Adminer already installed")
		return nil
	}
	p(20, "Downloading Adminer…")
	_ = os.MkdirAll(dest, 0755)
	// adminer.org/latest.php 302-redirects to the current versioned file;
	// GitHub release asset names changed with Adminer 5 (caught live: the
	// old asset 404s).
	if err := e.run(ctx, "curl", "-fsSL", "-o", dest+"/adminer.php", "https://www.adminer.org/latest.php"); err != nil {
		return err
	}
	p(100, "Adminer installed")
	return nil
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
	_ = e.run(ctx, "a2dissite", "000-default")
	_ = os.WriteFile("/etc/apache2/ports.conf", []byte("# managed by EpicPanel — do not edit\n"), 0o644)
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
	if err := e.run(ctx, "sh", "-c", "curl -fsSL https://rpms.litespeedtech.com/debian/lst_repo.gpg | gpg --yes --dearmor -o "+key); err != nil {
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
	// RPM distros ship ONE unversioned PHP (`php-fpm` binary, /etc/php-fpm.d
	// pools) — when its major matches the request it satisfies us. A mismatch
	// cannot be honored from distro repos at all, so the non-apt branch below
	// goes straight to the managed static build (versioned binary, pools and
	// unit on any distro).
	if e.pm.Name() != "apt" {
		if out, err := exec.CommandContext(ctx, "php-fpm", "-v").CombinedOutput(); err == nil &&
			strings.Contains(string(out), "PHP "+major+".") {
			slog.Info("distro php already installed", "version", major)
			p(100, "PHP "+major+" already installed (distro)")
			return nil
		}
		// No matching distro PHP on this RPM family — compile from source
		// (php.net), same as the apt path's source fallback.
		p(30, "Compiling PHP "+major+" from source…")
		if err := e.installPHPSource(ctx, major, p); err != nil {
			return fmt.Errorf("source PHP install: %w", err)
		}
		if err := e.EnsureComposer(ctx); err != nil {
			slog.Warn("composer install failed; skipping", "err", err)
		}
		p(100, "PHP "+major+" ready (compiled from source)")
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
		//  1. the distro's version repository — but ONLY when it publishes
		//     THIS distro's own suite (native builds; deps resolve). Ubuntu
		//     and derivatives: ondrej/php. Debian: deb.sury.org/php (the
		//     Debian PHP authority; the ondrej PPA never publishes Debian
		//     suites). Field-proven: pinning foreign suites deadlocks on
		//     Depends (libxml2 et al).
		//  2. otherwise: static PHP build (self-contained binary, zero system
		//     library dependencies) — always installable.
		p(15, "Version not in distro repos — trying the distro version repository…")
		slog.Warn("distro install failed, trying version repo fallback", "err", err)
		distroErr := err
		var repoErr error
		if osReleaseID() == "debian" {
			repoErr = e.addSuryPHP(ctx) // skips cleanly when suite not published
		} else {
			repoErr = e.addOndrejPPA(ctx) // skips cleanly when suite not published
		}
		if repoErr == nil {
			if verr := e.aptPackagesVisible(ctx, core); verr == nil {
				if ierr := install(); ierr == nil {
					if err := e.verifyPHPFPM(ctx, major); err != nil {
						return err
					}
					p(100, "PHP "+major+" ready (FPM + extensions)")
					return nil
				} else {
					slog.Warn("repo packages visible but install failed; falling to static build", "err", ierr)
				}
			} else {
				slog.Warn("repo packages not visible; falling to static build", "err", verr)
			}
		} else {
			slog.Warn("version repo path unavailable for this distro; compiling from source", "err", repoErr)
		}
	// Neither distro nor a version repository ships this version — compile
	// it from php.net source (full extension set, pecl/phpize included).
	p(30, "Compiling PHP "+major+" from source…")
	if serr := e.installPHPSource(ctx, major, p); serr != nil {
		return fmt.Errorf("source PHP install: %w (repo: %v; apt: %v)", serr, repoErr, distroErr)
	}
		if err := e.EnsureComposer(ctx); err != nil {
			slog.Warn("composer install failed; skipping", "err", err)
		}
		p(100, "PHP "+major+" ready (compiled from source)")
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
// is missing. Idempotent: an existing Composer short-circuits. Downloads the
// release PHAR directly — the same artifact the one-click installers use —
// instead of running composer-setup.php: the installer carries its own
// extension probes and signature dance and has failed on fresh nodes where
// the PHAR itself runs fine. Verified runnable (`composer --version`) before
// returning, so a deploy fails HERE with a readable reason, not mid-build.
func (e *Executor) EnsureComposer(ctx context.Context) error {
	if _, err := exec.LookPath("composer"); err == nil {
		return nil
	}
	slog.Info("installing composer")
	if _, err := exec.LookPath("php"); err != nil {
		return fmt.Errorf("php cli required for composer")
	}
	if err := e.run(ctx, "curl", "-fsSL", "-o", composerBin, "https://getcomposer.org/composer-stable.phar"); err != nil {
		return fmt.Errorf("download composer: %w", err)
	}
	_ = e.run(ctx, "chmod", "+x", composerBin)
	if out, err := exec.CommandContext(ctx, "php", composerBin, "--version").CombinedOutput(); err != nil {
		_ = os.Remove(composerBin)
		return fmt.Errorf("composer not runnable after install: %s (%w)", tail(out, 200), err)
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

// addSuryPHP enables deb.sury.org/php — the Debian counterpart of the
// ondrej PPA (same maintainer, native Debian builds of every supported PHP
// version). Same safety rules: probe the repo's dists/ index for THIS
// distro's suite and refuse foreign pins (Debian testing/unstable suites are
// not always published; that must fall through to the static build, never
// poison apt). The signing key comes over HTTPS from packages.sury.org and
// lands in a dedicated keyring — no apt-key, no third-party trust.
func (e *Executor) addSuryPHP(ctx context.Context) error {
	// Clean previous sury state first (same poisoning rules as ondrej:
	// stale 404 entries and conflicting Signed-By keyrings break apt).
	c0, cancel0 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel0()
	if err := e.run(c0, "bash", "-c",
		"rm -f /etc/apt/sources.list.d/sury-php*.list /etc/apt/sources.list.d/sury-php*.list.save 2>/dev/null || true; true"); err != nil {
		return err
	}

	codename, err := e.distroCodename(ctx)
	if err != nil {
		return err
	}
	if !suryHasSuite(ctx, codename) {
		slog.Warn("sury.org does not publish this Debian suite; refusing foreign-suite pin",
			"distro", codename)
		return errSuiteUnsupported
	}

	if err := e.run(ctx, "mkdir", "-p", "/etc/apt/keyrings"); err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := e.run(c, "bash", "-c",
		"curl -fsSL --max-time 60 https://packages.sury.org/php/apt.gpg | gpg --batch --yes --no-tty --dearmor -o /etc/apt/keyrings/sury-php.gpg"); err != nil {
		return fmt.Errorf("fetch sury signing key: %w", err)
	}

	c2, cancel2 := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"echo 'deb [signed-by=/etc/apt/keyrings/sury-php.gpg] https://packages.sury.org/php/ "+codename+" main' > /etc/apt/sources.list.d/sury-php.list"); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
}

// suryHasSuite probes packages.sury.org's dists/ index for a published
// suite (bookworm, trixie, …) — the authoritative source, same pattern as
// ppaHasSuiteGeneral.
func suryHasSuite(ctx context.Context, suite string) bool {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c",
		`[ "$(curl -fsSL --max-time 20 -o /dev/null -w '%{http_code}' 'https://packages.sury.org/php/dists/`+suite+`/Release')" = 200 ]`)
	return cmd.Run() == nil
}

// osReleaseID returns the ID field from /etc/os-release ("" when absent).
func osReleaseID() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ID=") {
			return strings.Trim(strings.TrimPrefix(line, "ID="), `"`)
		}
	}
	return ""
}

// staticPHPEnabled reports whether frozen-extension static PHP builds may be
// used as a fallback (EPICPANEL_STATIC_PHP=1). Off by default: a PHP without
// add-on extension support is worse than a clear "not installable here".
func staticPHPEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("EPICPANEL_STATIC_PHP")))
	return v == "1" || v == "true" || v == "yes"
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
	if major == "" && version == "latest" {
		// Legacy catalog payloads carry "latest"; the installer needs a
		// concrete minor (the catalog now offers 3.12/3.13 directly).
		version, major = "3.13", "3.13"
	}
	if major == "" {
		return fmt.Errorf("invalid Python version %q (want major.minor)", version)
	}
	// Already satisfied (distro python, previous install, or standalone)?
	if _, err := exec.LookPath("python" + major); err == nil {
		if out, err := exec.CommandContext(ctx, "python"+major, "--version").CombinedOutput(); err == nil {
			slog.Info("python already installed", "version", strings.TrimSpace(string(out)))
			p(100, "Python "+major+" already installed")
			return nil
		}
	}
	p(40, "Installing Python "+major+"…")
	// 1. Distro packages, via the detected package manager — versioned
	//    names exist on both the apt and dnf families (python3.12 on
	//    noble/el9, 3.13 on newer; only the distro's own suite has matching
	//    deps — same rule as PHP). The venv subpackage is Debian-only
	//    naming: best-effort everywhere else.
	if err := e.aptInstall(ctx, []string{"python" + major}, runtimeID, version); err == nil {
		_ = e.pm.InstallBestEffort(ctx, []string{"python" + major + "-venv"})
		if _, err := exec.LookPath("python" + major); err == nil {
			p(100, "Python "+major+" installed")
			return nil
		}
	}
	// 2. deadsnakes PPA — only the suite ppaCodename selects (distro's own,
	//    or the derivative's base-Ubuntu suite; never a blind foreign pin).
	codename, cnErr := e.ppaCodename(ctx, "deadsnakes/ppa")
	if cnErr == nil {
		p(55, "Adding deadsnakes PPA for Python "+major+"…")
		if err := e.addDeadsnakesPPA(ctx, codename); err == nil {
			c5, cancel5 := context.WithTimeout(ctx, 8*time.Minute)
			defer cancel5()
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
	if err := e.downloadFile(ctx, resolved, tmp, 8*time.Minute); err != nil {
		return err
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
		// Source build? Remove its prefix + unit + symlinks (idempotent).
		if _, err := os.Stat("/opt/epicpanel/php/" + major + "/sbin/php-fpm"); err == nil {
			return e.removePHPSource(ctx, major)
		}
		// Legacy frozen static build? Clean its tree + unit + symlinks.
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
		// Managed per-major tree (new layout), with legacy global-tarball
		// cleanup inside; distro packages stay managed by the package
		// manager (purging the panel's own nodejs is not our call).
		return e.removeNode(ctx, version)
	case "java":
		return e.RemoveJava(ctx, version)
	case "redis":
		return e.removeRedis(ctx)
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
	} {
		_ = os.Remove(link)
	}
	// The shared `php` shim must only die if it points into THIS static
	// tree — it may belong to a source-built or distro PHP now (caught live:
	// removing static 8.3 deleted the shim of source-built 8.4).
	if cur, lerr := os.Readlink("/usr/local/bin/php"); lerr == nil &&
		strings.HasPrefix(cur, "/opt/epicpanel/php-static/"+major+"/") {
		_ = os.Remove("/usr/local/bin/php")
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
	// Functional check, not just presence: a package install can succeed
	// while the binary is broken (wrong arch, missing libs).
	out, err := exec.CommandContext(ctx, bin, "-v").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PHP "+major+".") {
		return fmt.Errorf("php-fpm %s not functional after install: %s (%v)", bin, tail([]byte(out), 200), err)
	}
	return nil
}

// symlinkReplace points link at want, replacing an existing link atomically
// (tmp symlink + rename) so readers never observe a missing binary. No-op
// when the link already targets want.
func symlinkReplace(link, want string) error {
	if cur, lerr := os.Readlink(link); lerr == nil && cur == want {
		return nil
	}
	tmp := link + ".epicpanel-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(want, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// renderStaticDefaultPool is the placeholder pool for managed static PHP
// installs (the distro www.conf equivalent): a minimal dynamic pool so the
// master validates and starts before any site pool exists.
func renderStaticDefaultPool(major string) string {
	owner := "www-data"
	if _, err := user.Lookup(owner); err != nil {
		owner = "nobody" // Alpine and friends ship no www-data
	}
	group := owner
	if _, err := user.LookupGroup(group); err != nil {
		if g, gerr := user.LookupGroup("nogroup"); gerr == nil {
			group = g.Name
		}
	}
	sock := fpmSocketDir + "/default-" + major + ".sock"
	return "[epicpanel-default]\n" +
		"user = " + owner + "\n" +
		"group = " + group + "\n" +
		"listen = " + sock + "\n" +
		"listen.owner = " + owner + "\n" +
		"listen.group = " + group + "\n" +
		"pm = dynamic\n" +
		"pm.max_children = 2\n" +
		"pm.start_servers = 1\n" +
		"pm.min_spare_servers = 1\n" +
		"pm.max_spare_servers = 1\n" +
		"php_admin_value[error_log] = /var/log/epicpanel/php-fpm-" + major + "-default.log\n" +
		"php_admin_flag[log_errors] = on\n"
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

// downloadFile fetches url to dest, first clearing any stale file left by a
// previous failed attempt. On Ubuntu, /tmp is sticky with
// fs.protected_regular: re-opening another user's leftover file fails even
// for root (curl exit 23 "ERROR on write"), which poisoned every retry until
// the temp file was removed by hand (caught live with PHP 8.1). Failures
// remove the partial file too, so no attempt can poison the next one.
func (e *Executor) downloadFile(ctx context.Context, url, dest string, timeout time.Duration) error {
	_ = os.Remove(dest)
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := e.run(c, "curl", "-fsSL", "--retry", "2", "-o", dest, url)
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: %w", url, err)
	}
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


// addNodeSourceRepo provisions the NodeSource apt repository directly — a
// deb822 .sources file with a pinned keyring — instead of piping the
// upstream setup_XX.x script through `bash -c` (that ran remote code with
// root privileges and swallowed failures with `|| true`). NodeSource
// publishes one suite per major ("nodistro"); probing it first means
// unsupported/new distro suites fall through to the official tarball instead
// of poisoning apt with a 404 repo.
func (e *Executor) addNodeSourceRepo(ctx context.Context, major string) error {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe := exec.CommandContext(c, "bash", "-c",
		`[ "$(curl -fsSL --max-time 20 -o /dev/null -w '%{http_code}' 'https://deb.nodesource.com/node_`+major+`.x/dists/nodistro/Release')" = 200 ]`)
	if probe.Run() != nil {
		return fmt.Errorf("nodesource repo unreachable for node %s (nodistro suite)", major)
	}
	// The legacy setup script wrote /etc/apt/sources.list.d/nodesource.list;
	// remove stale copies so apt never sees two competing entries.
	c0, cancel0 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel0()
	if err := e.run(c0, "bash", "-c",
		"rm -f /etc/apt/sources.list.d/nodesource.list /etc/apt/sources.list.d/nodesource.list.save 2>/dev/null || true; true"); err != nil {
		return err
	}
	if err := e.run(ctx, "mkdir", "-p", "/etc/apt/keyrings"); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"curl -fsSL --max-time 60 https://deb.nodesource.com/gpgkey/nodesource.gpg.key | gpg --batch --yes --no-tty --dearmor -o /etc/apt/keyrings/nodesource.gpg"); err != nil {
		return fmt.Errorf("fetch nodesource signing key: %w", err)
	}
	sources := "X-Repolib-Name: NodeSource\n" +
		"Types: deb\n" +
		"URIs: https://deb.nodesource.com/node_" + major + ".x\n" +
		"Suites: nodistro\n" +
		"Components: main\n" +
		"Signed-By: /etc/apt/keyrings/nodesource.gpg\n"
	if err := AtomicWriteFile("/etc/apt/sources.list.d/nodesource.sources", []byte(sources), 0o644); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
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
	dest := "/usr/local/go-" + full
	// Already in place? Skip the download entirely — the dir may predate the
	// panel (root-managed trees survive reinstalls) and re-extracting only to
	// fail the rename burned 70MB per attempt (caught live on go 1.24).
	if _, err := os.Stat(filepath.Join(dest, "bin", "go")); err == nil {
		if err := e.linkGoSymlinks(dest); err != nil {
			return err
		}
		p(100, "Go "+full+" already installed")
		return nil
	}
	tarURL := fmt.Sprintf("https://go.dev/dl/go%s.linux-%s.tar.gz", full, goArch())
	tarFile := filepath.Join("/tmp", fmt.Sprintf("go%s.linux-%s.tar.gz", full, goArch()))
	if err := e.downloadFile(ctx, tarURL, tarFile, 8*time.Minute); err != nil {
		return err
	}
	// A previous crashed attempt can leave a partial /usr/local/go; tar would
	// merge into it and the rename to dest would fail forever after.
	_ = os.RemoveAll("/usr/local/go")
	if err := e.run(ctx, "tar", "-C", "/usr/local", "-xzf", tarFile); err != nil {
		return fmt.Errorf("extract go: %w", err)
	}
	// tar extracts to /usr/local/go; rename to versioned dir. Remove a stale
	// dest first — rename onto an existing non-empty dir fails (caught live).
	_ = os.RemoveAll(dest)
	if err := os.Rename("/usr/local/go", dest); err != nil {
		return fmt.Errorf("version go dir: %w", err)
	}
	_ = os.Remove(tarFile)
	if err := e.linkGoSymlinks(dest); err != nil {
		return err
	}
	// Functional check: binary must run and report the resolved release.
	if out, err := exec.CommandContext(ctx, "go", "version").CombinedOutput(); err != nil ||
		!strings.Contains(string(out), "go"+full+" ") {
		return fmt.Errorf("go not functional after install: %s (%v)", tail([]byte(out), 200), err)
	}
	return nil
}

// linkGoSymlinks points /usr/local/bin/go and gofmt at dest/bin atomically
// (replaces stale/dangling links from older installs or previous minors).
func (e *Executor) linkGoSymlinks(dest string) error {
	for _, bin := range []string{"go", "gofmt"} {
		if err := symlinkReplace("/usr/local/bin/"+bin, dest+"/bin/"+bin); err != nil {
			return fmt.Errorf("link %s: %w", bin, err)
		}
	}
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
// vhost on :8081, served through the panel's PHP-FPM runtime. preferredPHP
// (from the global_php_version setting) wins when that version's FPM binary
// is available; otherwise the highest installed minor is used.
func (e *Executor) InstallDatabaseTools(ctx context.Context, preferredPHP string) (ip string, ssoKey string, err error) {
	// PHP-FPM is required to serve both tools.
	if err := e.InstallRuntime(ctx, "", "php", latestInstalledPHPMinor(e)); err != nil {
		// no PHP at all: install the default managed version
		if err := e.InstallRuntime(ctx, "", "php", "8.3"); err != nil {
			return "", "", fmt.Errorf("php runtime for db tools: %w", err)
		}
	}
	minor := e.resolveDBToolsPHP(preferredPHP)
	_ = e.EnsureComposer(ctx) // best-effort: composer ships with the PHP stack

	// Web root + downloads.
	root := "/srv/epicpanel/dbadmin"
	_ = os.MkdirAll(root, 0o755)
	adminerFile := filepath.Join(root, "adminer.php")
	if _, err := os.Stat(adminerFile); os.IsNotExist(err) {
		if err := e.downloadFile(ctx, "https://www.adminer.org/latest.php", adminerFile, 3*time.Minute); err != nil {
			return "", "", fmt.Errorf("download adminer: %w", err)
		}
	}
	pmaDir := filepath.Join(root, "phpmyadmin")
	if _, err := os.Stat(filepath.Join(pmaDir, "index.php")); os.IsNotExist(err) {
		tarURL := "https://files.phpmyadmin.net/phpMyAdmin/5.2.2/phpMyAdmin-5.2.2-all-languages.tar.gz"
		tarFile := "/tmp/phpmyadmin-dbtools.tar.gz"
		if err := e.downloadFile(ctx, tarURL, tarFile, 5*time.Minute); err != nil {
			return "", "", err
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
		// 0755, not 0700: the shim runs as www-data and must TRAVERSE this
		// directory to reach the key (a fresh 0700 dir made every SSO login
		// fail with "SSO is not configured" — caught live). The key file
		// itself stays root:www-data 0640; agent.env next to it stays 0600.
		if err := os.MkdirAll(filepath.Dir(ssoKeyPath), 0o755); err != nil {
			return "", "", err
		}
		_ = os.Chmod(filepath.Dir(ssoKeyPath), 0o755) // heal pre-existing tight perms
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
	// The pool moved to `minor` — drop the dbadmin pool from any other PHP
	// version dir so two masters never race for the same socket (ADR-016
	// class bug: "another FPM instance already listens on ...sock").
	if err := e.CleanupOtherVersionPools(ctx, minor, dbadminPoolSiteID); err != nil {
		slog.Warn("dbadmin stale pool cleanup", "err", err)
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
// 127.0.0.1 (TCP), never "localhost": host=localhost makes mysqli use the
// socket, whose path differs per PHP build (source builds default to
// /tmp/mysql.sock and fail — caught live as a signon redirect loop).
$cfg['Servers'][$i]['host'] = '127.0.0.1';
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
if ($token === '') epic_fail('Your phpMyAdmin session has expired. Close this tab and open the database again from EpicPanel.');
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

// resolveDBToolsPHP picks the PHP minor for the dbadmin pool: the operator's
// global_php_version setting when that version's FPM binary is actually
// available, else the highest installed minor.
func (e *Executor) resolveDBToolsPHP(preferred string) string {
	if preferred != "" {
		if _, err := exec.LookPath(phpFpmBinary(preferred)); err == nil {
			return preferred
		}
		alt := "php-fpm" + strings.ReplaceAll(preferred, ".", "")
		if _, err := exec.LookPath(alt); err == nil {
			return preferred
		}
		if _, err := os.Stat("/opt/epicpanel/php-static/" + preferred + "/sbin/php-fpm"); err == nil {
			return preferred
		}
		slog.Warn("global php version not installed; falling back to highest", "preferred", preferred)
	}
	return latestInstalledPHPMinor(e)
}

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

