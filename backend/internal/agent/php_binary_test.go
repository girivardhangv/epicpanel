package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// phpBinary must find CLIs in both install layouts: distro packages
// (/usr/bin/phpX.Y) and panel-provisioned runtimes symlinked into
// /usr/local/bin (regression: one-click Laravel failed with "php 8.3 is
// not installed" on a server whose only 8.3 was panel-managed).
func TestPhpBinarySearchesBothLayouts(t *testing.T) {
	root := t.TempDir()
	distDir := filepath.Join(root, "usr", "bin")
	localDir := filepath.Join(root, "usr", "local", "bin")
	for _, d := range []string{distDir, localDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore := phpBinaryDirs
	defer func() { phpBinaryDirs = restore }()

	phpBinaryDirs = []string{distDir, localDir}

	// Nothing installed anywhere → distro default path (callers stat it and
	// turn the miss into the "not installed" error).
	if got := phpBinary("8.3"); got != "/usr/bin/php8.3" {
		t.Errorf("phpBinary with no installs = %q, want distro fallback /usr/bin/php8.3", got)
	}
	// Panel-managed CLI found even without a distro package.
	touch(t, filepath.Join(localDir, "php8.3"))
	if got := phpBinary("8.3"); got != filepath.Join(localDir, "php8.3") {
		t.Errorf("phpBinary = %q, want panel-managed /usr/local path", got)
	}
	// Distro package wins when both exist (matches the /usr/bin-first order).
	touch(t, filepath.Join(distDir, "php8.3"))
	if got := phpBinary("8.3"); got != filepath.Join(distDir, "php8.3") {
		t.Errorf("phpBinary = %q, want distro path preferred", got)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
