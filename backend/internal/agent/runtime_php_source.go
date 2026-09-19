package agent

// ============================================================================
// PHP compiled from php.net source — the quality path when neither the
// distro nor a version repository (ondrej/sury) ships the requested version.
//
// Why: distro repos lag, PPAs do not cover every distro suite, and frozen
// third-party binaries cannot take add-on extensions. A source build gives
// every version on every distro with the FULL extension set compiled in and
// pecl/phpize available for more.
//
// Layout (per major), integrated with the shared pool machinery:
//
//	/opt/epicpanel/php/<major>/            prefix (bin/php, sbin/php-fpm, ...)
//	/opt/epicpanel/php/<major>/etc/        php.ini home
//	/etc/php/<major>/fpm/php-fpm.conf      shared fpm config (fpmMainConfig)
//	/etc/php/<major>/fpm/pool.d/           shared pool dir (phpFpmPoolDir)
//	/etc/systemd/system/php<major>-fpm.service   (phpFpmService)
//	/usr/local/bin/php-fpm<major>, php<major>, php   compat symlinks
//
// Build deps are installed via the detected package manager. Tarballs are
// cached under /var/cache/epicpanel so re-installs never re-download.
// ============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const phpSourceRoot = "/opt/epicpanel/php"

// phpSourceBuildDeps maps package-manager families to the dev packages the
// configure set below needs.
func phpSourceBuildDeps(pm string) []string {
	switch pm {
	case "apt":
		return []string{
			"build-essential", "pkg-config", "libxml2-dev", "libsqlite3-dev",
			"libcurl4-openssl-dev", "libonig-dev", "libzip-dev", "libpng-dev",
			"libjpeg-dev", "libfreetype-dev", "libssl-dev", "libicu-dev",
			"libpq-dev", "re2c", "bison", "libreadline-dev",
		}
	case "dnf", "yum":
		return []string{
			"gcc", "gcc-c++", "make", "pkgconfig", "libxml2-devel",
			"sqlite-devel", "libcurl-devel", "oniguruma-devel", "libzip-devel",
			"libpng-devel", "libjpeg-turbo-devel", "freetype-devel",
			"openssl-devel", "libicu-devel", "libpq-devel", "readline-devel",
		}
	case "apk":
		return []string{
			"build-base", "pkgconfig", "libxml2-dev", "sqlite-dev", "curl-dev",
			"oniguruma-dev", "libzip-dev", "libpng-dev", "libjpeg-turbo-dev",
			"freetype-dev", "openssl-dev", "icu-dev", "postgresql-dev",
			"readline-dev",
		}
	case "pacman":
		return []string{"base-devel", "libxml2", "sqlite", "curl", "oniguruma", "libzip", "libpng", "libjpeg-turbo", "freetype2", "openssl", "icu", "postgresql-libs"}
	case "zypper":
		return []string{"gcc", "gcc-c++", "make", "pkg-config", "libxml2-devel", "sqlite3-devel", "libcurl-devel", "oniguruma-devel", "libzip-devel", "libpng-devel", "libjpeg-devel", "freetype-devel", "libopenssl-devel", "libicu-devel", "postgresql-devel"}
	default:
		return []string{"gcc", "make"}
	}
}

// phpSourceConfigureFlags is the compile set that satisfies the panel's
// extension catalog (mysql, pgsql, curl, gd, mbstring, xml, zip, intl,
// opcache, soap, bcmath, sockets, sqlite3) with FPM + CLI.
func phpSourceConfigureFlags(prefix string) []string {
	return []string{
		"--prefix=" + prefix,
		"--with-config-file-path=" + prefix + "/etc",
		"--enable-fpm",
		"--with-fpm-user=www-data",
		"--with-fpm-group=www-data",
		"--disable-cgi",
		"--enable-mbstring",
		"--enable-sockets",
		"--enable-soap",
		"--enable-bcmath",
		"--enable-intl",
		"--enable-gd",
		"--with-jpeg",
		"--with-freetype",
		"--with-curl",
		"--with-openssl",
		"--with-zlib",
		"--with-zip",
		"--with-mysqli=mysqlnd",
		"--with-pdo-mysql=mysqlnd",
		"--with-pdo-pgsql",
		"--with-pgsql",
		"--with-pdo-sqlite",
		"--enable-opcache",
		"--enable-pcntl",
		"--enable-calendar",
		"--enable-exif",
		"--with-readline",
	}
}

// runIn is e.run with the command's working directory set (source builds
// run ./configure && make inside the extracted tree). The caller owns the
// timeout via ctx.
func (e *Executor) runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", name, strings.Join(args, " "), tail(out, 500), err)
	}
	return nil
}

