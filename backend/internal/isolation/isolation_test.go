package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestSpec creates a REAL site dir (inside the allowed prefix) so
// NewSandbox's existence check passes.
func newTestSpec(t *testing.T) Spec {
	t.Helper()
	SetSiteBasePrefix(t.TempDir())
	base := filepath.Join(siteBasePrefix, "11111111-1111-1111-1111-111111111111")
	if err := os.MkdirAll(filepath.Join(base, "tmp"), 0o750); err != nil {
		t.Fatalf("site dir: %v", err)
	}
	return Spec{
		Version:   1,
		WebsiteID: "11111111-1111-1111-1111-111111111111",
		SiteBase:  base,
		UID:       1500,
		GID:       1500,
		Rlimits:   DefaultRlimits(),
		Network:   DefaultNetwork(),
	}
}

func validSpec() Spec {
	return Spec{
		Version:   1,
		WebsiteID: "11111111-1111-1111-1111-111111111111",
		SiteBase:  "/srv/epicpanel/websites/11111111-1111-1111-1111-111111111111",
		UID:       1500,
		GID:       1500,
		Rlimits:   DefaultRlimits(),
		Network:   DefaultNetwork(),
	}
}

func TestValidateRejectsRootUID(t *testing.T) {
	s := validSpec()
	s.UID = 0
	if err := s.Validate(); err == nil {
		t.Fatal("spec with uid 0 must be rejected")
	}
}

func TestValidateRejectsBadSiteBase(t *testing.T) {
	s := validSpec()
	s.SiteBase = "/etc"
	if err := s.Validate(); err == nil {
		t.Fatal("site_base=/etc must be rejected")
	}
	s2 := validSpec()
	s2.SiteBase = "/srv/epicpanel/websites/../../etc"
	if err := s2.Validate(); err == nil {
		t.Fatal("traversal site_base must be rejected")
	}
}

func TestValidateRejectsBadRuntimeMounts(t *testing.T) {
	cases := []struct {
		name string
		m    Mount
	}{
		{"root source", Mount{Src: "/root", Dst: "/root"}},
		{"shadow source", Mount{Src: "/etc/shadow", Dst: "/etc/shadow"}},
		{"relative dst", Mount{Src: "/usr/local/go", Dst: "relative"}},
		{"site overlap", Mount{Src: "/usr/local/go", Dst: "/site"}},
		{"site overlap sub", Mount{Src: "/usr/local/go", Dst: "/site/public"}},
	}
	for _, tc := range cases {
		s := validSpec()
		s.RuntimeMounts = []Mount{tc.m}
		if err := s.Validate(); err == nil {
			t.Errorf("%s: spec must be rejected", tc.name)
		}
	}
}

func TestLoadFailsClosedToDefault(t *testing.T) {
	dir := t.TempDir()
	SetConfigDir(dir)
	websiteID := "22222222-2222-2222-2222-222222222222"
	base := "/srv/epicpanel/websites/" + websiteID
	s := Load(websiteID, base, "ep-test", 1500, 1500)
	if s.Version != 1 || s.SiteBase != base {
		t.Fatalf("default spec not applied: %+v", s)
	}
	if s.Rlimits.MaxProc <= 0 {
		t.Fatal("default rlimits missing")
	}
	// corrupted file -> default (fail closed)
	os.WriteFile(ConfigPath(websiteID), []byte("{corrupted"), 0o644)
	s = Load(websiteID, base, "ep-test", 1500, 1500)
	if s.Version != 1 {
		t.Fatal("corrupted spec must fail closed to defaults")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	SetConfigDir(dir)
	s := validSpec()
	s.RuntimeMounts = []Mount{{Src: "/usr/local/go-1.22.2", Dst: "/usr/local/go-1.22.2"}}
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := Load(s.WebsiteID, s.SiteBase, "u", s.UID, s.GID)
	if len(loaded.RuntimeMounts) != 1 || loaded.RuntimeMounts[0].Src != "/usr/local/go-1.22.2" {
		t.Fatalf("runtime mount lost: %+v", loaded.RuntimeMounts)
	}
}

func TestBuildArgsContainCoreSandbox(t *testing.T) {
	SetConfigDir(t.TempDir())
	s := newTestSpec(t)
	sb, err := NewSandbox(s)
	if err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	args, err := sb.buildArgs("echo hi")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--unshare-pid",
		"--die-with-parent",
		"--new-session",
		"--bind " + s.SiteBase + " /site",
		"--ro-bind /usr /usr",
		"--clearenv",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox args missing %q; got: %s", want, joined)
		}
	}
}

func TestBuildArgsNetworkUnshare(t *testing.T) {
	SetConfigDir(t.TempDir())
	s := newTestSpec(t)
	s.Network = NetworkPolicy{SharedHostNetwork: false}
	sb, err := NewSandbox(s)
	if err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	args, _ := sb.buildArgs("echo hi")
	if !strings.Contains(strings.Join(args, " "), "--unshare-net") {
		t.Error("network policy false must add --unshare-net")
	}
}

func TestBuildArgsRuntimeMounts(t *testing.T) {
	SetConfigDir(t.TempDir())
	s := newTestSpec(t)
	s.RuntimeMounts = []Mount{{Src: "/usr/local/go-1.22.2", Dst: "/usr/local/go-1.22.2"}}
	sb, _ := NewSandbox(s)
	args, _ := sb.buildArgs("echo hi")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--ro-bind /usr/local/go-1.22.2 /usr/local/go-1.22.2") {
		t.Errorf("runtime mount missing: %s", joined)
	}
}

func TestWrapRlimits(t *testing.T) {
	s := validSpec()
	out := wrapRlimits(s, "echo hi")
	for _, want := range []string{"ulimit -u 256", "ulimit -n 1024", "ulimit -v", "echo hi"} {
		if !strings.Contains(out, want) {
			t.Errorf("wrapRlimits missing %q: %s", want, out)
		}
	}
}

func TestSpecPathTraversalRejected(t *testing.T) {
	s := validSpec()
	s.RuntimeMounts = []Mount{{Src: "/usr/local/go", Dst: "/usr/../etc"}}
	if err := s.Validate(); err == nil {
		t.Fatal("dst traversal must be rejected")
	}
}

var _ = filepath.Join
