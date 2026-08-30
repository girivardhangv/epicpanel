package platform

import "testing"

func TestCurrentReturnsKnownKind(t *testing.T) {
	k := Current()
	switch k {
	case KindLinux, KindWindows, Kind("other"):
		// "other" is the documented dev-fallback for darwin etc.
	default:
		t.Fatalf("unexpected platform kind %q", k)
	}
}

func TestNormalizeArch(t *testing.T) {
	for in, want := range map[string]string{
		"amd64":   "amd64",
		"arm64":   "arm64",
		"aarch64": "arm64",
	} {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectReportsHostHonestly(t *testing.T) {
	info, err := Collect(Current())
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	if info.OS == "" || info.Arch == "" {
		t.Fatalf("os/arch must always be reported, got %+v", info)
	}
	if info.Hostname == "" {
		t.Log("hostname unavailable on this host; agent tolerates that")
	}
}

func TestOsNameMapping(t *testing.T) {
	if osName(KindWindows) != "windows" || osName(KindLinux) != "linux" {
		t.Fatal("kind->wire name mapping drifted from backend contract")
	}
}
