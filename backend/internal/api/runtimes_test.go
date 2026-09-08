package api

import (
	"net/http"
	"testing"
)

// TestRuntimeLifecycle covers Phase 4:
// install php 8.3 via job -> available; website requires installed runtime;
// version switch (reconcile job); removal blocked while in use, allowed after.
func TestRuntimeLifecycle(t *testing.T) {
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

	// --- 1. Validation: bad type, bad version ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "ruby", "version": "3.2"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad type: %d want 422", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "php", "version": "eight.three"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad version: %d want 422", resp.status)
	}

	// --- 2. Install php 8.3 -> 202 + installing row ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "php", "version": "8.3"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("install request: %d %v", resp.status, resp.body)
	}
	runtime := resp.body
	if runtime["status"] != "installing" {
		t.Fatalf("runtime status: %v want installing", runtime["status"])
	}
	runtimeID, _ := runtime["id"].(string)

	// duplicate request rejected
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "php", "version": "8.3"})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate runtime: %d want 409", resp.status)
	}

	// --- 3. Agent claims install job, reports success -> available ---
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "install_runtime" {
		t.Fatalf("expected install_runtime job, got %v", claim.body)
	}
	jobID, _ := job["id"].(string)
	resp = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})
	if resp.status != http.StatusOK {
		t.Fatalf("job result: %d %v", resp.status, resp.body)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", nil)
	if !runtimeHasStatus(t, resp, "8.3", "available") {
		t.Fatalf("runtime 8.3 not available after install: %v", resp.body)
	}

	// --- 4. Website creation requires an installed runtime ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "phpapp", "server_id": serverID, "runtime": "php", "runtime_version": "8.2",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("uninstalled runtime version: %d want 422", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "phpapp", "server_id": serverID, "runtime": "php", "runtime_version": "8.3",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create php website: %d %v", resp.status, resp.body)
	}
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)

	// missing runtime_version for php rejected
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "nover", "server_id": serverID, "runtime": "php",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("php without version: %d want 422", resp.status)
	}

	// --- 5. Provision the php site (pool creation happens agent-side in real life) ---
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "provision_website" {
		t.Fatalf("expected provision job, got %v", claim.body)
	}
	jobID, _ = job["id"].(string)
	resp = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" {
		t.Fatalf("php site not ready: %v", resp.body)
	}

	// --- 6. Removal of 8.3 is refused while the site uses it ---
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes/"+runtimeID, nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("in-use removal: %d want 409", resp.status)
	}

	// --- 7. Install 8.2, then PATCH the website to switch versions (reconcile) ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "php", "version": "8.2"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("install 8.2: %d", resp.status)
	}
	// drain the install job so the queue is clean
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if j, _ := claim.body["job"].(map[string]any); j != nil && j["type"] == "install_runtime" {
		jid, _ := j["id"].(string)
		agent.do("POST", "/v1/agent/jobs/"+jid+"/result", map[string]any{"success": true})
	}
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID, map[string]string{"runtime_version": "8.2"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("patch version: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "provision_website" {
		t.Fatalf("expected reconcile provision job, got %v", claim.body)
	}
	jobID, _ = job["id"].(string)
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["runtime_version"] != "8.2" {
		t.Fatalf("website runtime_version: %v want 8.2", resp.body["runtime_version"])
	}

	// --- 8. Now 8.3 removal succeeds (refcount dropped to 0) ---
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes/"+runtimeID, nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("unused removal: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "remove_runtime" {
		t.Fatalf("expected remove_runtime job, got %v", claim.body)
	}
	jobID, _ = job["id"].(string)
	resp = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})
	if resp.status != http.StatusOK {
		t.Fatalf("remove job result: %d", resp.status)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", nil)
	if runtimeHasStatus(t, resp, "8.3", "") {
		// 8.3 row must be gone; only 8.2 remains.
		t.Fatalf("runtime 8.3 row still present after removal: %v", resp.body)
	}
}

// runtimeHasStatus returns true if the runtimes list response contains an
// entry for version with the given status. Empty status matches "any".
func runtimeHasStatus(t *testing.T, resp response, version, status string) bool {
	t.Helper()
	list, _ := resp.body["runtimes"].([]any)
	for _, item := range list {
		rt, _ := item.(map[string]any)
		v, _ := rt["version"].(string)
		s, _ := rt["status"].(string)
		if v == version && (status == "" || s == status) {
			return true
		}
	}
	return false
}
