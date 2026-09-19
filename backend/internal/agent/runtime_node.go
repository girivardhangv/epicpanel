package agent

// ============================================================================
// Node.js runtime — managed, per-major installs.
//
// Layout (per major, mirrors the python-standalone / java patterns):
//
//	/opt/epicpanel/node/<major>/bin/{node,npm,npx}
//	/usr/local/bin/node<major>            per-major shim (e.g. node22)
//	/usr/local/bin/{node,npm,npx}         convenience shims → a managed major
//
// Why per-major dirs instead of a repo: the runtime registry models one
// install per major, but apt holds ONE nodejs at a time and /usr/local/bin/
// node is a single symlink — a server hosting a Node 20 and a Node 22 site
// was impossible. Apps therefore exec the absolute managed binary and builds
// prepend the major's bin dir to PATH (app_ops.go); the system-wide `node`
// stays a convenience, never the source of truth.
//
// Install chain: managed tarball (nodejs.org official binaries —
// version-pinned, side-by-side, no apt repo mutation) is primary on glibc.
// On musl (Alpine) the glibc tarball cannot run at all (caught live: exec
// fails with ENOENT = missing ELF interpreter), so distro packages are the
// only path there. A failed managed install falls back to the historical
// system-wide strategies: NodeSource repo (apt) or distro nodejs (others).
// ============================================================================

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nodeRootBase is the managed Node.js install root. Injectable for tests.
var nodeRootBase = "/opt/epicpanel/node"

// nodeShimDir is where the PATH shims live. Injectable for tests.
var nodeShimDir = "/usr/local/bin"

// legacyNodeRoot is the pre-per-major global tarball location (old panel
// versions); cleaned up on removal when it holds the major being removed.
// Injectable for tests.
var legacyNodeRoot = "/usr/local/lib/nodejs"

func nodeManagedDir(major string) string { return filepath.Join(nodeRootBase, major) }

func nodeManagedBin(major string) string { return filepath.Join(nodeRootBase, major, "bin", "node") }

// nodeManagedSatisfies reports whether the managed per-major install exists
// and runs (functional check — a partial extraction must not count).
func nodeManagedSatisfies(major string) bool {
	out, err := exec.Command(nodeManagedBin(major), "--version").Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "v"+major+".")
}

