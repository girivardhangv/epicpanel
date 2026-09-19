package agent

// ============================================================================
// Redis runtime (managed software, Software page). Singleton per server.
//
// Install ladder, first success wins:
//  1. already satisfied: `redis-server --version` on PATH
//  2. distro packages: apt→redis-server, dnf/apk/pacman/zypper→redis
//  3. apt only: packages.redis.io deb822 repo (pinned keyring, probed
//     dists/stable — the old packagecloud gpgkey URL is dead) for the
//     latest stable when the distro package is missing or ancient
//  4. source build from download.redis.io (latest stable resolved from the
//     release index) into /opt/epicpanel/redis/<version> with a managed
//     systemd unit — needs a C compiler, installed best-effort first
//
// Service start is best-effort (systemctl); the hard verify is a functional
// `redis-server --version`. Configs bind 127.0.0.1 only — never expose a
// bare cache daemon.
// ============================================================================

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const redisRootBase = "/opt/epicpanel/redis"

// installRedis ensures a working redis-server is available.
func (e *Executor) installRedis(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	_ = version // singleton "latest" — the resolved version shows in --version

	// 1. Already satisfied?
	if out, err := exec.CommandContext(ctx, "redis-server", "--version").CombinedOutput(); err == nil {
		slog.Info("redis already installed", "version", strings.TrimSpace(string(out)))
		p(100, "Redis already installed")
		return nil
	}

	pmg := e.pm.Name()

	// 2. Distro package.
	p(25, "Installing Redis from distro packages…")
	if err := e.pm.Update(ctx); err != nil {
		slog.Warn("package index refresh failed; continuing with cache", "manager", pmg, "err", err)
	}
	pkg := "redis"
	if pmg == "apt" {
		pkg = "redis-server"
	}
	if err := e.pm.Install(ctx, []string{pkg}); err == nil {
		return e.finishRedisInstall(ctx, pmg, "redis-server", p, "distro")
	} else {
		slog.Warn("distro redis install failed; trying the redis.io repo", "manager", pmg, "err", err)
	}

	// 3. packages.redis.io (apt distros only; probe before pin, like the
	// ondrej/sury flows — a 404 repo poisons every later apt run).
	if pmg == "apt" {
		p(45, "Adding the packages.redis.io repository…")
		if err := e.addRedisRepo(ctx); err == nil {
			if err := e.pm.Install(ctx, []string{"redis-server"}); err == nil {
				return e.finishRedisInstall(ctx, pmg, "redis-server", p, "redis.io repo")
			} else {
				slog.Warn("redis.io repo install failed; falling back to source build", "err", err)
			}
		} else {
			slog.Warn("redis.io repo unavailable; falling back to source build", "err", err)
		}
	}

	// 4. Source build (universal fallback).
	p(60, "Building Redis from source…")
	if err := e.installRedisSource(ctx, p); err != nil {
		return err
	}
	return e.finishRedisInstall(ctx, pmg, filepath.Join(redisRootBase, "current", "bin", "redis-server"), p, "source")
}

// finishRedisInstall enables the service best-effort and verifies the
// server binary actually runs.
func (e *Executor) finishRedisInstall(ctx context.Context, pmg, serverBin string, p ProgressFunc, via string) error {
	p(90, "Enabling redis service…")
	unit := "redis-server"
	if pmg != "apt" {
		unit = "redis"
	}
	_ = e.run(ctx, "systemctl", "enable", unit)
	_ = e.run(ctx, "systemctl", "start", unit)
	// Functional verify: the binary must run and report a version.
	out, err := exec.CommandContext(ctx, serverBin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "v=") {
		return fmt.Errorf("redis-server not functional after install: %s (%v)", tail([]byte(out), 200), err)
	}
	// Reachability is a bonus (sandboxed/dev environments have no systemd):
	_ = e.run(ctx, "redis-cli", "ping")
	slog.Info("redis installed", "via", via, "version", strings.TrimSpace(string(out)))
	p(100, "Redis installed ("+via+")")
	return nil
}

