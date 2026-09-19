package agent

// ============================================================================
// Java runtime (managed software runtime, exposed on the Software page).
//
// Install strategy, in order:
//  1. already-satisfied check (PATH java, /usr/lib/jvm, /opt/epicpanel/java)
//     — `java -version` output is matched against the requested major
//     (Java 8 prints "1.8.0_…", Java 9+ prints "21.0.4", "17.0.10", …).
//  2. distro packages (openjdk-<major>-jre-headless via the detected package
//     manager — tolerated to fail when the distro does not ship the version).
//  3. Adoptium/Temurin tarball fallback (api.adoptium.net latest hotspot
//     build) extracted to /opt/epicpanel/java/<major> with a /usr/local/bin/java
//     symlink — always published for every major (8/11/17/21/25), zero repo
//     needed.
//
// Everything is idempotent per major; never clobbers a foreign JVM.
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
	"strings"
	"time"
)

// javaRootBase is the managed Adoptium install root (one dir per major).
const javaRootBase = "/opt/epicpanel/java"

// installJava ensures a JDK/JRE of the requested major is available on PATH.
func (e *Executor) installJava(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := javaMajorFor(version)
	if major == "" || !isJavaMajor(major) {
		return fmt.Errorf("invalid Java version %q (want a single major, e.g. 21)", version)
	}

	// 1. Already satisfied?
	if ok := javaSatisfies(ctx, major); ok {
		slog.Info("java already installed", "major", major)
		p(100, "Java "+major+" already installed")
		return nil
	}

	// 2. Distro packages (best-effort: many distros ship only one version).
	p(20, "Installing OpenJDK "+major+" from distro packages…")
	pkg := "openjdk-" + major + "-jre-headless"
	c1, cancel1 := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel1()
	if err := e.pm.Update(c1); err != nil {
		slog.Warn("package index refresh failed; continuing with cache", "manager", e.pm.Name(), "err", err)
	}
	if err := e.pm.Install(c1, []string{pkg}); err == nil && javaSatisfies(ctx, major) {
		p(100, "Java "+major+" installed (distro OpenJDK)")
		return nil
	} else if err != nil {
		slog.Warn("distro openjdk install failed; falling back to Temurin tarball", "package", pkg, "err", err)
	} else {
		slog.Warn("distro openjdk present but wrong major; falling back to Temurin tarball", "package", pkg)
	}

	// 3. Adoptium/Temurin tarball.
	p(40, "Installing Eclipse Temurin "+major+" (Adoptium)…")
	if err := e.installJavaTarball(ctx, major, p); err != nil {
		return err
	}
	if ok := javaSatisfies(ctx, major); !ok {
		return fmt.Errorf("java %s not functional after install", major)
	}
	p(100, "Java "+major+" installed (Temurin)")
	return nil
}

// isJavaMajor accepts plain numeric majors ("8", "17", "21").
func isJavaMajor(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// javaMajorFor normalizes a stored runtime version to a single major: java
// registry rows may carry "17", "17.0" or "1.8"; the numeric-prefix major is
// what the installer and removal counterpart work against.
func javaMajorFor(version string) string {
	v := strings.TrimSpace(version)
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	if v == "1" {
		// legacy "1.8" style: the meaningful major is the next component.
		rest := strings.TrimSpace(version)
		parts := strings.Split(rest, ".")
		if len(parts) >= 2 {
			v = parts[1]
		}
	}
	return v
}

// javaSatisfies reports whether a java binary with the requested major is
// already reachable: PATH `java -version` output first, then JVM dirs
// (/usr/lib/jvm, the managed /opt/epicpanel/java tree). Handles both version
// formats: "1.<major>.0_…" (Java 8) and "\"<major>.…" (Java 9+).
func javaSatisfies(ctx context.Context, major string) bool {
	if out, err := exec.CommandContext(ctx, "java", "-version").CombinedOutput(); err == nil {
		if javaOutputMatches(string(out), major) {
			return true
		}
	}
	for _, dir := range []string{"/usr/lib/jvm", javaRootBase} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*"+major+"*/bin/java"))
		if len(matches) > 0 {
			return true
		}
	}
	return false
}

// javaOutputMatches parses `java -version` output for the requested major.
func javaOutputMatches(out, major string) bool {
	// Java 9+: openjdk version "21.0.4" 2026-07-16
	if strings.Contains(out, "\""+major+".") || strings.Contains(out, "\""+major+"\"") {
		return true
	}
	// Java 8: java version "1.8.0_412"
	if major == "8" && strings.Contains(out, "\"1.8") {
		return true
	}
	return false
}

