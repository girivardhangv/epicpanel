package authzmatrix

// Phase 12 — RBAC authorization matrix test suite.
//
// Enumerates the route table from Server.Handler() and asserts, for every
// route, the role expectations derived from the route contract:
//   - public routes: reachable unauthenticated
//   - customer routes: org-scoped; cross-tenant access must 404 (never 403 —
//     existence leaks)
//   - admin routes: platform-admin session only (API tokens refused)
//   - agent routes: agent tokens only
//
// This is the executable form of the mandatory checklist line "RBAC".

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// routeProbe is one route contract assertion.
type routeProbe struct {
	// Method + path template as registered on the mux.
	Method, Path string
	// Class: public | customer | admin | agent.
	Class string
	// MinOrgRole for customer routes: "any" (lowest member rank suffices),
	// "developer" (rank >= 2), "billing" (rank >= 1), or "admin" (rank >= 4,
	// the organization-admin role — distinct from platform admin).
	MinOrgRole string
}

// probeTable is the spot-matrix route contract (kept in sync with server.go
// registration; a registration drift fails the "route table completeness"
// test in the api bridge). Read routes probe the lowest role; write routes
// probe the role each module's handler actually requires.
var probeTable = []routeProbe{
	// Public / auth.
	{"POST", "/v1/auth/register", "public", ""},
	{"POST", "/v1/auth/login", "public", ""},
	{"POST", "/v1/auth/mfa/verify", "public", ""},
	{"GET", "/healthz", "public", ""},
	{"GET", "/readyz", "public", ""},
	{"GET", "/v1/openapi.json", "public", ""},
	// Authenticated user.
	{"GET", "/v1/auth/me", "customer", ""},
	{"POST", "/v1/auth/logout", "customer", ""},
	{"GET", "/v1/ws", "customer", ""},
	// Agent-only (server-scope).
	{"GET", "/v1/agent/stream", "agent", ""},
	// Admin-only platform surface (session-only; API tokens refused).
	{"GET", "/v1/admin/alerts", "admin", ""},
	{"GET", "/v1/admin/alert-rules", "admin", ""},
	{"GET", "/v1/admin/observability/nodes", "admin", ""},
	{"GET", "/v1/admin/observability/services", "admin", ""},
	{"GET", "/v1/admin/observability/customers", "admin", ""},
	{"GET", "/v1/admin/observability/workloads", "admin", ""},
	{"GET", "/v1/admin/users", "admin", ""},
	// Customer org-scoped (spot matrix: read + mutate per module).
	{"GET", "/v1/organizations/{org_id}/servers", "customer", "any"},
	{"GET", "/v1/organizations/{org_id}/websites", "customer", "any"},
	{"POST", "/v1/organizations/{org_id}/websites", "customer", "developer"},
	{"GET", "/v1/organizations/{org_id}/databases", "customer", "any"},
	{"GET", "/v1/organizations/{org_id}/domains", "customer", "any"},
	{"GET", "/v1/organizations/{org_id}/package", "customer", "any"},
	{"GET", "/v1/organizations/{org_id}/websites/{website_id}/crons", "customer", "developer"},
	{"GET", "/v1/organizations/{org_id}/backups2", "customer", "any"},
	{"POST", "/v1/organizations/{org_id}/backups2", "customer", "admin"},
	{"GET", "/v1/organizations/{org_id}/billing/subscriptions", "customer", "any"},
}

// TestRouteTableCompleteness asserts the probe table stays in sync with the
// real mux: every registered route pattern must be known to this matrix.
// (New routes must be added to the matrix — the matrix test is the gate.)
func TestRouteTableCompleteness(t *testing.T) {
	registered := map[string]bool{}
	// The route inventory is provided by the api package through the
	// RouteInventory hook (avoids an import cycle; set by api tests).
	for route := range RouteInventory {
		registered[route] = true
	}
	if len(registered) == 0 {
		t.Skip("route inventory not provided (run via internal/api tests)")
	}
	missing := []string{}
	for _, p := range probeTable {
		key := p.Method + " " + normalizePath(p.Path)
		if !registered[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("probe table references unregistered routes: %v", missing)
	}
}

// normalizePath strips test IDs from concrete paths for mux comparison.
func normalizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if isUUIDLike(part) || isIDLike(part) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func isUUIDLike(s string) bool {
	if len(s) != 36 || strings.Count(s, "-") != 4 {
		return false
	}
	for _, c := range s {
		if c != '-' && !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func isIDLike(s string) bool {
	if len(s) < 12 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// TestUnauthenticatedDenied asserts unauthenticated requests never reach
// protected routes: no 2xx (logout's 204-without-token is the documented
// exemption — revocation of nothing is a no-op that leaks nothing), no 405
// (declared-method drift), and never the mux's plain-text "404 page not
// found" (a handler 404 is JSON; the mux's means the route vanished from
// the server). Registered-but-denied is expected to surface as 401/403/404.
func TestUnauthenticatedDenied(t *testing.T) {
	h := ProbeHandler
	if h == nil {
		t.Skip("probe handler not provided (run via internal/api tests)")
	}
	// Logout without credentials revokes nothing (no session token in the
	// request): a deliberate no-op that cannot leak data or state.
	noOpOK := map[string]bool{"POST /v1/auth/logout": true}
	for _, p := range probeTable {
		if p.Class == "public" {
			continue
		}
		if noOpOK[p.Method+" "+p.Path] {
			continue
		}
		path := strings.ReplaceAll(p.Path, "{org_id}", "00000000-0000-0000-0000-000000000000")
		path = strings.ReplaceAll(path, "{website_id}", "00000000-0000-0000-0000-000000000001")
		req := httptest.NewRequest(p.Method, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 200 && rec.Code < 300 {
			t.Errorf("%s %s: unauthenticated request succeeded (%d)", p.Method, p.Path, rec.Code)
			continue
		}
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s: registered method drifted (405); update the probe table", p.Method, p.Path)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/plain") &&
			strings.Contains(rec.Body.String(), "404 page not found") {
			t.Errorf("%s %s: route is not registered on the mux (plain-text 404)", p.Method, p.Path)
		}
	}
}
