package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// withHeader issues a request carrying one extra header (alias targeting:
// X-EpicPanel-Org) with the standard parsed-body response shape.
func (c *testClient) withHeader(method, path, key, val string, payload any) response {
	c.t.Helper()
	var reqBody *bodyReader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("marshal payload: %v", err)
		}
		reqBody = newBodyReader(b)
	} else {
		reqBody = newBodyReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, reqBody)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EpicPanel", "1")
	req.Header.Set(key, val)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		decoded = map[string]any{}
	}
	return response{status: resp.StatusCode, body: decoded}
}

// TestOrgAliases covers invisible tenancy (ADR-060):
//   - every account lands in a personal org at signup (self-registered and
//     admin-created alike) with collision-safe slugs
//   - short /v1/X paths resolve to the caller's active organization and are
//     equivalent to the canonical /v1/organizations/{org}/X routes
//   - X-EpicPanel-Org targets a specific org for sessions and platform keys
//   - org tokens (epk_) are pinned to their bound org: the header cannot
//     move them (confinement holds through the alias layer)
func TestOrgAliases(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})

	// --- 1. Auto-org at signup: exactly one org, no explicit creation step ---
	resp := admin.do("GET", "/v1/organizations", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("list orgs: %d %v", resp.status, resp.body)
	}
	orgs, _ := resp.body["organizations"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("auto-org at signup: got %d orgs, want 1", len(orgs))
	}
	org1, _ := orgs[0].(map[string]any)
	org1ID, _ := org1["id"].(string)

	// --- 2. Short URL is equivalent to the canonical path ---
	if resp = admin.do("GET", "/v1/websites", nil); resp.status != http.StatusOK {
		t.Fatalf("short GET /v1/websites: %d %v", resp.status, resp.body)
	}
	if resp = admin.do("GET", "/api/v1/websites", nil); resp.status != http.StatusOK {
		t.Fatalf("short GET /api/v1/websites (prefix rewrite compose): %d %v", resp.status, resp.body)
	}

	// --- 3. Full handler chain runs behind the alias (scopes, gates, RBAC) ---
	resp = admin.do("POST", "/v1/websites", map[string]any{"name": "via-alias"})
	// No servers -> auto-placement validation error (NOT 404/403).
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("short POST /v1/websites: %d %v", resp.status, resp.body)
	}

	// --- 4. Header targeting: second org is reachable explicitly ---
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Second Org"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create second org: %d %v", resp.status, resp.body)
	}
	org2ID, _ := resp.body["id"].(string)

	// Give org1 one token so its list is distinguishable from org2's.
	resp = admin.do("POST", "/v1/organizations/"+org1ID+"/api-tokens", map[string]any{
		"name": "probe", "scopes": []string{"websites:read"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create probe token: %d %v", resp.status, resp.body)
	}

	resp = admin.do("GET", "/v1/api-tokens", nil)
	toks, _ := resp.body["api_tokens"].([]any)
	if resp.status != http.StatusOK || len(toks) != 1 {
		t.Fatalf("short /v1/api-tokens defaults to primary org: %d %v", resp.status, resp.body)
	}
	resp = admin.withHeader("GET", "/v1/api-tokens", orgAliasHeader, org2ID, nil)
	toks2, _ := resp.body["api_tokens"].([]any)
	if resp.status != http.StatusOK || len(toks2) != 0 {
		t.Fatalf("header targeting second org: %d %d tokens, want 200/0", resp.status, len(toks2))
	}

	// --- 5. Org tokens stay pinned through the alias (header ignored) ---
	resp = admin.do("POST", "/v1/organizations/"+org1ID+"/api-tokens", map[string]any{
		"name": "confined", "scopes": []string{"org:read"},
	})
	rawOrg, _ := resp.body["raw_token"].(string)
	orgTok := admin.NewClient()
	setBearer(orgTok, rawOrg)

	resp = orgTok.do("GET", "/v1/api-tokens", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("org token short list: %d %v", resp.status, resp.body)
	}
	resp = orgTok.withHeader("GET", "/v1/api-tokens", orgAliasHeader, org2ID, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("org token short list w/ header: %d %v", resp.status, resp.body)
	}
	// org1 now holds two tokens (probe + confined); the header must NOT have
	// switched the listing to org2's empty set — confinement held.
	toks3, _ := resp.body["api_tokens"].([]any)
	if len(toks3) != 2 {
		t.Fatalf("org token header moved confinement: got %d tokens, want 2 (bound org)", len(toks3))
	}
	// ...and the second org stays 404-cloaked on the canonical path too.
	resp = orgTok.do("GET", "/v1/organizations/"+org2ID+"/api-tokens", nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("org token cross-org canonical: %d want 404", resp.status)
	}

	// --- 6. Platform keys target orgs via the header (ADR-059 + ADR-060) ---
	resp = admin.do("POST", "/v1/admin/api-keys", map[string]any{
		"name": "ops", "scopes": []string{"org:read"},
	})
	rawKey, _ := resp.body["raw_token"].(string)
	pkey := admin.NewClient()
	setBearer(pkey, rawKey)

	resp = pkey.do("GET", "/v1/api-tokens", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("platform key short list (primary org default): %d %v", resp.status, resp.body)
	}
	resp = pkey.withHeader("GET", "/v1/api-tokens", orgAliasHeader, org2ID, nil)
	toks4, _ := resp.body["api_tokens"].([]any)
	if resp.status != http.StatusOK || len(toks4) != 0 {
		t.Fatalf("platform key header targeting: %d %v", resp.status, resp.body)
	}

	// --- 7. Unauthenticated short path: structured 401, never a plain 404 ---
	anon := admin.NewClient()
	resp = anon.do("GET", "/v1/websites", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated short URL: %d want 401", resp.status)
	}

	// --- 8. Non-org segments are never rewritten ---
	if resp = admin.do("GET", "/v1/auth/me", nil); resp.status != http.StatusOK {
		t.Fatalf("skip-listed /v1/auth/me: %d %v", resp.status, resp.body)
	}
	if resp = admin.do("GET", "/v1/audit-logs", nil); resp.status != http.StatusOK {
		t.Fatalf("skip-listed /v1/audit-logs: %d %v", resp.status, resp.body)
	}

	// --- 9. Admin-created customers land in their own org ---
	resp = admin.do("POST", "/v1/admin/users", map[string]string{
		"email": "cust@example.test", "password": "customerpass1", "name": "Cust",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("admin create user: %d %v", resp.status, resp.body)
	}
	cust := admin.NewClient()
	resp = cust.do("POST", "/v1/auth/login", map[string]string{
		"email": "cust@example.test", "password": "customerpass1",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("cust login: %d %v", resp.status, resp.body)
	}
	resp = cust.do("GET", "/v1/organizations", nil)
	orgsC, _ := resp.body["organizations"].([]any)
	if resp.status != http.StatusOK || len(orgsC) != 1 {
		t.Fatalf("admin-created user auto-org: %d %v", resp.status, resp.body)
	}
	if resp = cust.do("GET", "/v1/websites", nil); resp.status != http.StatusOK {
		t.Fatalf("cust short URL: %d %v", resp.status, resp.body)
	}

	// --- 10. Slug collisions across same-named users are resolved ---
	alice1 := admin.NewClient()
	alice1.do("POST", "/v1/auth/register", map[string]string{
		"email": "a1@example.test", "password": "supersecret123", "name": "Alice",
	})
	alice2 := admin.NewClient()
	alice2.do("POST", "/v1/auth/register", map[string]string{
		"email": "a2@example.test", "password": "supersecret123", "name": "Alice",
	})
	var slugs []string
	for _, c := range []*testClient{alice1, alice2} {
		resp := c.do("GET", "/v1/organizations", nil)
		list, _ := resp.body["organizations"].([]any)
		if len(list) != 1 {
			t.Fatalf("alice org count: %d", len(list))
		}
		o, _ := list[0].(map[string]any)
		slugs = append(slugs, o["slug"].(string))
	}
	if slugs[0] == slugs[1] {
		t.Fatalf("slug collision unresolved: %v", slugs)
	}
	if !strings.HasPrefix(slugs[1], slugs[0]) {
		t.Fatalf("collision suffix unexpected: %v", slugs)
	}
}