// phpSourceLatestPatch resolves the newest patch of a major.minor from
// php.net's release JSON (e.g. 8.3 -> 8.3.14).
func phpSourceLatestPatch(ctx context.Context, major string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := "https://www.php.net/releases/index.php?json&version=" + major
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("php.net release lookup: %s", resp.Status)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Version == "" {
		return "", fmt.Errorf("php.net returned no version for %s", major)
	}
	return payload.Version, nil
}

// installPHPSource compiles the requested PHP major.minor from php.net
// source. Idempotent: an existing prefix short-circuits; the source tarball
// is cached across attempts.
func (e *Executor) installPHPSource(ctx context.Context, major string, p ProgressFunc) error {
	prefix := filepath.Join(phpSourceRoot, major)
	fpmBin := filepath.Join(prefix, "sbin", "php-fpm")
	if _, err := os.Stat(fpmBin); err == nil {
		p(100, "PHP "+major+" (source) already installed")
		return nil
	}

	p(8, "Installing build dependencies…")
	for _, dep := range phpSourceBuildDeps(e.pm.Name()) {
		_ = e.pm.InstallBestEffort(ctx, []string{dep})
	}

	p(15, "Resolving the latest PHP "+major+" release…")
	full, err := phpSourceLatestPatch(ctx, major)
	if err != nil {
		return fmt.Errorf("resolve php release: %w", err)
	}
	if _, err := os.Stat(fpmBin); err == nil { // re-check after slow dep install
		p(100, "PHP "+major+" (source) already installed")
		return nil
	}

	cache := "/var/cache/epicpanel/php-src"
	tarFile := filepath.Join(cache, "php-"+full+".tar.gz")
	if _, err := os.Stat(tarFile); err != nil {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return err
		}
		p(22, "Downloading PHP "+full+" source…")
		url := "https://www.php.net/distributions/php-" + full + ".tar.gz"
		if err := e.downloadFile(ctx, url, tarFile, 8*time.Minute); err != nil {
			return err
		}
	}

	src := filepath.Join(cache, "php-"+full)
	if _, err := os.Stat(filepath.Join(src, "configure")); err != nil {
		p(30, "Extracting source…")
		_ = os.RemoveAll(src)
		if err := e.run(ctx, "tar", "-xzf", tarFile, "-C", cache); err != nil {
			return fmt.Errorf("extract php source: %w", err)
		}
	}

	p(40, "Configuring PHP "+full+"…")
	cfgCtx, cfgCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cfgCancel()
	configure := append([]string{"-C"}, phpSourceConfigureFlags(prefix)...)
	if err := e.runIn(cfgCtx, src, "./configure", configure...); err != nil {
		return fmt.Errorf("configure php: %w", err)
	}

	p(55, "Compiling PHP "+full+" (make -j"+fmt.Sprint(runtime.NumCPU())+") — this takes a while…")
	buildCtx, buildCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer buildCancel()
	if err := e.runIn(buildCtx, src, "make", "-j", fmt.Sprint(runtime.NumCPU())); err != nil {
		return fmt.Errorf("build php: %w", err)
	}
	p(80, "Installing (make install)…")
	if err := e.runIn(ctx, src, "make", "install"); err != nil {
		return fmt.Errorf("make install: %w", err)
	}

	// Shared pool machinery (same conventions as every other install path —
	// fpm.go's validation, pools and service lookups depend on these paths).
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
			return fmt.Errorf("write php-fpm.conf: %w", err)
		}
	}
	poolDir := phpFpmPoolDir(major)
	_ = os.MkdirAll(poolDir, 0o755)
	_ = os.MkdirAll(fpmSocketDir, 0o755)
	if entries, _ := os.ReadDir(poolDir); len(entries) == 0 {
		if err := AtomicWriteFile(filepath.Join(poolDir, "epicpanel-default.conf"),
			[]byte(renderStaticDefaultPool(major)), 0o644); err != nil {
			return fmt.Errorf("write default pool: %w", err)
		}
	}

	// systemd unit (phpFpmService naming — shared with the pool engine).
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		unit := "/etc/systemd/system/php" + major + "-fpm.service"
		if _, err := os.Stat(unit); os.IsNotExist(err) {
			content := "[Unit]\n" +
				"Description=PHP " + major + " FastCGI Process Manager (EpicPanel source)\n" +
				"After=network.target\n\n[Service]\n" +
				"Type=simple\n" +
				"ExecStart=" + fpmBin + " --nodaemonize --fpm-config " + mainCfg + "\n" +
				"ExecReload=/bin/kill -USR2 $MAINPID\n" +
				"Restart=on-failure\n\n[Install]\n" +
				"WantedBy=multi-user.target\n"
			if err := AtomicWriteFile(unit, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write fpm unit: %w", err)
			}
			_ = e.run(ctx, "systemctl", "daemon-reload")
		}
	}

	// php.ini: production baseline with OPcache ON and hosting-sane limits
	// (a source build ships no php.ini, which silently left OPcache off).
	iniPath := filepath.Join(prefix, "etc", "php.ini")
	if _, err := os.Stat(iniPath); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(iniPath), 0o755)
		var ini string
		if b, rerr := os.ReadFile(filepath.Join("/var/cache/epicpanel/php-src", "php-"+full, "php.ini-production")); rerr == nil {
			ini = string(b)
		}
		ini += "\n; -- EpicPanel baseline (appended; last value wins) --\n" +
			"zend_extension=opcache\n" +
			"opcache.enable=1\n" +
			"opcache.enable_cli=0\n" +
			"opcache.memory_consumption=128\n" +
			"opcache.max_accelerated_files=16000\n" +
			"expose_php=Off\n" +
			"memory_limit=256M\n" +
			"upload_max_filesize=64M\n" +
			"post_max_size=64M\n" +
			"max_execution_time=120\n" +
			// Source builds default the MySQL socket to /tmp/mysql.sock, but
			// MariaDB (the panel's engine) listens on the Debian path — an
			// app connecting via "localhost" would get "No such file or
			// directory" (caught live: phpMyAdmin login looped).
			"mysqli.default_socket=/run/mysqld/mysqld.sock\n" +
			"pdo_mysql.default_socket=/run/mysqld/mysqld.sock\n"
		if err := AtomicWriteFile(iniPath, []byte(ini), 0o644); err != nil {
			return fmt.Errorf("write php.ini: %w", err)
		}
	}

	// PATH shims.
	_ = e.symlinkIfAbsent(fpmBin, "/usr/local/bin/php-fpm"+major)
	_ = e.symlinkIfAbsent(fpmBin, "/usr/local/bin/php-fpm"+strings.ReplaceAll(major, ".", ""))
	_ = e.symlinkIfAbsent(filepath.Join(prefix, "bin", "php"), "/usr/local/bin/php"+major)
	if cli, err := os.Stat(filepath.Join(prefix, "bin", "php")); err == nil && cli.Mode().IsRegular() {
		if err := symlinkReplace("/usr/local/bin/php", filepath.Join(prefix, "bin", "php")); err != nil {
			slog.Warn("php PATH shim skipped", "err", err)
		}
	}

	// Functional verify: CLI and FPM must run and match the requested major.
	if out, err := exec.CommandContext(ctx, filepath.Join(prefix, "bin", "php"), "-v").CombinedOutput(); err != nil ||
		!strings.Contains(string(out), "PHP "+major+".") {
		return fmt.Errorf("php cli not functional after source install: %s (%v)", tail([]byte(out), 200), err)
	}
	if err := ValidateCmd(ctx, 60*time.Second, fpmBin, "--fpm-config", mainCfg, "--test"); err != nil {
		return fmt.Errorf("source php-fpm config test failed: %w", err)
	}
	slog.Info("php installed from source", "major", major, "full", full, "prefix", prefix)
	return nil
}

