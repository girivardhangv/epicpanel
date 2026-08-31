package software

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// compileFromSource extracts a source tarball, installs build dependencies,
// configures, compiles and installs the component into our software dir.
// The result is a production-ready binary tree at <softwareDir>/<name>.
func (m *Manager) compileFromSource(ctx context.Context, p Provider, rel Release) error {
	if p.Build == nil {
		return fmt.Errorf("%s has no build spec", p.Name)
	}
	compDir := m.compDir(p.Name)
	if rel.URL == "" {
		return fmt.Errorf("%s has no source URL configured", p.Name)
	}

	// 1. Download and verify the source tarball.
	archive, err := download(rel.URL, rel.SHA256)
	if err != nil {
		return fmt.Errorf("download source %s: %w", p.Name, err)
	}
	defer os.Remove(archive)

	// 2. Extract to a temp build directory.
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.dir, ".tmp-build-"+p.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := extractTo(archive, rel.Format, rel.Prefix, tmp); err != nil {
		return fmt.Errorf("extract source %s: %w", p.Name, err)
	}

	// The extraction places files under tmp/<prefix> or directly into tmp.
	// Source tarballs always have a single top-level dir (the prefix).
	srcDir := tmp // default: files are at tmp root
	if rel.Prefix != "" {
		srcDir = filepath.Join(tmp, rel.Prefix)
	}
	if _, err := os.Stat(srcDir); err != nil {
		// Try walking to find the configure script.
		if found, werr := findFile(tmp, "configure"); werr == nil {
			srcDir = filepath.Dir(filepath.Join(tmp, found))
		} else {
			return fmt.Errorf("source tree for %s has no configure script", p.Name)
		}
	}

	// Verify the extracted tree is complete enough to build. A missing
	// critical file means the download was truncated/corrupt (nginx.org has
	// dropped connections mid-stream before); fail with a clear message so
	// the user can simply retry rather than debug a confusing configure error.
	for _, cf := range p.Build.CriticalFiles {
		if _, err := os.Stat(filepath.Join(srcDir, filepath.FromSlash(cf))); err != nil {
			return fmt.Errorf("source tree for %s is incomplete (missing %s) — download was likely truncated; retry the install", p.Name, cf)
		}
	}

	// 3. Build log file (persisted for debugging).
	_ = os.MkdirAll(compDir, 0o755)
	logPath := filepath.Join(compDir, "build.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create build log: %w", err)
	}
	defer logFile.Close()

	// 4. Install build dependencies (idempotent).
	if err := m.ensureBuildDeps(ctx, p, logFile); err != nil {
		return err
	}

	// 5. Run ./configure with the production flags (skip for components like
	//    Redis that have no configure script, just Makefile).
	if p.Build.NoConfigure {
		_ = writeLog(logFile, "skipping configure (no configure script)\n")
	} else {
		configureArgs := append([]string{
			filepath.Join(srcDir, "configure"),
			"--prefix=" + compDir,
		}, p.Build.ConfigureArgs...)
		if err := m.runBuildCmd(ctx, logFile, configureArgs[0], configureArgs[1:]...); err != nil {
			return fmt.Errorf("configure %s failed — see build log at %s", p.Name, logPath)
		}
	}

	// 6. make -j with CPU count, then install.
	ncpu := runtime.NumCPU()
	if ncpu < 1 {
		ncpu = 1
	}
	makeArgs := []string{"-j", strconv.Itoa(ncpu)}
	makeArgs = append(makeArgs, p.Build.MakeFlags...)
	if err := m.runBuildCmd(ctx, logFile, "make", makeArgs...); err != nil {
		return fmt.Errorf("make %s failed — see build log at %s", p.Name, logPath)
	}

	installArgs := []string{"install"}
	if p.Build.NoConfigure {
		// Components without configure (Redis) use `make install PREFIX=<dir>`
		// instead of the --prefix from configure.
		installArgs = append(installArgs, "PREFIX="+compDir)
	}
	if err := m.runBuildCmd(ctx, logFile, "make", installArgs...); err != nil {
		return fmt.Errorf("make install %s failed — see build log at %s", p.Name, logPath)
	}

	// 7. Post-install config generation (PHP/Redis ship no default config).
	if err := m.postInstallConfig(ctx, p, srcDir, compDir); err != nil {
		return fmt.Errorf("post-install config %s: %w", p.Name, err)
	}

	_ = logFile.Close()
	_ = os.Remove(logPath) // clean up on success
	return nil
}

// ensureBuildDeps installs the build dependencies for a component (idempotent).
func (m *Manager) ensureBuildDeps(ctx context.Context, p Provider, logFile *os.File) error {
	if p.Build == nil {
		return nil
	}
	var deps []string
	switch m.os.PackageManager {
	case "apt":
		deps = p.Build.DepsApt
	case "dnf":
		deps = p.Build.DepsDnf
	case "zypper":
		deps = p.Build.DepsZypper
	}
	if len(deps) == 0 {
		// No deps to install — assume they are present.
		_ = writeLog(logFile, "no build dependencies to install\n")
		return nil
	}
	_ = writeLog(logFile, "installing build dependencies: "+strings.Join(deps, ", ")+"\n")
	// Use a longer timeout for dependency installation (network, large packages).
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	argv := []string{m.os.PackageManager, "install", "-y"}
	argv = append(argv, deps...)
	_, err := Run(ctx, argv[0], argv[1:]...)
	if err != nil {
		_ = writeLog(logFile, "dependency install failed: "+err.Error()+"\n")
		return fmt.Errorf("install build deps: %w", err)
	}
	_ = writeLog(logFile, "build dependencies installed\n")
	return nil
}

