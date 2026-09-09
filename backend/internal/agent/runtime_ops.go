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
		// Distro repos may not carry this version; try the Ondřej PPA (Debian/Ubuntu).
		p(15, "Version not in distro repos — adding ondrej/php PPA…")
		slog.Warn("distro install failed, trying PPA fallback", "err", err)
		if err := e.addOndrejPPA(ctx); err != nil {
			return fmt.Errorf("add PPA: %w (original install error: %v)", err, err)
		}
		// The index refresh is deliberately tolerant of unrelated broken repos
		// — which means a poisoned PPA entry only surfaces here. Verify apt can
		// actually SEE the core packages before retrying (field case: "Unable
		// to locate package php8.3-fpm" after a silently failed update).
		if err := e.aptPackagesVisible(ctx, core); err != nil {
			return fmt.Errorf("PPA added but PHP %s packages are not visible to apt — indexes did not land: %w", major, err)
		}
		if err := install(); err != nil {
			return err
		}
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

// addOndrejPPA enables ppa:ondrej/php.
//
// Field-observed failure modes, all handled here:
//  1. `add-apt-repository ppa:ondrej/php` hangs forever: it fetches the GPG
//     key over hkp://keyserver.ubuntu.com:11371 — a port many cloud networks
//     block — with no timeout. → hard timeouts + noninteractive env.
//  2. Very new distro releases (e.g. codename "resolute") are not published
//     on the PPA yet → apt 404 "does not have a Release file", which then
//     poisons EVERY later apt-get update. → probe Launchpad for supported
//     suites and fall back to the nearest one the PPA actually has (e.g. the
//     previous LTS), with a warning.
//  3. Leftover broken PPA list files from a previous attempt. → removed
//     before writing ours (the fingerprint is fetched from Launchpad's API
//     over HTTPS — no keyserver, no hkp, no hardcoded key material).
func (e *Executor) addOndrejPPA(ctx context.Context) error {
	// Clean ALL previous ondrej state FIRST (stale 404 entries and
	// conflicting Signed-By keyrings are the field killers) — and never
	// after a write, so our own result is never pruned.
	if err := e.pruneOndrejSources(ctx); err != nil {
		return err
	}

	codename, err := e.distroCodename(ctx)
	if err != nil {
		return err
	}

	// Decide the suite up front: the PPA's real dists/ index is the source
	// of truth (resolute absent, noble/jammy published — verified live).
	// Never let add-apt-repository create an entry for an unpublished suite
	// (it does not validate; the 404 then poisons every later apt update).
	suite := codename
	if !e.ppaHasSuite(ctx, suite) {
		suite = ""
		for _, candidate := range []string{"noble", "jammy", "focal", "devel"} {
			if e.ppaHasSuite(ctx, candidate) {
				suite = candidate
				break
			}
		}
		if suite == "" {
			return fmt.Errorf("ppa:ondrej/php publishes no usable suite for %q (tried noble/jammy/focal/devel)", codename)
		}
		slog.Warn("PPA has no suite for this distro release; pinning nearest supported suite",
			"distro", codename, "using", suite)
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

	// Write the single canonical sources entry for the chosen suite.
	c2, cancel2 := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"echo 'deb [signed-by=/etc/apt/keyrings/ondrej-php.gpg] https://ppa.launchpadcontent.net/ondrej/php/ubuntu "+suite+" main' > /etc/apt/sources.list.d/ondrej-php.list"); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
}

// AddOndrejPPATest exposes the PPA flow for integration testing.
func (e *Executor) AddOndrejPPATest(ctx context.Context) error {
	return e.addOndrejPPA(ctx)
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

// ppaHasSuite probes the PPA's actual repository index (dists/) for a
// published suite — the authoritative source (the Launchpad API JSON does
// not list suites; the repo directory listing does).
func (e *Executor) ppaHasSuite(ctx context.Context, suite string) bool {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "bash", "-c",
		`curl -fsSL --max-time 20 'https://ppa.launchpadcontent.net/ondrej/php/ubuntu/dists/' | grep -q 'href="`+suite+`/"'`)
	return cmd.Run() == nil
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
	p(40, "Installing Python "+major+"…")
	if err := e.run(ctx, "apt-get", "install", "-y", "python"+major, "python"+major+"-venv"); err != nil {
		return err
	}
	p(100, "Python "+major+" installed")
	return nil
}

// RemoveRuntime removes OS packages for a runtime version (PHP only for now).
// Called only when refcount is zero (enforced control-plane side).
func (e *Executor) RemoveRuntime(ctx context.Context, runtimeID, rtType, version string) error {
	if rtType != "php" {
		return fmt.Errorf("removal for %s not implemented", rtType)
	}
	major := versionMajorDot(version)
	packages := []string{"php" + major + "-fpm", "php" + major + "-cli", "php" + major + "-common"}
	args := append([]string{"purge", "-y"}, packages...)
	if err := e.run(ctx, "apt-get", args...); err != nil {
		return fmt.Errorf("apt-get purge: %w", err)
	}
	// Purging phpX.Y-fpm stops+disables its service; nothing else to do.
	slog.Info("runtime removed", "type", rtType, "version", version)
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
		return err
	}
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node binary missing after install")
	}
	p(100, "Node.js "+major+" installed")
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
