package api

import (
	"net/http"
	"strings"
	"testing"
)

func bearerClient(c *testClient, rawToken string) {
	setBearer(c, rawToken)
}

// TestAPITokens covers Phase 11: token lifecycle + scope enforcement +
// org confinement.
func TestAPITokens(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ := resp.body["id"].(string)

	// --- 1. Create token with websites:read only ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "automation", "scopes": []string{"websites:read"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create token: %d %v", resp.status, resp.body)
	}
	raw, _ := resp.body["raw_token"].(string)
	if !strings.HasPrefix(raw, "epk_") {
		t.Fatalf("raw token format: %q", raw)
	}

	// --- 2. Token works for GET websites ---
	tok := admin.NewClient()
	bearerClient(tok, raw)
	resp = tok.do("GET", "/v1/organizations/"+orgID+"/websites", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("token GET websites: %d %v", resp.status, resp.body)
	}

	// --- 3. Token blocked from write (scope missing) ---
	resp = tok.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "nope", "server_id": "00000000-0000-0000-0000-000000000000",
	})
	if resp.status != http.StatusForbidden {
		t.Fatalf("token write without scope: %d want 403", resp.status)
	}

	// --- 4. Token cannot read other resources (scope missing) ---
	resp = tok.do("GET", "/v1/organizations/"+orgID+"/databases", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("token GET databases: %d want 403", resp.status)
	}

	// --- 5. Token with write scope can create a website (auto-placement
	// needs a server; without one it fails validation, proving scope passed) ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "writer", "scopes": []string{"websites:read", "websites:write"},
	})
	raw2, _ := resp.body["raw_token"].(string)
	tok2 := admin.NewClient()
	bearerClient(tok2, raw2)
	resp = tok2.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "writer-site",
	})
	// No servers exist -> auto-placement fails with validation error (not 403).
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("auto-placement without servers: %d %v", resp.status, resp.body)
	}
	if !strings.Contains(string(rune(0)), "") {
		_ = resp
	}

	// --- 6. Invalid token rejected ---
	bad := admin.NewClient()
	setBearer(bad, "epk_deadbeef")
	resp = bad.do("GET", "/v1/organizations/"+orgID+"/websites", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("bad token: %d want 401", resp.status)
	}

	// --- 7. Revoke kills access ---
	tokenID, _ := resp2id(t, admin, orgID)
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/api-tokens/"+tokenID, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.status)
	}
	resp = tok.do("GET", "/v1/organizations/"+orgID+"/websites", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d want 401", resp.status)
	}
}

func resp2id(t *testing.T, admin *testClient, orgID string) (string, error) {
	t.Helper()
	resp := admin.do("GET", "/v1/organizations/"+orgID+"/api-tokens", nil)
	list, _ := resp.body["api_tokens"].([]any)
	for _, item := range list {
		entry, _ := item.(map[string]any)
		if entry["name"] == "automation" {
			return entry["id"].(string), nil
		}
	}
	t.Fatal("automation token not found in list")
	return "", nil
}

// TestSchedulerAndMaintenance covers Phase 12: auto-placement picks the
// least-loaded online server; maintenance mode excludes a server.
func TestSchedulerAndMaintenance(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ := resp.body["id"].(string)

	// Two servers enrolled.
	var srv1, srv2 string
	for _, name := range []string{"node-a", "node-b"} {
		reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": name})
		regToken, _ := reg.body["registration_token"].(string)
		agent := admin.NewClient()
		enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
			"registration_token": regToken, "hostname": name + ".local",
		})
		setBearer(agent, enroll.body["agent_token"].(string))
		id := enroll.body["server"].(map[string]any)["id"].(string)
		if name == "node-a" {
			srv1 = id
			// node-a heavily loaded.
			agent.do("POST", "/v1/agent/heartbeat", map[string]any{
				"metrics": map[string]any{"cpu_percent": 95.0, "memory_total_bytes": 1000, "memory_used_bytes": 900, "disk_total_bytes": 100, "disk_used_bytes": 50},
			})
		} else {
			srv2 = id
			// node-b idle.
			agent.do("POST", "/v1/agent/heartbeat", map[string]any{
				"metrics": map[string]any{"cpu_percent": 5.0, "memory_total_bytes": 1000, "memory_used_bytes": 100, "disk_total_bytes": 100, "disk_used_bytes": 10},
			})
		}
	}

	// Auto-placed website should land on node-b.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "placed",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("auto-place: %d %v", resp.status, resp.body)
	}
	website, _ := resp.body["website"].(map[string]any)
	if website["server_id"] != srv2 {
		t.Fatalf("auto-placement picked %v want node-b %s", website["server_id"], srv2)
	}

	// Capacity endpoint reflects both servers.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/capacity", nil)
	caps, _ := resp.body["capacity"].([]any)
	if len(caps) != 2 {
		t.Fatalf("capacity entries: %d want 2", len(caps))
	}

	// Maintenance: drain node-b, next placement must fail (node-a also has
	// no capacity? it's online — but placement should pick node-a now? No:
	// maintenance excludes node-b, node-a remains eligible.
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/servers/"+srv2+"/maintenance", map[string]any{"enabled": true})
	if resp.status != http.StatusNoContent {
		t.Fatalf("maintenance: %d", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "placed2",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("place on node-a: %d %v", resp.status, resp.body)
	}
	website, _ = resp.body["website"].(map[string]any)
	if website["server_id"] != srv1 {
		t.Fatalf("placement after maintenance: %v want node-a", website["server_id"])
	}

	// Both drained -> placement fails.
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/servers/"+srv1+"/maintenance", map[string]any{"enabled": true})
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "placed3",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("no candidates: %d want 422", resp.status)
	}
}

func TestTokenOrgConfinement(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "OrgA"})
	orgA, _ := resp.body["id"].(string)
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "OrgB"})
	orgB, _ := resp.body["id"].(string)

	resp = admin.do("POST", "/v1/organizations/"+orgA+"/api-tokens", map[string]any{
		"name": "scoped", "scopes": []string{"websites:read"},
	})
	raw, _ := resp.body["raw_token"].(string)
	tok := admin.NewClient()
	setBearer(tok, raw)

	// Token works on its own org.
	resp = tok.do("GET", "/v1/organizations/"+orgA+"/websites", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("own org: %d", resp.status)
	}
	// Creator is owner of OrgB too, but the token is confined to OrgA.
	resp = tok.do("GET", "/v1/organizations/"+orgB+"/websites", nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("cross-org token: %d want 404", resp.status)
	}
}

func TestRateLimitAuth(t *testing.T) {
	_, admin := newTestServer(t)
	got429 := false
	for i := 0; i < 25; i++ {
		resp := admin.do("POST", "/v1/auth/login", map[string]string{
			"email": "nobody@example.test", "password": "wrongwrong12",
		})
		if resp.status == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("auth rate limit never returned 429")
	}
}