// highestManagedNodeMajor returns the newest managed major ("22"), or "".
func highestManagedNodeMajor() string {
	entries, err := os.ReadDir(nodeRootBase)
	if err != nil {
		return ""
	}
	best := ""
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || !allDigits(name) {
			continue
		}
		if best == "" || atoiDefault(name) > atoiDefault(best) {
			best = name
		}
	}
	return best
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func atoiDefault(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// nodeShimOwned reports whether a shim symlink target belongs to the panel
// (managed per-major tree or the legacy global tarball layout). Foreign
// links (nvm, hand installs) are never touched.
func nodeShimOwned(target string) bool {
	return strings.HasPrefix(target, nodeRootBase+"/") ||
		strings.HasPrefix(target, legacyNodeRoot+"/")
}

// relinkDefaultNodeShims points the convenience shims at the given managed
// major — but only when absent or already panel-owned. A system-wide node
// installed by anything else stays the default.
func relinkDefaultNodeShims(major string) {
	binDir := filepath.Join(nodeManagedDir(major), "bin")
	for _, name := range []string{"node", "npm", "npx"} {
		link := filepath.Join(nodeShimDir, name)
		want := filepath.Join(binDir, name)
		fi, err := os.Lstat(link)
		switch {
		case err != nil: // absent → create
			_ = os.Symlink(want, link)
		case fi.Mode()&os.ModeSymlink == 0:
			// regular file/dir — foreign, leave it
		default:
			if cur, lerr := os.Readlink(link); lerr == nil && nodeShimOwned(cur) {
				_ = symlinkReplace(link, want)
			}
		}
	}
}

// installNode ensures Node.js <major> is available (see file header for the
// layout and fallback chain). Idempotent.
func (e *Executor) installNode(ctx context.Context, runtimeID, version string, p ProgressFunc) error {
	major := nodeMajor(version)
	if major == "" {
		return fmt.Errorf("invalid Node.js version %q (want major, e.g. 22)", version)
	}
	if nodeManagedSatisfies(major) {
		p(100, "Node.js "+major+" already installed (managed)")
		return nil
	}
	if e.pm.Name() == "apk" {
		// musl: the official glibc tarball cannot run — distro packages only.
		return e.installNodeSystem(ctx, runtimeID, version, major, p)
	}
	p(60, "Installing official Node.js "+major+" binaries…")
	if err := e.installNodeManaged(ctx, major, p); err != nil {
		slog.Warn("managed node install failed; falling back to system packages", "err", err)
		return e.installNodeSystem(ctx, runtimeID, version, major, p)
	}
	p(100, "Node.js "+major+" installed (managed)")
	return nil
}

// installNodeManaged installs the official nodejs.org binaries for a major
// into /opt/epicpanel/node/<major> and wires the PATH shims.
func (e *Executor) installNodeManaged(ctx context.Context, major string, p ProgressFunc) error {
	// nodejs.org asset naming: node-v22.23.2-linux-x64.tar.xz (x64 | arm64).
	arch := "x64"
	if goArch() == "arm64" {
		arch = "arm64"
	}
	p(70, "Resolving latest Node.js "+major+" release…")
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
	if err := e.downloadFile(ctx, url, tmp, 8*time.Minute); err != nil {
		return err
	}
	if err := os.MkdirAll(nodeRootBase, 0o755); err != nil {
		return err
	}
	staging := filepath.Join(nodeRootBase, ".staging-"+major)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	c2, cancel2 := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel2()
	// Extracts to node-v<full>-linux-<arch>/ containing bin/{node,npm,npx};
	// strip the prefix so the staging dir IS the install tree.
	if err := e.run(c2, "tar", "-xf", tmp, "-C", staging, "--strip-components=1"); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("extract nodejs tarball: %w", err)
	}
	_ = os.Remove(tmp)
	// Verify BEFORE replacing anything live: the staging binary must run and
	// report the requested major.
	out2, err := exec.CommandContext(ctx, filepath.Join(staging, "bin", "node"), "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out2)), "v"+major+".") {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("node tarball not functional: %s (%v)", tail([]byte(out2), 200), err)
	}
	// Swap in: old aside → staging in place → old removed. Apps running from
	// the old tree keep their open binaries; new starts use the new tree.
	final := nodeManagedDir(major)
	old := final + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			_ = os.RemoveAll(staging)
			return fmt.Errorf("stage node dir swap: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("activate node dir: %w", err)
	}
	_ = os.RemoveAll(old)

	// Per-major shim + convenience shims (re-pointed only when we own them).
	_ = e.symlinkIfAbsent(filepath.Join(final, "bin", "node"), filepath.Join(nodeShimDir, "node"+major))
	relinkDefaultNodeShims(major)
	slog.Info("managed nodejs installed", "major", major, "dir", final, "release", file)
	return nil
}