// removePHPSource tears down a source-built PHP prefix + unit + symlinks.
func (e *Executor) removePHPSource(ctx context.Context, major string) error {
	prefix := filepath.Join(phpSourceRoot, major)
	if _, err := os.Stat(prefix); err == nil {
		_ = e.run(ctx, "systemctl", "stop", phpFpmService(major))
		_ = e.run(ctx, "systemctl", "disable", phpFpmService(major))
		_ = os.Remove("/etc/systemd/system/php" + major + "-fpm.service")
		_ = e.run(ctx, "systemctl", "daemon-reload")
		_ = os.RemoveAll(prefix)
		_ = os.RemoveAll("/etc/php/" + major) // pool dir for this version
		for _, link := range []string{
			"/usr/local/bin/php-fpm" + major,
			"/usr/local/bin/php-fpm" + strings.ReplaceAll(major, ".", ""),
			"/usr/local/bin/php" + major,
		} {
			_ = os.Remove(link)
		}
		// Default `php` shim: re-point at the highest remaining source/distro
		// install, or drop it.
		if cur, lerr := os.Readlink("/usr/local/bin/php"); lerr == nil && strings.HasPrefix(cur, prefix+"/") {
			_ = os.Remove("/usr/local/bin/php")
		}
	}
	return nil
}
