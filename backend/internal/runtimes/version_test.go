package runtimes

import "testing"

func TestValidVersionForType(t *testing.T) {
	cases := []struct {
		typ  Type
		ver  string
		want bool
	}{
		// node: single major
		{TypeNode, "22", true},
		{TypeNode, "20", true},
		{TypeNode, "22.4", false}, // frontend sends majors; minor form is invalid
		{TypeNode, "x", false},
		{TypeNode, "", false},
		// go: major.minor (patch resolved agent-side from go.dev)
		{TypeGo, "1.22", true},
		{TypeGo, "1", false}, // was wrongly accepted before the fix
		{TypeGo, "1.22.5", false},
		// php / python: major.minor
		{TypePHP, "8.3", true},
		{TypePHP, "8", false},
		{TypePHP, "8.3.33", false},
		{TypePython, "3.12", true},
		{TypePython, "3", false},
		// legacy two-part flavors still pass the default branch
		{TypeApache, "2.4", true},
		// the field-reported regression: "22" for node must NOT fail anymore
		{TypeNode, "24", true},
	}
	for _, c := range cases {
		if got := ValidVersionForType(c.typ, c.ver); got != c.want {
			t.Errorf("ValidVersionForType(%q, %q) = %v, want %v", c.typ, c.ver, got, c.want)
		}
	}
}

func TestValidVersionStillMajorMinor(t *testing.T) {
	if !ValidVersion("8.3") {
		t.Fatal("ValidVersion(8.3) = false")
	}
	if ValidVersion("22") {
		t.Fatal("ValidVersion(22) = true, want false (php/python contract)")
	}
}
