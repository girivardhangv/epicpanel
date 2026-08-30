package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidSiteName(t *testing.T) {
	valid := []string{"example.com", "shop.example.com", "wildcard.example.com", "a-b.co"}
	for _, n := range valid {
		if !validSiteName(n) {
			t.Errorf("validSiteName(%q) = false, want true", n)
		}
	}
	invalid := []string{"", "Example.com", "ex ample.com", "../evil", "a/b", `a\b`,
		"a;rm.com", "evil$(x).com", "-leading", ".dot", "double..dot"}
	for _, n := range invalid {
		if validSiteName(n) {
			t.Errorf("validSiteName(%q) = true, want false", n)
		}
	}
}

func TestParseNginxVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx version: nginx/1.24.0 (Ubuntu)\n", "1.24.0"},
		{"nginx/1.25.3\n", "1.25.3"},
		{"built by gcc\nnginx version: nginx/1.26.2", "1.26.2"},
		{"command not found", ""},
	}
	for _, c := range cases {
		got, _ := parseNginxVersion(c.in)
		if got != c.want {
			t.Errorf("parseNginxVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidPHPSetting(t *testing.T) {
	valid := map[string]string{
		"memory_limit": "256M", "upload_max_filesize": "64M",
		"post_max_size": "128M", "max_execution_time": "120",
		"max_input_time": "60", "memory_limit2": "", // key itself invalid below
	}
	if !ValidPHPSetting("memory_limit", "256M") {
		t.Error("256M should be valid")
	}
	if !ValidPHPSetting("max_execution_time", "120") {
		t.Error("120 seconds should be valid")
	}
	invalid := []struct{ k, v string }{
		{"memory_limit", ""}, {"memory_limit", "0"},
		{"memory_limit", "abc"}, {"memory_limit", "-1M"},
		{"max_execution_time", "abc"}, {"max_input_time", "1x"},
		{"arbitrary_directive", "1"}, // not allowlisted
		{"disable_functions", "exec"},
	}
	for _, c := range invalid {
		if ValidPHPSetting(c.k, c.v) {
			t.Errorf("ValidPHPSetting(%q, %q) = true, want false", c.k, c.v)
		}
	}
	_ = valid
}

func TestPHPSettingsBlockSkipsUnsafe(t *testing.T) {
	block := phpSettingsBlock(map[string]string{
		"memory_limit":       "256M",
		"arbitrary_directive": "pwned",
		"post_max_size":      "nope!",
	})
	if block != "php_admin_value[memory_limit] = 256M\n" {
		t.Errorf("settings block = %q", block)
	}
}

func TestReadLogBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	content := ""
	for i := 0; i < 1000; i++ {
		content += "line " + string(rune('a'+i%26)) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Small window: result must be bounded and start on a line boundary.
	text, size, truncated, err := ReadLogBounded(path, 512)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(content)) != size {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len(text) > 512 {
		t.Errorf("content length %d exceeds window", len(text))
	}
	if text[0] == '\n' || len(text) > 0 && text[0] == '\r' {
		t.Error("tail must start on a line boundary")
	}

	// Full read when the file is smaller than the window.
	text2, _, truncated2, err := ReadLogBounded(path, 512*1024)
	if err != nil || truncated2 {
		t.Fatalf("full read: truncated=%v err=%v", truncated2, err)
	}
	if text2 != content {
		t.Error("full read content mismatch")
	}
}

func TestFSOpsPathContainment(t *testing.T) {
	if runtime.GOOS == "windows" {
		root := `C:\Panel\Sites`
		fs, err := NewFSOps(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.(*fsOps).resolve(`C:\Panel\Sites\example.com\public`); err != nil {
			t.Errorf("inside root rejected: %v", err)
		}
		for _, bad := range []string{`C:\Windows\system32`, `C:\Panel`, `C:\Panel\Sites`,
			`C:\Panel\Sites\..\..`, ""} {
			if _, err := fs.(*fsOps).resolve(bad); err == nil && bad != `C:\Panel\Sites` {
				// equal-to-root is allowed for resolve (remove refuses later)
				if bad != "" {
					t.Errorf("resolve(%q) = nil error, want rejection", bad)
				}
			}
		}
	} else {
		root := "/srv/panel/sites"
		fs, err := NewFSOps(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.(*fsOps).resolve("/srv/panel/sites/example.com/public"); err != nil {
			t.Errorf("inside root rejected: %v", err)
		}
		for _, bad := range []string{"/etc/passwd", "/srv/panel", "/srv/panel/sites/../..", "/"} {
			if _, err := fs.(*fsOps).resolve(bad); err == nil {
				t.Errorf("resolve(%q) = nil error, want rejection", bad)
			}
		}
	}
}

func TestLogsReadAllowedDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		root := `C:\Panel\Sites`
		if _, err := validatedLogPath(root, `C:\Panel\Sites\example.com\logs\access.log`); err != nil {
			t.Errorf("site log rejected: %v", err)
		}
		if _, err := validatedLogPath(root, `C:\Windows\system.ini`); err == nil {
			t.Error("outside path must be rejected")
		}
	} else {
		root := "/srv/panel/sites"
		if _, err := validatedLogPath(root, "/srv/panel/sites/example.com/logs/error.log"); err != nil {
			t.Errorf("site log rejected: %v", err)
		}
		if _, err := validatedLogPath(root, "/var/log/nginx/error.log"); err != nil {
			t.Errorf("nginx log rejected: %v", err)
		}
		if _, err := validatedLogPath(root, "/etc/shadow"); err == nil {
			t.Error("sensitive path must be rejected")
		}
	}
}
