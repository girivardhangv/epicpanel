package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The static-PHP placeholder pool must render a complete, validating pool:
// a section header, a live socket path under the managed dir, and the pm
// fields php-fpm requires (caught live on Debian 12 — an empty pool.d made
// every standalone static install fail validation forever).
func TestRenderStaticDefaultPool(t *testing.T) {
	content := renderStaticDefaultPool("8.3")
	for _, want := range []string{
		"[epicpanel-default]",
		"listen = " + fpmSocketDir + "/default-8.3.sock",
		"pm = dynamic",
		"pm.max_children = ",
		"pm.start_servers = ",
		"php_admin_value[error_log] = /var/log/epicpanel/php-fpm-8.3-default.log",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("default pool missing %q\ngot:\n%s", want, content)
		}
	}
	if strings.Contains(content, "user = root") {
		t.Error("default pool must not run workers as root")
	}
}

// symlinkReplace must (a) no-op when the link already targets the wanted
// path, (b) atomically re-point a stale link (the installGo upgrade bug:
// os.Symlink silently failed on an existing link, leaving the old minor on
// PATH), and (c) create the link when absent.
func TestSymlinkReplace(t *testing.T) {
	dir := t.TempDir()
	want1 := filepath.Join(dir, "go-1.24", "bin", "go")
	want2 := filepath.Join(dir, "go-1.26", "bin", "go")
	for _, w := range []string{want1, want2} {
		if err := os.MkdirAll(w, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "go")

	// (c) absent -> created
	if err := symlinkReplace(link, want1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if cur, _ := os.Readlink(link); cur != want1 {
		t.Fatalf("created link -> %q, want %q", cur, want1)
	}
	// (a) same target -> no-op (still fine afterwards)
	if err := symlinkReplace(link, want1); err != nil {
		t.Fatalf("no-op replace: %v", err)
	}
	// (b) stale target -> re-pointed
	if err := symlinkReplace(link, want2); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if cur, _ := os.Readlink(link); cur != want2 {
		t.Fatalf("upgraded link -> %q, want %q", cur, want2)
	}
}