// runBuildCmd executes a build command with a long timeout and logs full output.
// On failure, the entire log is preserved at the build log path.
func (m *Manager) runBuildCmd(ctx context.Context, logFile *os.File, cmd string, args ...string) error {
	_ = writeLog(logFile, "> "+cmd+" "+strings.Join(args, " ")+"\n")
	res, err := runCmdLong(ctx, cmd, args...)
	if err != nil {
		_ = writeLog(logFile, "[ERROR] "+err.Error()+"\n")
		_ = writeLog(logFile, "stdout:\n"+truncate(res.Stdout, 4096)+"\n")
		_ = writeLog(logFile, "stderr:\n"+truncate(res.Stderr, 4096)+"\n")
		return err
	}
	if res.ExitCode != 0 {
		_ = writeLog(logFile, "[ERROR] exit code "+strconv.Itoa(res.ExitCode)+"\n")
		_ = writeLog(logFile, "stdout:\n"+truncate(res.Stdout, 8192)+"\n")
		_ = writeLog(logFile, "stderr:\n"+truncate(res.Stderr, 8192)+"\n")
		return fmt.Errorf("%s exited %d", cmd, res.ExitCode)
	}
	_ = writeLog(logFile, res.Stdout)
	_ = writeLog(logFile, res.Stderr)
	return nil
}

func writeLog(f *os.File, msg string) error {
	if f == nil {
		return nil
	}
	_, err := f.WriteString(msg)
	return err
}

// postInstallConfig writes the runtime config files that some components do
// not ship with after a bare `make install` (PHP, Redis). nginx/Apache already
// install their conf trees. All content is static/derived — never from request
// input.
func (m *Manager) postInstallConfig(ctx context.Context, p Provider, srcDir, compDir string) error {
	_ = ctx
	switch p.Name {
	case "php":
		// php.ini: prefer the production template shipped in the source tree.
		ini := filepath.Join(compDir, "lib", "php.ini")
		_ = os.MkdirAll(filepath.Dir(ini), 0o755)
		if src, err := os.ReadFile(filepath.Join(srcDir, "php.ini-production")); err == nil {
			if err := os.WriteFile(ini, src, 0o644); err != nil {
				return err
			}
		} else if src, err := os.ReadFile(filepath.Join(srcDir, "php.ini-development")); err == nil {
			if err := os.WriteFile(ini, src, 0o644); err != nil {
				return err
			}
		}
		// php-fpm.conf + a default www pool so the compiled fpm can start.
		etc := filepath.Join(compDir, "etc")
		_ = os.MkdirAll(filepath.Join(etc, "pool.d"), 0o755)
		fpmConf := `[global]
pid = ` + filepath.Join(compDir, "var", "run", "php-fpm.pid") + `
error_log = ` + filepath.Join(compDir, "var", "log", "php-fpm.log") + `
include=` + filepath.Join(etc, "pool.d", "*.conf") + `
`
		if err := os.WriteFile(filepath.Join(etc, "php-fpm.conf"), []byte(fpmConf), 0o644); err != nil {
			return err
		}
		www := `[www]
user = epicpanel-sites
group = epicpanel-sites
listen = /run/php-fpm.sock
listen.owner = epicpanel-sites
listen.group = epicpanel-sites
pm = dynamic
pm.max_children = 10
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 3
`
		if err := os.WriteFile(filepath.Join(etc, "pool.d", "www.conf"), []byte(www), 0o644); err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Join(compDir, "var", "run"), 0o755)
		_ = os.MkdirAll(filepath.Join(compDir, "var", "log"), 0o755)
	case "redis":
		// Redis source ships a redis.conf; ensure it is present with sane
		// defaults for a supervised (non-daemonizing) service.
		etc := filepath.Join(compDir, "etc")
		_ = os.MkdirAll(etc, 0o755)
		conf := filepath.Join(etc, "redis.conf")
		if _, err := os.Stat(conf); err != nil {
			src := filepath.Join(srcDir, "redis.conf")
			raw, rerr := os.ReadFile(src)
			if rerr != nil {
				raw = []byte(defaultRedisConf)
			}
			if err := os.WriteFile(conf, raw, 0o644); err != nil {
				return err
			}
		}
		_ = os.MkdirAll(filepath.Join(compDir, "var", "log"), 0o755)
		_ = os.MkdirAll(filepath.Join(compDir, "var", "data"), 0o755)
	}
	return nil
}

const defaultRedisConf = `# EpicPanel-managed Redis
bind 127.0.0.1
protected-mode yes
port 6379
tcp-backlog 511
daemonize no
supervised no
logfile ""
databases 16
save 900 1
save 300 10
save 60 10000
dir /opt/epicpanel/software/redis/var/data
appendonly yes
appendfsync everysec
`

// findFile walks root and returns the relative path of the first file with
// the given base name. Used to locate configure scripts in unknown layouts.
func findFile(root, name string) (string, error) {
	var found string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if found != "" || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), name) {
			rel, rerr := filepath.Rel(root, path)
			if rerr == nil {
				found = filepath.ToSlash(rel)
			}
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if found == "" {
		return "", fmt.Errorf("file %s not found", name)
	}
	return found, nil
}