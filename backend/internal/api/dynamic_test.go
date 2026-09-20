package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// newDynamicSite provisions a ready site with the given create body and
// returns its id. The provision job is claimed and completed so the site is
// status=ready (the allocator only acts on ready sites).
func newDynamicSite(t *testing.T, admin, agent *testClient, orgID, serverID, name string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{
		"name": name, "server_id": serverID, "runtime": "static",
		"primary_domain": name + ".example.test",
	}
	for k, v := range extra {
		body[k] = v
	}
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites", body)
	if resp.status != http.StatusAccepted {
		t.Fatalf("create site %s: %d %v", name, resp.status, resp.body)
	}
	website := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job != nil {
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
			"success": true,
			"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
		})
	}
	return websiteID
}

func TestDynamicResourcesLifecycle(t *testing.T) {
	srv, admin := newTestServer(t)
	dev := admin.NewClient()

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "dyn-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "DynOrg"})
	orgID, _ := resp.body["id"].(string)
	dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "dyn-dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "dyn-dev@example.test", "role": "developer",
	})
	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "dyn-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent := admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "dyn-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID := enroll.body["server"].(map[string]any)["id"].(string)

	// The migration seeds the Free Perk row; panel config defaults are off.
	resp = admin.do("GET", "/v1/admin/dynamic", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("GET /v1/admin/dynamic: %d %v", resp.status, resp.body)
	}
	if enabled, _ := resp.body["enabled"].(bool); enabled {
		t.Fatal("dynamic resources must default to OFF panel-wide")
	}
	perkPkg, _ := resp.body["free_perk_package"].(map[string]any)
	if perkPkg == nil {
		t.Fatal("free perk package missing (migration 0049 seed)")
	}

	// Creation-time flags: dynamic_enabled + free_perk on one site.
	websiteID := newDynamicSite(t, admin, agent, orgID, serverID, "dyn-site", map[string]any{
		"dynamic_enabled": true, "free_perk": true,
	})

	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("GET dynamic status: %d %v", resp.status, resp.body)
	}
	if v, _ := resp.body["enabled"].(bool); !v {
		t.Fatalf("site should be dynamic-enabled: %v", resp.body)
	}
	if v, _ := resp.body["free_perk"].(bool); !v {
		t.Fatalf("site should carry the free perk: %v", resp.body)
	}
	if v, _ := resp.body["tier"].(float64); v != 1 {
		t.Fatalf("fresh enable must start at base tier, got %v", resp.body["tier"])
	}
	if v, _ := resp.body["state"].(string); v != "active" {
		t.Fatalf("state = %v, want active", resp.body["state"])
	}

	// Free Perk cap: a second site cannot take the perk while cap = 1.
	secondID := newDynamicSite(t, admin, agent, orgID, serverID, "dyn-second", nil)
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+secondID+"/free-perk", nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("perk cap: expected 409, got %d %v", resp.status, resp.body)
	}
	// Raise the cap panel-wide and retry — now it succeeds.
	resp = admin.do("PATCH", "/v1/admin/dynamic", map[string]any{"free_perk_max_per_org": 2})
	if resp.status != http.StatusOK {
		t.Fatalf("patch admin dynamic: %d %v", resp.status, resp.body)
	}
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+secondID+"/free-perk", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("perk assign after cap raise: %d %v", resp.status, resp.body)
	}
	// Org view reflects usage.
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/free-perk", nil)
	if used, _ := resp.body["used"].(float64); used != 2 {
		t.Fatalf("org perk used = %v, want 2", resp.body["used"])
	}

	// Turn the panel-wide feature ON (needed by the allocator below).
	admin.do("PATCH", "/v1/admin/dynamic", map[string]any{"enabled": true})

	// Per-site disable: converges to base and clears state.
	resp = dev.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", map[string]any{"enabled": false})
	if resp.status != http.StatusOK {
		t.Fatalf("disable dynamic: %d %v", resp.status, resp.body)
	}
	// An enforce_limits job lands for the disabled site (base convergence).
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if job, _ := claim.body["job"].(map[string]any); job != nil {
		if jt, _ := job["type"].(string); jt != "enforce_limits" {
			t.Fatalf("expected enforce_limits job after disable, got %v", job["type"])
		}
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})
	}
	// Re-enable for the attack exercise.
	resp = dev.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", map[string]any{"enabled": true})
	if resp.status != http.StatusOK {
		t.Fatalf("re-enable dynamic: %d %v", resp.status, resp.body)
	}

	// ---- Bot attack: consecutive suspicious windows must suspend the site.
	attack := func() agentproto.SiteTraffic {
		return agentproto.SiteTraffic{
			WebsiteID: websiteID, WindowS: 60, Requests: 3000,
			UniqueIPs: 1, TopIP: "203.0.113.66", TopIPShare: 1, Top3Share: 1,
			UABadTool: 2800, NotFoundReqs: 2700, PathSamples: 500, UniquePaths: 490,
		}
	}
	srv.Traffic.Ingest([]agentproto.SiteTraffic{attack(), attack(), attack()})
	srv.dynamicTick(context.Background())

	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", nil)
	if state, _ := resp.body["state"].(string); state != "suspended_attack" {
		t.Fatalf("state after attack = %v, want suspended_attack (resp: %v)", resp.body["state"], resp.body)
	}
	// The suspension job is queued (idempotent lifecycle job).
	found := false
	for i := 0; i < 5 && !found; i++ {
		claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
		job, _ := claim.body["job"].(map[string]any)
		if job == nil {
			break
		}
		if jt, _ := job["type"].(string); jt == "suspend_website" {
			found = true
		}
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})
	}
	if !found {
		t.Fatal("attack did not enqueue a suspend_website job")
	}

	// Restore (manual, developer+): resume + back to active at base tier.
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic/restore", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("restore: %d %v", resp.status, resp.body)
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", nil)
	if state, _ := resp.body["state"].(string); state != "active" {
		t.Fatalf("state after restore = %v, want active", resp.body["state"])
	}
	// Decision trail recorded the full lifecycle.
	if events, _ := resp.body["recent_events"].([]any); len(events) == 0 {
		t.Fatal("no dynamic_resource_events recorded")
	}

	// ---- Panel-wide OFF sweep: dynamic sites converge to base.
	// Attack again, then flip the panel toggle off; the sweep resumes the site.
	srv.Traffic.Ingest([]agentproto.SiteTraffic{attack(), attack(), attack()})
	srv.dynamicTick(context.Background())
	admin.do("PATCH", "/v1/admin/dynamic", map[string]any{"enabled": false})
	srv.dynamicTick(context.Background())
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dynamic", nil)
	if state, _ := resp.body["state"].(string); state != "active" {
		t.Fatalf("state after panel-off sweep = %v, want active", resp.body["state"])
	}
}
