package api

import (
	"net/http"
	"testing"
)

// TestDeploymentAndStagingLifecycle covers Phase 8:
// deploy config, deploy job flow, deployment history, rollback guard,
// staging creation + clone + promote flows.
func TestDeploymentAndStagingLifecycle(t *testing.T) {
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

	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "app", "server_id": serverID, "primary_domain": "app.example.test",
	})
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// --- 1. Deploy without config -> 422 ---
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/deploy", nil)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("deploy unconfigured: %d want 422", resp.status)
	}

	// --- 2. Config + deploy ---
	resp = dev.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/deployment-config", map[string]any{
		"repo_url": "https://github.com/example/app.git", "branch": "main", "deploy_token": "ghp_secret123",
	})
	if resp.status != http.StatusNoContent {
		t.Fatalf("deploy config: %d %v", resp.status, resp.body)
	}
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/deploy", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("deploy: %d %v", resp.status, resp.body)
	}
	deployment := resp.body
	depID, _ := deployment["id"].(string)
	if deployment["status"] != "pending" {
		t.Fatalf("deployment status: %v want pending", deployment["status"])
	}

	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "deploy_website" {
		t.Fatalf("expected deploy_website job, got %v", claim.body)
	}
	// Deployment row must be running after claim.
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/deployments", nil)
	deps, _ := resp.body["deployments"].([]any)
	firstDep, _ := deps[0].(map[string]any)
	if firstDep["status"] != "running" {
		t.Fatalf("deployment after claim: %v want running", firstDep["status"])
	}
	resp = agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"commit_sha": "abc123", "release_dir": "/srv/epicpanel/releases/" + websiteID + "/r1", "log": "- ok"},
	})
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/deployments", nil)
	deps, _ = resp.body["deployments"].([]any)
	firstDep, _ = deps[0].(map[string]any)
	if firstDep["status"] != "successful" || firstDep["commit_sha"] != "abc123" {
		t.Fatalf("deployment after success: %v", firstDep)
	}

	// --- 3. Rollback before any other deploy: last successful is itself ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/rollback", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("rollback: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "rollback_website" {
		t.Fatalf("expected rollback job, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"release_dir": "/srv/epicpanel/releases/" + websiteID + "/r1"},
	})
	_ = depID

	// --- 4. Staging creation + clone + promote ---
	// Attach a database to the website first (simulate ready db row via flow).
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/databases", map[string]any{
		"name": "appdb", "server_id": serverID, "engine": "mariadb", "website_id": websiteID,
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create db: %d %v", resp.status, resp.body)
	}
	db := resp.body
	dbID, _ := db["id"].(string)
	dbName, _ := db["name"].(string)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "create_database" {
		t.Fatalf("expected create_database job, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true, "result": map[string]string{"password": "dbpass-abc123"},
	})
	_ = dbID

	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/staging", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("create staging: %d %v", resp.status, resp.body)
	}
	staging, _ := resp.body["staging"].(map[string]any)
	stagingID, _ := staging["id"].(string)
	if staging["is_staging"] != true {
		t.Fatalf("staging flag: %v", staging["is_staging"])
	}

	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "clone_staging" {
		t.Fatalf("expected clone_staging job, got %v", claim.body)
	}
	payload, _ := job["payload"].(map[string]any)
	dbPairs, _ := payload["databases"].([]any)
	if len(dbPairs) != 1 {
		t.Fatalf("clone payload databases: %v (want 1 pair with _stg suffix)", payload["databases"])
	}
	pair, _ := dbPairs[0].(map[string]any)
	if pair["target_name"] != dbName+"_stg" {
		t.Fatalf("staging db name: %v want %s_stg", pair["target_name"], dbName)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"files_copied": true, "databases": []string{dbName + "->" + dbName + "_stg"}},
	})

	// Promote (developer forbidden, admin allowed)
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites/"+stagingID+"/promote", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("dev promote: %d want 403", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+stagingID+"/promote", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("promote: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "promote_staging" {
		t.Fatalf("expected promote_staging job, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// --- 5. Audit trail ---
	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	logs, _ := resp.body["logs"].([]any)
	actions := map[string]bool{}
	for _, l := range logs {
		entry, _ := l.(map[string]any)
		if a, ok := entry["action"].(string); ok {
			actions[a] = true
		}
	}
	for _, want := range []string{"deployment.triggered", "deployment.rollback_triggered", "website.staging_created", "website.staging_promoted"} {
		if !actions[want] {
			t.Fatalf("audit missing %q; got %v", want, actions)
		}
	}
}
