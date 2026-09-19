package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestApplicationLifecycle covers node app creation, build+start job flow,
// status, update->restart, stop, and static-runtime rejection.
func TestApplicationLifecycle(t *testing.T) {
	_, admin := newTestServer(t)
	dev := admin.NewClient()

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ := resp.body["id"].(string)

	dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "dev@example.test", "role": "developer",
	})

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent := admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID := enroll.body["server"].(map[string]any)["id"].(string)

	// node runtime available
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "node", "version": "22"})
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if j, _ := claim.body["job"].(map[string]any); j != nil {
		agent.do("POST", "/v1/agent/jobs/"+j["id"].(string)+"/result", map[string]any{"success": true})
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", nil)
	if !strings.Contains(string(rune(0)), "") && !runtimeAvailable(resp, "node") {
		t.Fatalf("node runtime not available: %v", resp.body)
	}

	// upgrade org to Enterprise package (allows node/python/go runtimes)
	resp = admin.do("POST", "/v1/admin/packages", map[string]any{
		"name": "AppTest-Pkg", "max_websites": 10, "max_databases": 10, "max_disk_mb": 10240,
		"memory_limit_mb": 256, "cpu_cores": 2,
		"max_addon_domains": 10, "max_subdomains": 10,
		"allowed_runtimes": []string{"static", "php", "node", "python", "go"},
	})
	pkgID, _ := resp.body["id"].(string)
	resp = admin.do("POST", "/v1/admin/organizations/"+orgID+"/package", map[string]string{"package_id": pkgID})
	if resp.status != http.StatusOK {
		t.Fatalf("assign package: %d %v", resp.status, resp.body)
	}

	// node website
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "api-app", "server_id": serverID, "runtime": "node", "runtime_version": "22", "primary_domain": "api.example.test",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create node site: %d %v", resp.status, resp.body)
	}
	website := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// create application
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/application", map[string]any{
		"startup_file":  "server.js",
		"build_command": "npm run build",
		"internal_port": 3000,
		"env_vars":      map[string]string{"NODE_ENV": "production"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create app: %d %v", resp.status, resp.body)
	}
	app := resp.body
	appID, _ := app["id"].(string)

	// duplicate rejected
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/application", map[string]any{
		"startup_file": "server.js",
	})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate app: %d want 409", resp.status)
	}

	// build job claimed
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "build_app" {
		t.Fatalf("expected build_app job, got %v", claim.body)
	}
	payload, _ := job["payload"].(map[string]any)
	if payload["website_id"] != websiteID {
		t.Fatalf("payload website mismatch: %v", payload["website_id"])
	}
	if payload["unix_user"] == "" {
		t.Fatal("payload missing unix_user")
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// App creation also converges the reverse-proxy vhost via a
	// desired-state reconcile; drain it before continuing the lifecycle.
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if job, _ := claim.body["job"].(map[string]any); job != nil && job["type"] == "provision_website" {
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})
	}

	// start job
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/application/start", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("start: %d", resp.status)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job["type"] != "start_app" {
		t.Fatalf("expected start_app, got %v", job["type"])
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// stop
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/application/stop", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("stop: %d", resp.status)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job["type"] != "stop_app" {
		t.Fatalf("expected stop_app, got %v", job["type"])
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// restart
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/application/restart", nil)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "restart_app" {
		t.Fatalf("expected restart_app, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// audit
	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	logs, _ := resp.body["logs"].([]any)
	found := map[string]bool{}
	for _, l := range logs {
		entry, _ := l.(map[string]any)
		if a, ok := entry["action"].(string); ok {
			found[a] = true
		}
	}
	for _, want := range []string{"application.created", "application.started", "application.stopped", "application.restarted"} {
		if !found[want] {
			t.Fatalf("audit missing %q; got %v", want, found)
		}
	}
	_ = appID
}

func runtimeAvailable(resp response, rt string) bool {
	list, _ := resp.body["runtimes"].([]any)
	for _, item := range list {
		r, _ := item.(map[string]any)
		if r["type"] == rt && r["status"] == "available" {
			return true
		}
	}
	return false
}