// installNodeSystem is the fallback path: NodeSource repo on apt, distro
// nodejs elsewhere. Used when the managed tarball cannot be delivered, and
// always on musl (Alpine) where the official glibc binaries cannot run.
func (e *Executor) installNodeSystem(ctx context.Context, runtimeID, version, major string, p ProgressFunc) error {
	installed := false
	if e.pm.Name() == "apt" {
		// NodeSource publishes one suite per major ("nodistro"); probing it
		// first means unsupported/new distro suites skip ahead instead of
		// poisoning apt with a 404 repo (deb822, pinned keyring — see
		// addNodeSourceRepo).
		if err := e.addNodeSourceRepo(ctx, major); err != nil {
			slog.Warn("nodesource repo unavailable; falling back", "err", err)
		} else {
			p(70, "Installing Node.js "+major+"…")
			if err := e.aptInstall(ctx, []string{"nodejs"}, runtimeID, version); err == nil {
				installed = true
			} else {
				slog.Warn("nodesource install failed; falling back", "err", err)
			}
		}
	}
	if !installed {
		// A stale panel shim shadowing the distro node on PATH (the managed
		// tree failed to deliver — its shim would exec as ENOENT forever).
		if cur, lerr := os.Readlink(filepath.Join(nodeShimDir, "node")); lerr == nil && nodeShimOwned(cur) {
			if out, verr := exec.CommandContext(ctx, filepath.Join(nodeShimDir, "node"), "--version").Output(); verr != nil {
				_ = os.Remove(filepath.Join(nodeShimDir, "node"))
				slog.Warn("removed stale managed node shim (binary not runnable)", "err", verr)
			} else {
				_ = out
			}
		}
		p(60, "Installing Node.js "+major+" from distro packages…")
		if err := e.pm.Install(ctx, []string{"nodejs"}); err == nil {
			// Distro repos carry ONE nodejs line — accept it only when it is
			// the requested major (otherwise there is no honest success).
			if out, verr := exec.CommandContext(ctx, "node", "--version").Output(); verr == nil &&
				strings.HasPrefix(strings.TrimSpace(string(out)), "v"+major+".") {
				installed = true
			} else {
				slog.Warn("distro nodejs is a different major", "want", major,
					"have", strings.TrimSpace(string(out)))
			}
		} else {
			slog.Warn("distro nodejs install failed", "manager", e.pm.Name(), "err", err)
		}
	}
	if !installed {
		return fmt.Errorf("node %s install failed (managed tarball and system packages)", major)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node binary missing after install")
	}
	p(100, "Node.js "+major+" installed (system)")
	return nil
}

// removeNode uninstalls one managed major: its tree, its shim, and the
// convenience shims healed to the highest remaining managed major (or
// dropped when panel-installed node is gone entirely — a shim into a
// removed tree execs as ENOENT forever). Distro packages stay managed by
// the package manager (purging the panel's own nodejs is not our call).
// The legacy global tarball layout of older panel versions is removed only
// when it holds the major being removed.
func (e *Executor) removeNode(ctx context.Context, version string) error {
	major := nodeMajor(version)
	if major == "" {
		return fmt.Errorf("invalid node version %q for removal", version)
	}
	dir := nodeManagedDir(major)
	managedExisted := false
	if _, err := os.Lstat(dir); err == nil {
		if err := e.run(ctx, "rm", "-rf", dir); err != nil {
			return err
		}
		managedExisted = true
	}
	_ = os.Remove(filepath.Join(nodeShimDir, "node"+major))
	for _, name := range []string{"node", "npm", "npx"} {
		link := filepath.Join(nodeShimDir, name)
		cur, lerr := os.Readlink(link)
		if lerr != nil || !strings.HasPrefix(cur, dir+"/") {
			continue // absent, foreign, or not pointing into the removed tree
		}
		if best := highestManagedNodeMajor(); best != "" {
			_ = symlinkReplace(link, filepath.Join(nodeManagedDir(best), "bin", name))
		} else {
			_ = os.Remove(link)
		}
	}
	// Legacy global layout cleanup (pre-per-major panel versions).
	nodeLink := filepath.Join(nodeShimDir, "node")
	if !managedExisted && legacyNodeLinkedMajor(nodeLink) == major {
		_ = e.run(ctx, "rm", "-rf", legacyNodeRoot)
		for _, name := range []string{"node", "npm", "npx"} {
			link := filepath.Join(nodeShimDir, name)
			if cur, lerr := os.Readlink(link); lerr == nil && strings.HasPrefix(cur, legacyNodeRoot+"/") {
				_ = os.Remove(link)
			}
		}
	}
	slog.Info("node runtime removed", "major", major)
	return nil
}

// legacyNodeLinkedMajor parses the major from a node shim symlink into the
// legacy tree (…/node-v22.23.2-linux-x64/bin/node), "" when not legacy.
func legacyNodeLinkedMajor(link string) string {
	cur, err := os.Readlink(link)
	if err != nil || !strings.HasPrefix(cur, legacyNodeRoot+"/") {
		return ""
	}
	stem := filepath.Base(filepath.Dir(filepath.Dir(cur)))
	if !strings.HasPrefix(stem, "node-v") {
		return ""
	}
	major := strings.SplitN(strings.TrimPrefix(stem, "node-v"), ".", 2)[0]
	if !allDigits(major) {
		return ""
	}
	return major
}
