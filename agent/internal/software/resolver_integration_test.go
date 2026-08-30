//go:build integration

package software

import (
	"context"
	"testing"
)

// TestResolversResolve is an opt-in network test (run with
// `go test -tags integration -run TestResolversResolve`). It verifies each
// dynamic resolver can reach its upstream source and produce a valid Release.
func TestResolversResolve(t *testing.T) {
	ctx := context.Background()
	win := OSInfo{Family: "windows", Arch: "amd64"}
	cases := []struct{ name string; os OSInfo }{
		{"nginx", win}, {"php", win}, {"node", win},
		{"java", win}, {"mariadb", win},
		{"nginx", OSInfo{Family: "debian", Arch: "amd64"}},
		{"php", OSInfo{Family: "debian", Arch: "amd64"}},
		{"redis", OSInfo{Family: "debian", Arch: "amd64"}},
		{"apache", OSInfo{Family: "debian", Arch: "amd64"}},
		{"node", OSInfo{Family: "debian", Arch: "amd64"}},
		{"java", OSInfo{Family: "linux", Arch: "amd64"}},
		{"mariadb", OSInfo{Family: "linux", Arch: "amd64"}},
	}
	for _, c := range cases {
		p, ok := Default().Get(c.name)
		if !ok {
			t.Fatalf("provider not found: %s", c.name)
		}
		if p.Resolve == nil {
			t.Logf("%s: no resolver (package manager)", c.name)
			continue
		}
		rel, err := p.Resolve(ctx, c.os)
		if err != nil {
			t.Errorf("%s (on %s): resolve error: %v", c.name, c.os.Family, err)
			continue
		}
		if rel.Version == "" {
			t.Errorf("%s: empty version", c.name)
		}
		if rel.URL == "" {
			t.Errorf("%s: empty URL", c.name)
		}
		if rel.Format == "" {
			t.Errorf("%s: empty format", c.name)
		}
		t.Logf("%s (%s): v%s (sha=%t) %s", c.name, c.os.Family, rel.Version, rel.SHA256 != "", rel.URL)
	}
}
