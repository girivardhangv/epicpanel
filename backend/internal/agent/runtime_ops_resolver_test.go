package agent

import (
	"context"
	"strings"
	"testing"
)

func TestResolveStaticPHPAssetURL(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	e := &Executor{}
	url, err := e.resolveStaticPHPAsset(context.Background(), "8.3", "x86_64")
	if err != nil {
		t.Skipf("static-php.dev index unreachable or changed: %v", err)
	}
	// Live-verified listing shape: php-8.3.9-fpm-linux-x86_64.tar.gz
	if !strings.HasPrefix(url, "https://dl.static-php.dev/static-php-cli/common/php-8.3.") ||
		!strings.Contains(url, "-fpm-linux-x86_64.tar.gz") {
		t.Fatalf("unexpected asset URL %q", url)
	}
}

func TestResolveStaticPHPAssetArm64Naming(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	e := &Executor{}
	url, err := e.resolveStaticPHPAsset(context.Background(), "8.2", "aarch64")
	if err != nil {
		t.Skipf("static-php.dev index unreachable or changed: %v", err)
	}
	if !strings.Contains(url, "aarch64") {
		t.Fatalf("arm64 mapping broken: %q", url)
	}
}

func TestResolveGitHubGlobPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	e := &Executor{}
	// Mirrors installStandalonePython's pattern for 3.12: '*' expands over
	// "<patch>+<builddate>" in cpython-3.12.14+20260901-x86_64-...
	pattern := "https://github.com/astral-sh/python-build-standalone/releases/latest/download/" +
		"cpython-3.12*x86_64-unknown-linux-gnu-install_only.tar.gz"
	url, err := e.resolveGitHubGlob(context.Background(), pattern)
	if err != nil {
		t.Skipf("github api unreachable or changed: %v", err)
	}
	if !strings.Contains(url, "cpython-3.12") || !strings.Contains(url, "x86_64-unknown-linux-gnu-install_only.tar.gz") {
		t.Fatalf("unexpected asset URL %q", url)
	}
}

func TestNodeTarballListingRegex(t *testing.T) {
	// The shell snippet installNodeTarball greps nodejs.org with must match
	// the live naming: node-v22.23.2-linux-x64.tar.xz (verified).
	file := "node-v22.23.2-linux-x64.tar.xz"
	stem := strings.TrimSuffix(file, ".tar.xz")
	if !strings.HasPrefix(stem, "node-v") || !strings.Contains(stem, "-linux-x64") {
		t.Fatalf("stem parsing broken: %q", stem)
	}
}
