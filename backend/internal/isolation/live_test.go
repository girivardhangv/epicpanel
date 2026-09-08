package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Live escape tests. Guarded by: root + bubblewrap. These run REAL bwrap
// sandboxes and assert that escape primitives fail.
func TestLiveSandboxEscapeAttempts(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}
	testDir := "/tmp/epicpanel-isolation-test"
	os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0o755)
	SetConfigDir(testDir)
	SetSiteBasePrefix(testDir)
	base := filepath.Join(testDir, "11111111-1111-1111-1111-111111111111")
	os.MkdirAll(base+"/tmp", 0o755)
	os.MkdirAll(base+"/public", 0o755)
	os.Chmod(testDir, 0o755)
	os.Chmod(base, 0o755)
	// own as uid 1500 like a real site tree
	exec.Command("chown", "-R", "1500:1500", base).Run()
	spec := Load("11111111-1111-1111-1111-111111111111", base, "ep-test", 1500, 1500)
	sb, err := NewSandbox(spec)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}

	run := func(cmdline string) (string, int) {
		cmd, err := sb.Command(cmdline)
		if err != nil {
			t.Fatalf("command: %v", err)
		}
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return string(out), code
	}

	t.Run("host_shadow_not_visible", func(t *testing.T) {
		out, _ := run("cat /etc/shadow")
		if !strings.Contains(out, "No such file") && !strings.Contains(out, "Permission denied") {
			t.Fatalf("/etc/shadow visible inside sandbox: %s", out)
		}
	})

	t.Run("other_site_tree_not_visible", func(t *testing.T) {
		// create a decoy site on the host
		decoy := "/srv/epicpanel/websites/99999999-9999-9999-9999-999999999999"
		os.MkdirAll(decoy, 0o750)
		os.WriteFile(decoy+"/secret.txt", []byte("TOP-SECRET"), 0o644)
		defer os.RemoveAll(decoy)
		out, _ := run("cat /srv/epicpanel/websites/99999999-9999-9999-9999-999999999999/secret.txt")
		if strings.Contains(out, "TOP-SECRET") {
			t.Fatalf("other site's files readable inside sandbox: %s", out)
		}
	})

	t.Run("host_root_not_visible", func(t *testing.T) {
		out, _ := run("ls /root")
		if !strings.Contains(out, "No such file") && !strings.Contains(out, "Permission denied") {
			t.Fatalf("/root visible inside sandbox: %s", out)
		}
	})

	t.Run("ps_sees_no_host_processes", func(t *testing.T) {
		out, _ := run("ps aux 2>/dev/null | wc -l")
		// inside a PID namespace only own processes exist; if ps fails (not
		// mounted) that's also a pass. A large count means host visibility.
		if strings.TrimSpace(out) != "" {
			var n int
			_, _ = fmtSscan(strings.TrimSpace(out), &n)
			if n > 50 {
				t.Fatalf("host processes visible via ps: %d rows", n)
			}
		}
	})

	t.Run("host_processes_invisible", func(t *testing.T) {
		// Inside the PID namespace only sandbox-internal processes exist. A
		// real host has 100+ processes; seeing < 20 proves PID ns isolation.
		out, _ := run("ps aux 2>/dev/null | wc -l")
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
		if n >= 20 {
			t.Fatalf("host process table visible via ps: %d rows", n)
		}
	})

	t.Run("mount_blocked", func(t *testing.T) {
		out, _ := run("mkdir -p /tmp/x && mount --bind /tmp /site 2>&1; echo MOUNT-EXIT:$?")
		if strings.Contains(out, "MOUNT-EXIT:0") {
			t.Fatalf("mount succeeded inside sandbox: %s", out)
		}
	})

	t.Run("symlink_escape_fails", func(t *testing.T) {
		out, _ := run("ln -sf /etc /site/evil-link 2>/dev/null; cat /site/evil-link/shadow 2>&1; echo SYM-EXIT:$?; rm -f /site/evil-link")
		if strings.Contains(out, "root:") && strings.Contains(out, "SYM-EXIT:0") {
			t.Fatalf("symlink escape read host shadow: %s", out)
		}
		if strings.Contains(out, "No such file") {
			t.Logf("symlink target correctly absent from namespace")
		}
	})

	t.Run("site_writes_still_work", func(t *testing.T) {
		out, code := run("echo data > /site/public/live-test.txt && cat /site/public/live-test.txt && rm /site/public/live-test.txt && echo WRITE-OK")
		if code != 0 || !strings.Contains(out, "WRITE-OK") {
			t.Fatalf("legitimate site writes broken: %s (code %d)", out, code)
		}
	})

	t.Run("allowed_binaries_work", func(t *testing.T) {
		out, code := run("echo BIN-OK && ls /site && echo BIN-EXIT:$?")
		if code != 0 || !strings.Contains(out, "BIN-EXIT:0") || !strings.Contains(out, "BIN-OK") {
			t.Fatalf("allowed binaries broken: %s (code %d)", out, code)
		}
	})
}

func fmtSscan(s string, args ...any) (int, error) {
	n, err := fmtSscanf(s, "%d", args...)
	return n, err
}