// addRedisRepo provisions the packages.redis.io apt repo directly (deb822,
// pinned keyring) — never a pipe-to-bash setup script. Probe dists/stable
// first so unsupported suites fall through instead of poisoning apt.
func (e *Executor) addRedisRepo(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe := exec.CommandContext(c, "bash", "-c",
		`[ "$(curl -fsSL --max-time 20 -o /dev/null -w '%{http_code}' 'https://packages.redis.io/deb/dists/stable/Release')" = 200 ]`)
	if probe.Run() != nil {
		return fmt.Errorf("packages.redis.io unreachable")
	}
	c0, cancel0 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel0()
	if err := e.run(c0, "bash", "-c",
		"rm -f /etc/apt/sources.list.d/redis.list /etc/apt/sources.list.d/redis.list.save 2>/dev/null || true; true"); err != nil {
		return err
	}
	if err := e.run(ctx, "mkdir", "-p", "/etc/apt/keyrings"); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	if err := e.run(c2, "bash", "-c",
		"curl -fsSL --max-time 60 https://packages.redis.io/gpg | gpg --batch --yes --no-tty --dearmor -o /etc/apt/keyrings/redis.gpg"); err != nil {
		return fmt.Errorf("fetch redis signing key: %w", err)
	}
	sources := "X-Repolib-Name: Redis\n" +
		"Types: deb\n" +
		"URIs: https://packages.redis.io/deb\n" +
		"Suites: stable\n" +
		"Components: main\n" +
		"Signed-By: /etc/apt/keyrings/redis.gpg\n"
	if err := AtomicWriteFile("/etc/apt/sources.list.d/redis.sources", []byte(sources), 0o644); err != nil {
		return err
	}
	return e.aptUpdateTolerant(ctx)
}

var redisVersionRe = regexp.MustCompile(`redis-([0-9]+\.[0-9]+\.[0-9]+)\.tar\.gz`)

// installRedisSource resolves the latest stable tarball from
// download.redis.io, builds it with make (compiler installed best-effort),
// and installs into /opt/epicpanel/redis/<version> with a `current` symlink.
func (e *Executor) installRedisSource(ctx context.Context, p ProgressFunc) error {
	p(62, "Resolving the latest stable Redis release…")
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	listing, err := exec.CommandContext(c, "bash", "-c",
		"curl -fsSL --max-time 40 'https://download.redis.io/releases/' | grep -oE 'redis-[0-9]+\\.[0-9]+\\.[0-9]+\\.tar\\.gz' | sort -uV | tail -1").Output()
	if err != nil {
		return fmt.Errorf("fetch redis release index: %w", err)
	}
	file := strings.TrimSpace(string(listing))
	m := redisVersionRe.FindStringSubmatch(file)
	if m == nil {
		return fmt.Errorf("no redis release tarball found in download.redis.io index")
	}
	ver := m[1]

	// Compiler toolchain for make (root agent can install it).
	if _, err := exec.LookPath("cc"); err != nil {
		if _, err := exec.LookPath("gcc"); err != nil {
			p(65, "Installing a C compiler…")
			ccPkgs := []string{"build-essential"}
			if e.pm.Name() != "apt" {
				ccPkgs = []string{"gcc", "make"}
			}
			if err := e.pm.Install(ctx, ccPkgs); err != nil {
				return fmt.Errorf("C compiler required for redis source build: %w", err)
			}
		}
	}

	dest := filepath.Join(redisRootBase, ver)
	bin := filepath.Join(dest, "bin", "redis-server")
	if _, err := os.Stat(bin); err == nil {
		return e.linkRedisCurrent(dest, p) // already built
	}

	p(70, "Downloading Redis "+ver+"…")
	url := "https://download.redis.io/releases/" + file
	tmp := filepath.Join("/tmp", file)
	if err := e.downloadFile(ctx, url, tmp, 8*time.Minute); err != nil {
		return err
	}
	src := filepath.Join("/tmp", "redis-"+ver)
	_ = os.RemoveAll(src)
	if err := e.run(ctx, "tar", "-xzf", tmp, "-C", "/tmp"); err != nil {
		return fmt.Errorf("extract redis source: %w", err)
	}
	_ = os.Remove(tmp)
	defer os.RemoveAll(src)

	p(78, "Building Redis "+ver+" (make -j)…")
	buildCtx, buildCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer buildCancel()
	if err := e.runEnv(buildCtx, []string{"BUILD_TLS=no"}, "make", "-C", src, "-j", fmt.Sprint(runtime.NumCPU()), "MALLOC=libc"); err != nil {
		return fmt.Errorf("build redis: %w", err)
	}
	if err := e.run(ctx, "make", "-C", src, "install", "PREFIX="+dest); err != nil {
		return fmt.Errorf("install redis: %w", err)
	}

	// Minimal hardened config: localhost only, no protected-mode surprises.
	_ = os.MkdirAll(filepath.Join(dest, "etc"), 0o755)
	conf := "bind 127.0.0.1\n" +
		"port 6379\n" +
		"protected-mode yes\n" +
		"dir " + filepath.Join(dest, "data") + "\n" +
		"daemonize no\n" +
		"logfile " + filepath.Join(dest, "redis.log") + "\n"
	_ = os.MkdirAll(filepath.Join(dest, "data"), 0o750)
	if err := AtomicWriteFile(filepath.Join(dest, "etc", "redis.conf"), []byte(conf), 0o640); err != nil {
		return err
	}
	if err := e.linkRedisCurrent(dest, p); err != nil {
		return err
	}

	// Managed unit so `systemctl start redis` has something to talk to when
	// the distro did not ship one (source installs).
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		if _, err := os.Stat("/etc/systemd/system/redis.service"); err != nil {
			unit := "[Unit]\n" +
				"Description=Redis (EpicPanel managed)\n" +
				"After=network.target\n\n[Service]\n" +
				"Type=simple\n" +
				"ExecStart=" + filepath.Join(dest, "bin", "redis-server") + " " + filepath.Join(dest, "etc", "redis.conf") + "\n" +
				"ExecStop=/bin/kill -TERM $MAINPID\n" +
				"Restart=on-failure\n\n[Install]\n" +
				"WantedBy=multi-user.target\n"
			if err := AtomicWriteFile("/etc/systemd/system/redis.service", []byte(unit), 0o644); err != nil {
				return err
			}
			_ = e.run(ctx, "systemctl", "daemon-reload")
		}
	}
	return nil
}

