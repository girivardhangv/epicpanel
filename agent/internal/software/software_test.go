package software

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "os-release")
	os.WriteFile(p, []byte("NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n# comment\n"), 0o644)
	f := parseEnvFile(p)
	if f["ID"] != "ubuntu" || f["ID_LIKE"] != "debian" {
		t.Errorf("parse failed: %+v", f)
	}
}

func TestRemoveArgs(t *testing.T) {
	got := removeArgs("apt", []string{"apt-get", "install", "-y", "nginx"})
	want := []string{"apt-get", "remove", "-y", "nginx"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("removeArgs[%d]=%q want %q", i, got[i], want[i])
		}
	}
	w := removeArgs("winget", []string{"winget", "install", "-e", "--id", "NGINX.NGINX"})
	if w[1] != "uninstall" {
		t.Errorf("winget uninstall not produced: %v", w)
	}
}

func TestRegistryAllowlist(t *testing.T) {
	r := Default()
	if _, ok := r.Get("nginx"); !ok {
		t.Error("nginx should be registered")
	}
	if _, ok := r.Get("rm"); ok {
		t.Error("arbitrary name must not be in the registry")
	}
	for _, n := range []string{"nginx", "mariadb", "redis", "php", "node", "docker"} {
		if _, ok := r.Get(n); !ok {
			t.Errorf("expected component %q", n)
		}
	}
}

// TestRunRejectsNonAllowlisted is the core security guarantee: a command not
// in the allowlist is refused before any execution.
func TestRunRejectsNonAllowlisted(t *testing.T) {
	_, err := Run(context.Background(), "rm", "-rf", "/")
	if err == nil {
		t.Fatal("expected non-allowlisted command to be rejected")
	}
}

func TestManagerUnknownComponent(t *testing.T) {
	m := NewManager(nil)
	if _, err := m.Install(context.Background(), "not-a-real-package"); err == nil {
		t.Error("install of unknown component must error")
	}
}
