package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestPlatformAdminKeys covers the epa_ platform admin API key lifecycle and
// the auth matrix that makes "admin controls everything via API" safe:
//   - epa_ keys reach the admin surface only with admin:read/admin:write
//   - epa_ keys operate across orgs with org scopes (never membership)
//   - epk_ org tokens NEVER reach the admin surface (ADR-027)
//   - key creation/revocation stays session-only (a leaked key cannot mint
//     or revoke other keys)
func TestPlatformAdminKeys(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})

	// --- 1. Session creates a full-control key (scope "*" expands) ---
	resp := admin.do("POST", "/v1/admin/api-keys", map[string]any{
		"name": "provisioning-bot", "scopes": []string{"*"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create admin key: %d %v", resp.status, resp.body)
	}
	raw, _ := resp.body["raw_token"].(string)
	if !strings.HasPrefix(raw, "epa_") {
		t.Fatalf("raw admin key format: %q", raw)
	}
	key := admin.NewClient()
	setBearer(key, raw)

	// --- 2. Key lists keys (admin:read via expansion) ---
	resp = key.do("GET", "/v1/admin/api-keys", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("admin key lists keys: %d %v", resp.status, resp.body)
	}

	// --- 3. Key reaches the admin WHM surface ---
	resp = key.do("GET", "/v1/adminview/overview", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("admin key adminview overview: %d %v", resp.status, resp.body)
	}

	// --- 4. Key manages platform users (formerly session-only) ---
	resp = key.do("POST", "/v1/admin/users", map[string]string{
		"email": "cust@example.test", "password": "customerpass1", "name": "Cust",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("admin key created user: %d %v", resp.status, resp.body)
	}

	// --- 5. Key reads the platform job console (formerly session-only) ---
	resp = key.do("GET", "/v1/jobs", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("admin key GET /v1/jobs: %d %v", resp.status, resp.body)
	}

	// --- 6. Key operates across orgs: create org + website (org scopes) ---
	resp = key.do("POST", "/v1/organizations", map[string]string{"name": "RemoteOrg"})
	if resp.status != http.StatusCreated {
		t.Fatalf("admin key create org: %d %v", resp.status, resp.body)
	}
	orgID, _ := resp.body["id"].(string)
	resp = key.do("GET", "/v1/organizations/"+orgID+"/websites", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("admin key GET websites in fresh org: %d %v", resp.status, resp.body)
	}
	resp = key.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "remote-site",
	})
	// No servers -> auto-placement validation error, proving the scope and
	// the platform-admin org bypass both passed (403/404 would fail).
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("admin key create website w/o servers: %d %v", resp.status, resp.body)
	}

	// --- 7. Key CANNOT mint or revoke keys (session-only by design) ---
	resp = key.do("POST", "/v1/admin/api-keys", map[string]any{
		"name": "escalation", "scopes": []string{"*"},
	})
	if resp.status != http.StatusForbidden {
		t.Fatalf("admin key created another key: %d want 403", resp.status)
	}

	// --- 8. Key without admin scopes is blocked from the admin surface ---
	resp = admin.do("POST", "/v1/admin/api-keys", map[string]any{
		"name": "site-only", "scopes": []string{"websites:read"},
	})
	rawSite, _ := resp.body["raw_token"].(string)
	siteKey := admin.NewClient()
	setBearer(siteKey, rawSite)
	resp = siteKey.do("GET", "/v1/adminview/overview", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("scoped key adminview: %d want 403", resp.status)
	}

	// --- 9. Org token (epk_) NEVER reaches the admin surface ---
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID2, _ := resp.body["id"].(string)
	resp = admin.do("POST", "/v1/organizations/"+orgID2+"/api-tokens", map[string]any{
		"name": "org-bot", "scopes": []string{"websites:read", "websites:write", "org:read"},
	})
	rawOrg, _ := resp.body["raw_token"].(string)
	orgTok := admin.NewClient()
	setBearer(orgTok, rawOrg)
	resp = orgTok.do("GET", "/v1/adminview/overview", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("org token adminview: %d want 403", resp.status)
	}
	resp = orgTok.do("GET", "/v1/admin/users", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("org token admin users: %d want 403", resp.status)
	}
	resp = orgTok.do("GET", "/v1/jobs", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("org token jobs console: %d want 403", resp.status)
	}

	// --- 10. Org token CAN read its own org (org:read gap fix) ---
	resp = orgTok.do("GET", "/v1/organizations/"+orgID2, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("org token GET org: %d %v", resp.status, resp.body)
	}
	// ...and is still confined: another org is 404-cloaked.
	resp = orgTok.do("GET", "/v1/organizations/"+orgID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("org token cross-org read: %d want 404", resp.status)
	}

	// --- 11. Invalid epa_ key rejected ---
	bad := admin.NewClient()
	setBearer(bad, "epa_deadbeef")
	resp = bad.do("GET", "/v1/adminview/overview", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("invalid admin key: %d want 401", resp.status)
	}

	// --- 12. Revocation kills the key immediately ---
	resp = admin.do("POST", "/v1/admin/api-keys", map[string]any{
		"name": "shortlived", "scopes": []string{"admin:read"},
	})
	rawShort, _ := resp.body["raw_token"].(string)
	shortID, _ := resp.body["admin_api_key"].(map[string]any)["id"].(string)
	shortKey := admin.NewClient()
	setBearer(shortKey, rawShort)
	if resp := shortKey.do("GET", "/v1/adminview/overview", nil); resp.status != http.StatusOK {
		t.Fatalf("fresh key adminview: %d %v", resp.status, resp.body)
	}
	if resp := admin.do("DELETE", "/v1/admin/api-keys/"+shortID, nil); resp.status != http.StatusNoContent {
		t.Fatalf("revoke admin key: %d %v", resp.status, resp.body)
	}
	if resp := shortKey.do("GET", "/v1/adminview/overview", nil); resp.status != http.StatusUnauthorized {
		t.Fatalf("revoked key still valid: %d want 401", resp.status)
	}
}

// TestBillingScopeMapping covers the billing:read/write scope group on the
// org-side billing surface (previously unreachable for tokens entirely).
func TestBillingScopeMapping(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin2@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "PayOrg"})
	orgID, _ := resp.body["id"].(string)

	resp = admin.do("POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "billing-bot", "scopes": []string{"billing:read"},
	})
	raw, _ := resp.body["raw_token"].(string)
	tok := admin.NewClient()
	setBearer(tok, raw)

	if resp := tok.do("GET", "/v1/organizations/"+orgID+"/billing/overview", nil); resp.status != http.StatusOK {
		t.Fatalf("billing:read token overview: %d %v", resp.status, resp.body)
	}
	// Write scope missing -> orders denied.
	if resp := tok.do("POST", "/v1/organizations/"+orgID+"/billing/orders", map[string]any{
		"product_id": "00000000-0000-0000-0000-000000000000",
	}); resp.status != http.StatusForbidden {
		t.Fatalf("billing:read token order: %d want 403", resp.status)
	}
}
