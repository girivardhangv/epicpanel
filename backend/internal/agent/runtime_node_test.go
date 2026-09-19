package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempNodeRoot points the managed node roots at a temp dir for the test's
// lifetime and returns the root.
func useTempNodeRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldRoot, oldShim, oldLegacy := nodeRootBase, nodeShimDir, legacyNodeRoot
	nodeRootBase, nodeShimDir = dir, filepath.Join(dir, "bin-shims")
	legacyNodeRoot = filepath.Join(dir, "nodejs")
	t.Cleanup(func() { nodeRootBase, nodeShimDir, legacyNodeRoot = oldRoot, oldShim, oldLegacy })
	return dir
}

// fakeManagedNode writes a managed install whose bin/node is an executable
// stub printing the given node version line (exec runs it like the real
// binary — the satisfies check is functional, not a stat).
func fakeManagedNode(t *testing.T, major, version string) string {
	t.Helper()
	binDir := filepath.Join(useTempNodeRoot(t), major, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "node")
	script := "#!/bin/sh\necho " + version + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// nodeManagedSatisfies must be a functional check: the binary has to run and
// report the requested major, so a half-extracted tree never counts as
// installed (the registry would then skip a needed reinstall).
func TestNodeManagedSatisfies(t *testing.T) {
	bin := fakeManagedNode(t, "22", "v22.14.0")
	if !nodeManagedSatisfies("22") {
		t.Error("managed 22 reporting v22.14.0 should satisfy")
	}
	if nodeManagedSatisfies("20") {
		t.Error("managed 22 must not satisfy a request for 20")
	}
	// Broken/partial install: non-executable binary must not satisfy.
	if err := os.Chmod(bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if nodeManagedSatisfies("22") {
		t.Error("non-runnable binary must not satisfy")
	}
}

// highestManagedNodeMajor picks the numerically newest managed major and
// ignores staging dirs and non-numeric entries.
func TestHighestManagedNodeMajor(t *testing.T) {
	root := useTempNodeRoot(t)
	for _, name := range []string{"9", "20", "22", ".staging-24", "latest", "22.old"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := highestManagedNodeMajor(); got != "22" {
		t.Errorf("highest managed major = %q, want \"22\" (9 vs 22 numeric, staging/old ignored)", got)
	}
}

// legacyNodeLinkedMajor parses the major out of the old global-tarball
// symlink layout and refuses foreign targets.
func TestLegacyNodeLinkedMajor(t *testing.T) {
	dir := t.TempDir()
	oldLegacy := legacyNodeRoot
	legacyNodeRoot = filepath.Join(dir, "nodejs")
	t.Cleanup(func() { legacyNodeRoot = oldLegacy })

	target := filepath.Join(legacyNodeRoot, "node-v22.23.2-linux-x64", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "node")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := legacyNodeLinkedMajor(link); got != "22" {
		t.Errorf("legacy link major = %q, want \"22\"", got)
	}
	foreign := filepath.Join(dir, "foreign")
	if err := os.Symlink("/usr/bin/node", foreign); err != nil {
		t.Fatal(err)
	}
	if got := legacyNodeLinkedMajor(foreign); got != "" {
		t.Errorf("foreign link major = %q, want \"\"", got)
	}
}

// nodeRuntimeBinDir resolves the managed bin dir only when that major is
// actually installed; anything else falls back to PATH resolution.
func TestNodeRuntimeBinDir(t *testing.T) {
	fakeManagedNode(t, "20", "v20.18.1")
	if got := nodeRuntimeBinDir("20"); filepath.Base(got) != "bin" || filepath.Base(filepath.Dir(got)) != "20" {
		t.Errorf("nodeRuntimeBinDir(20) = %q, want the managed 20 bin dir", got)
	}
	if got := nodeRuntimeBinDir("22"); got != "" {
		t.Errorf("nodeRuntimeBinDir(22) = %q, want \"\" (not installed)", got)
	}
	if got := nodeRuntimeBinDir(""); got != "" {
		t.Errorf("nodeRuntimeBinDir(\"\") = %q, want \"\"", got)
	}
}

// relinkDefaultNodeShims must create absent shims, re-point panel-owned
// links (managed or legacy trees), and never touch foreign links/files.
func TestRelinkDefaultNodeShims(t *testing.T) {
	fakeManagedNode(t, "22", "v22.14.0")
	shim := nodeShimDir
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	wantNode := filepath.Join(nodeRootBase, "22", "bin", "node")
	wantNpm := filepath.Join(nodeRootBase, "22", "bin", "npm")

	// Absent shim → created.
	relinkDefaultNodeShims("22")
	if cur, err := os.Readlink(filepath.Join(shim, "node")); err != nil || cur != wantNode {
		t.Errorf("absent node shim -> %q (err %v), want %q", cur, err, wantNode)
	}
	// Panel-owned (legacy tree) shim → re-pointed. The first relink already
	// created npm (absent → create), so replace it with the legacy link.
	legacyTarget := filepath.Join(legacyNodeRoot, "node-v20.11.0-linux-x64", "bin", "npm")
	_ = os.Remove(filepath.Join(shim, "npm"))
	if err := os.Symlink(legacyTarget, filepath.Join(shim, "npm")); err != nil {
		t.Fatal(err)
	}
	relinkDefaultNodeShims("22")
	if cur, err := os.Readlink(filepath.Join(shim, "npm")); err != nil || cur != wantNpm {
		t.Errorf("legacy npm shim -> %q (err %v), want %q", cur, err, wantNpm)
	}
	// Foreign shim → untouched. The target must live OUTSIDE both managed
	// roots (the shim dir sits under nodeRootBase in tests, so a separate
	// temp dir keeps it genuinely foreign). (The first relink created npx.)
	foreign := filepath.Join(shim, "npx")
	foreignTarget := filepath.Join(t.TempDir(), "nvm-npx")
	_ = os.Remove(foreign)
	if err := os.Symlink(foreignTarget, foreign); err != nil {
		t.Fatal(err)
	}
	relinkDefaultNodeShims("22")
	if cur, err := os.Readlink(foreign); err != nil || cur != foreignTarget {
		t.Errorf("foreign npx shim -> %q (err %v), want untouched %q", cur, err, foreignTarget)
	}
	// Foreign regular file → untouched (and no clobber).
	reg := filepath.Join(shim, "node2")
	if err := os.WriteFile(reg, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	relinkDefaultNodeShims("22")
	if b, err := os.ReadFile(reg); err != nil || string(b) != "x" {
		t.Error("foreign regular file was modified by relink")
	}
}