// installJavaTarball downloads the latest Adoptium Temurin hotspot build for
// the major into /opt/epicpanel/java/<major> and wires `java` onto PATH.
func (e *Executor) installJavaTarball(ctx context.Context, major string, p ProgressFunc) error {
	base := filepath.Join(javaRootBase, major)
	if ok := javaSatisfies(ctx, major); ok {
		p(100, "Temurin "+major+" already installed")
		return nil
	}

	// Adoptium wants x64 / aarch64 (go's amd64/arm64 mapped).
	arch := "x64"
	if goArch() == "arm64" {
		arch = "aarch64"
	}
	api := fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%s/hotspot?architecture=%s&image_type=jdk&os=linux&project=jdk", major, arch)

	p(55, "Resolving Temurin "+major+" release…")
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(apiReq)
	if err != nil {
		return fmt.Errorf("adoptium api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("adoptium api: %s", resp.Status)
	}
	var assets []struct {
		Binary struct {
			Package struct {
				Link string `json:"link"`
			} `json:"package"`
		} `json:"binary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return fmt.Errorf("adoptium api decode: %w", err)
	}
	if len(assets) == 0 || assets[0].Binary.Package.Link == "" {
		return fmt.Errorf("no Temurin %s build published for %s", major, arch)
	}
	tarURL := assets[0].Binary.Package.Link
	slog.Info("installing java from adoptium", "major", major, "url", tarURL)

	p(65, "Downloading Temurin "+major+"…")
	file := fmt.Sprintf("temurin-%s-%s.tar.gz", major, arch)
	tmp := filepath.Join("/tmp", file)
	if err := e.downloadFile(ctx, tarURL, tmp, 10*time.Minute); err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel2()
	// Tarball extracts to jdk-<full>+<build>/ — strip the prefix so
	// /opt/epicpanel/java/<major>/bin/java is the canonical binary path.
	if err := e.run(c2, "tar", "-xzf", tmp, "-C", base, "--strip-components=1"); err != nil {
		return fmt.Errorf("extract temurin: %w", err)
	}
	_ = os.Remove(tmp)

	// Wire `java` onto PATH. Replace a stale symlink ONLY when it points
	// into our managed tree (or is dangling) — never clobber a foreign JVM.
	link := "/usr/local/bin/java"
	if cur, lerr := os.Readlink(link); lerr != nil || strings.HasPrefix(cur, javaRootBase+"/") {
		_ = os.Remove(link)
		if err := os.Symlink(filepath.Join(base, "bin", "java"), link); err != nil {
			return fmt.Errorf("link java: %w", err)
		}
	}
	_ = e.symlinkIfAbsent(filepath.Join(base, "bin", "java"), "/usr/local/bin/java-"+major)

	if out, err := exec.CommandContext(ctx, filepath.Join(base, "bin", "java"), "-version").CombinedOutput(); err != nil || !javaOutputMatches(string(out), major) {
		return fmt.Errorf("temurin java not functional: %s (%v)", tail([]byte(out), 200), err)
	}
	slog.Info("temurin java installed", "major", major, "dir", base)
	return nil
}

// RemoveJava uninstalls a managed Java major: our Temurin tree, its PATH
// symlinks, and a distro openjdk-<major>-jre-headless we installed (best
// effort — a JVM installed outside EpicPanel is left alone).
func (e *Executor) RemoveJava(ctx context.Context, major string) error {
	major = javaMajorFor(major)
	if major == "" {
		return fmt.Errorf("invalid java version %q for removal", major)
	}
	base := filepath.Join(javaRootBase, major)
	if _, err := os.Stat(base); err == nil {
		if err := os.RemoveAll(base); err != nil {
			return fmt.Errorf("remove %s: %w", base, err)
		}
	}
	_ = os.Remove("/usr/local/bin/java-" + major)
	// Drop the default `java` symlink when nothing satisfies it anymore.
	if cur, lerr := os.Readlink("/usr/local/bin/java"); lerr == nil && strings.HasPrefix(cur, javaRootBase+"/") {
		if ok := javaSatisfies(ctx, major); !ok {
			_ = os.Remove("/usr/local/bin/java")
		}
	}
	c1, cancel1 := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel1()
	_ = e.pm.Remove(c1, []string{"openjdk-" + major + "-jre-headless"}) // best-effort
	slog.Info("java runtime removed", "major", major)
	return nil
}
