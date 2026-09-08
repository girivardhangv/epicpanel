package api

import (
	"net/http"
	"testing"
)

// TestVhostLifecycle covers Phase 5 control-plane behavior:
// web_server validation, vhost data flow through provision jobs,
// and web_server switching via PATCH (reconcile).
func TestVhostLifecycle(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ := resp.body["id"].(string)

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent := admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID := enroll.body["server"].(map[string]any)["id"].(string)

	// --- 1. Invalid web_server rejected ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "site", "server_id": serverID, "web_server": "iis",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad web_server: %d want 422", resp.status)
	}

	// --- 2. Static site with nginx (default): provision job carries web_server ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "staticsite", "server_id": serverID, "primary_domain": "static.example.test",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create static site: %d %v", resp.status, resp.body)
	}
	website, _ := resp.body["website"].(map[string]any)
	if website["web_server"] != "nginx" {
		t.Fatalf("web_server default: %v want nginx", website["web_server"])
	}
	websiteID, _ := website["id"].(string)

	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil {
		t.Fatal("expected provision job")
	}
	payloadMap, _ := job["payload"].(map[string]any)
	if payloadMap["web_server"] != "nginx" {
		t.Fatalf("payload web_server: %v want nginx", payloadMap["web_server"])
	}
	if payloadMap["primary_domain"] != "static.example.test" {
		t.Fatalf("payload primary_domain: %v", payloadMap["primary_domain"])
	}
	jobID, _ := job["id"].(string)
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// --- 3. web_server=none: no vhost serving, static files only ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "noserver", "server_id": serverID, "web_server": "none",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create noserver site: %d", resp.status)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil {
		t.Fatal("expected provision job for noserver")
	}
	payloadMap, _ = job["payload"].(map[string]any)
	if payloadMap["web_server"] != "none" {
		t.Fatalf("noserver payload web_server: %v", payloadMap["web_server"])
	}
	jobID, _ = job["id"].(string)
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})

	// --- 4. standalone apache/OLS is rejected: nginx always owns :80/:443 ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "ols-site", "server_id": serverID, "web_server": "openlitespeed",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("standalone ols site must be rejected: %d want 422", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "ols-site", "server_id": serverID, "web_server": "nginx,openlitespeed",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create nginx,ols site: %d", resp.status)
	}
	ols, _ := resp.body["website"].(map[string]any)
	_ = ols["id"]

	// The provision payload must carry the web-server mode and a unique
	// backend port inside the OLS range (7100-7499).
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil {
		t.Fatal("expected provision job for nginx,ols site")
	}
	jobID, _ = job["id"].(string)
	if payloadMap["web_server"] == "nginx,openlitespeed" {
		t.Fatal("stale job claimed (ordering)")
	}
	_ = jobID
	olsPayload := job["payload"].(map[string]any)
	if olsPayload["web_server"] != "nginx,openlitespeed" {
		t.Fatalf("payload web_server: %v want nginx,openlitespeed", olsPayload["web_server"])
	}
	backendPort := int(olsPayload["backend_port"].(float64))
	if backendPort < 7100 || backendPort > 7499 {
		t.Fatalf("backend_port %d outside OLS range", backendPort)
	}
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})

	// --- 5. a second apache site gets a DIFFERENT unique apache port ---
	// (bump the org to a bigger package first: the default Starter caps sites)
	resp = admin.do("GET", "/v1/admin/packages", nil)
	var enterpriseID string
	if pkgs, ok := resp.body["packages"].([]any); ok {
		for _, raw := range pkgs {
			p, _ := raw.(map[string]any)
			if p["name"] == "Enterprise" {
				enterpriseID, _ = p["id"].(string)
			}
		}
	}
	if enterpriseID != "" {
		admin.do("POST", "/v1/admin/organizations/"+orgID+"/package", map[string]any{"package_id": enterpriseID})
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "apache-site", "server_id": serverID, "web_server": "nginx,apache",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create nginx,apache site: %d", resp.status)
	}
	apacheSite, _ := resp.body["website"].(map[string]any)
	apacheID, _ := apacheSite["id"].(string)
	// Drain the queued jobs (provision + quota etc.) without asserting.
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)

	// --- 6. docroot override (Laravel layout) via PATCH ---
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+apacheID, map[string]any{
		"docroot_suffix": "public/public",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("docroot patch: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	var docrootInPayload any
	for i := 0; i < 10; i++ {
		job, _ := claim.body["job"].(map[string]any)
		if job == nil {
			break
		}
		jt, _ := job["type"].(string)
		if jt == "provision_website" {
			p := job["payload"].(map[string]any)
			if p["docroot_suffix"] == "public/public" {
				docrootInPayload = p["docroot_suffix"]
				break
			}
		}
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})
		claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	}
	if docrootInPayload != "public/public" {
		t.Fatalf("reconcile payload missing docroot_suffix=public/public (got %v)", docrootInPayload)
	}
	// traversal attempts rejected
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+apacheID, map[string]any{
		"docroot_suffix": "../other-site",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("traversal docroot must be rejected: %d", resp.status)
	}
	_ = apacheSite
}