// linkRedisCurrent points the `current` symlink and PATH shims at dest
// atomically (re-pointed on rebuilds).
func (e *Executor) linkRedisCurrent(dest string, p ProgressFunc) error {
	_ = p
	cur := filepath.Join(redisRootBase, "current")
	if curTarget, lerr := filepath.EvalSymlinks(cur); lerr != nil || curTarget != dest {
		tmp := cur + ".epicpanel-tmp"
		_ = os.Remove(tmp)
		if err := os.Symlink(dest, tmp); err != nil {
			return fmt.Errorf("link redis current: %w", err)
		}
		if err := os.Rename(tmp, cur); err != nil {
			return fmt.Errorf("swap redis current: %w", err)
		}
	}
	for _, bin := range []string{"redis-server", "redis-cli"} {
		if err := symlinkReplace("/usr/local/bin/"+bin, filepath.Join(redisRootBase, "current", "bin", bin)); err != nil {
			// PATH shim is a convenience; the service path works regardless.
			slog.Warn("redis PATH shim skipped", "bin", bin, "err", err)
		}
	}
	return nil
}

// removeRedis uninstalls redis: distro packages purged best-effort, managed
// source tree + unit + symlinks removed. Data under the managed tree goes
// with it; distro /var/lib/redis is left untouched.
func (e *Executor) removeRedis(ctx context.Context) error {
	c1, cancel1 := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel1()
	_ = e.pm.Remove(c1, []string{"redis-server", "redis"}) // best-effort
	_ = e.run(ctx, "systemctl", "stop", "redis.service")
	_ = e.run(ctx, "systemctl", "stop", "redis-server.service")
	_ = os.Remove("/etc/systemd/system/redis.service")
	_ = e.run(ctx, "systemctl", "daemon-reload")
	_ = os.RemoveAll(redisRootBase)
	for _, bin := range []string{"redis-server", "redis-cli"} {
		link := "/usr/local/bin/" + bin
		if cur, lerr := os.Readlink(link); lerr == nil && strings.HasPrefix(cur, redisRootBase+"/") {
			_ = os.Remove(link)
		}
	}
	slog.Info("redis removed")
	return nil
}
