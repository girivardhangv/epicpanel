// Package authzmatrix — unit tests for the probe table itself plus a
// compile-time-ish integrity check on the route contract. The live tests
// (route-completeness, unauthenticated-denied, role matrix) run through the
// api package bridge (phase12_security_test.go) which supplies the hooks.
package authzmatrix

import (
	"strings"
	"testing"
)

// TestProbeTableIntegrity checks the probe table metadata: classes are the
// four known ones, MinOrgRole (when set) names a real role tier, and every
// customer route is org-scoped.
func TestProbeTableIntegrity(t *testing.T) {
	classes := map[string]bool{"public": true, "customer": true, "admin": true, "agent": true}
	// Real role tiers (organizations.RoleRank): billing = rank >= 1, any =
	// lowest member rank, developer = rank >= 2, admin = organization admin
	// (rank 4).
	roles := map[string]bool{"": true, "any": true, "billing": true, "developer": true, "admin": true}
	for _, p := range probeTable {
		if !classes[p.Class] {
			t.Errorf("%s %s: unknown class %q", p.Method, p.Path, p.Class)
		}
		if !roles[p.MinOrgRole] {
			t.Errorf("%s %s: unknown MinOrgRole %q", p.Method, p.Path, p.MinOrgRole)
		}
		if p.Class == "customer" && strings.Contains(p.Path, "/organizations/") {
			// org-scoped customer probes must carry the org wildcard
			found := false
			for _, seg := range splitPath(p.Path) {
				if seg == "{org_id}" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s: customer probe is not org-scoped", p.Method, p.Path)
			}
		}
	}
}

// TestNormalizePath covers the concrete-id -> wildcard normalization used
// when matching probed paths against the registered inventory.
func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/v1/organizations/550e8400-e29b-41d4-a716-446655440000/websites": "/v1/organizations/{id}/websites",
		"/v1/organizations/{org_id}/servers":                              "/v1/organizations/{org_id}/servers",
		"/v1/auth/me":                                                     "/v1/auth/me",
		"/v1/organizations/abcdef0123456789abcdef0123456789/members":      "/v1/organizations/{id}/members",
		"/v1/short/x":                                                     "/v1/short/x",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	return out
}
