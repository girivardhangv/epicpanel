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

// TestSafeJoinRejectsTraversal is the zip-slip guarantee: archive entries
// must never escape the destination root.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	for _, evil := range []string{
		"../../etc/passwd",
		"..\\..\\windows\\system32\\cmd.exe",
		"/etc/passwd",
		`C:\Windows\System32\cmd.exe`,
		"a/../../../b",
	} {
		if _, err := safeJoin("C:\\sites\\root", evil); err == nil {
			t.Errorf("safeJoin accepted traversal %q", evil)
		}
	}
	if p, err := safeJoin("C:\\sites\\root", "public/index.php"); err != nil || p == "" {
		t.Errorf("safeJoin rejected a valid entry: %v", err)
	}
}

func TestStripPrefix(t *testing.T) {
	cases := []struct{ name, prefix, want string }{
		{"nginx-1.27.4/nginx.exe", "nginx-1.27.4", "nginx.exe"},
		{"nginx-1.27.4/conf/nginx.conf", "nginx-1.27.4", "conf/nginx.conf"},
		{"nginx-1.27.4", "nginx-1.27.4", ""},            // the dir itself
		{"other/file.txt", "nginx-1.27.4", ""},          // unrelated, skip
		{"node-v22.11.0-linux-x64/bin/node", "node-v22.11.0-linux-x64", "bin/node"},
		{"plain.txt", "", "plain.txt"},                  // no prefix
	}
	for _, c := range cases {
		if got := stripPrefix(c.name, c.prefix); got != c.want {
			t.Errorf("stripPrefix(%q,%q)=%q want %q", c.name, c.prefix, got, c.want)
		}
	}
}

func TestBinName(t *testing.T) {
	win := OSInfo{Family: "windows"}
	linux := OSInfo{Family: "debian"}
	if got := binName(win, "nginx"); got != "nginx.exe" {
		t.Errorf("windows binName = %q", got)
	}
	if got := binName(win, "nginx.exe"); got != "nginx.exe" {
		t.Errorf("windows binName(exe) = %q", got)
	}
	if got := binName(linux, "nginx"); got != "nginx" {
		t.Errorf("linux binName = %q", got)
	}
}

// TestFindBinary ensures the tree search locates binaries under a versioned
// top-level directory (JRE-style archives).
func TestFindBinary(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "jdk-17.0.12+7", "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "jdk-17.0.12+7", "bin", "java.exe"), []byte("x"), 0o755)
	rel, err := findBinary(dir, "java.exe")
	if err != nil {
		t.Fatalf("findBinary: %v", err)
	}
	want := "jdk-17.0.12+7/bin/java.exe"
	if filepath.ToSlash(rel) != want {
		t.Errorf("findBinary = %q want %q", rel, want)
	}
	if _, err := findBinary(dir, "absent.exe"); err == nil {
		t.Error("findBinary should fail for a missing binary")
	}
}

// TestHasResolverSelection maps each provider to its self-contained status.
func TestHasResolverSelection(t *testing.T) {
	r := Default()
	cases := map[string]bool{
		"nginx":   true, // win zip + linux source
		"mariadb": true, // win + linux
		"php":     true, // win zip + linux source
		"node":    true, // win + linux
		"java":    true, // win + linux (adoptium)
		"redis":   true, // linux source (make only)
		"apache":  true, // linux source
		"docker":  false,
	}
	for name, want := range cases {
		p, _ := r.Get(name)
		if got := p.Resolve != nil; got != want {
			t.Errorf("%s: hasResolver=%v want %v", name, got, want)
		}
	}
}

func TestCompareDotted(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.27.4", "1.27.4", 0},
		{"1.27.10", "1.27.4", 1},
		{"12.3.3", "11.8.5", 1},
		{"8.5.10", "8.5.9", 1},
		{"10.6", "10.11", -1},
	}
	for _, c := range cases {
		if got := compareDotted(c.a, c.b); got != c.want {
			t.Errorf("compareDotted(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestMaxDotted(t *testing.T) {
	body := []byte(`
nginx-1.25.0.zip
nginx-1.27.4.zip
nginx-1.26.2.zip
`)
	v, ok := maxDotted(reNginxVersion, body)
	if !ok || v != "1.27.4" {
		t.Errorf("maxDotted nginx = %q,%v want 1.27.4,true", v, ok)
	}
}

func TestMaxFileAndVersionPHP(t *testing.T) {
	body := []byte(`
php-8.3.9-Win32-vs16-x64.zip
php-8.5.10-Win32-vs17-x64.zip
php-8.4.2-Win32-vs17-x64.zip
`)
	file, v, ok := maxFileAndVersion(rePHPVersion, body)
	if !ok || v != "8.5.10" || file != "php-8.5.10-Win32-vs17-x64.zip" {
		t.Errorf("maxFileAndVersion php = %q,%q,%v", file, v, ok)
	}
}

func TestBuildProviders(t *testing.T) {
	r := Default()
	for _, name := range []string{"nginx", "php", "redis", "apache"} {
		p, ok := r.Get(name)
		if !ok {
			t.Fatalf("provider %s not found", name)
		}
		if p.Build == nil {
			t.Errorf("%s: Build is nil (expected source build spec)", name)
		} else {
			if len(p.Build.ConfigureArgs) == 0 && !p.Build.NoConfigure {
				t.Errorf("%s: Build.ConfigureArgs is empty but NoConfigure=false", name)
			}
			if len(p.Build.DepsApt) == 0 {
				t.Errorf("%s: Build.DepsApt is empty", name)
			}
		}
	}
	// Components without official prebuilt binaries and no source build should
	// NOT have a Build spec (docker stays system-only).
	for _, name := range []string{"docker"} {
		p, _ := r.Get(name)
		if p.Build != nil {
			t.Errorf("%s: has Build spec (should not)", name)
		}
	}
}
