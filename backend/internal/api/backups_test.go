package api

import (
	"net/http"
	"testing"
)

// TestBackupAndMonitoringLifecycle covers Phase 9+10:
// manual backup flow with db attachment, retention config, restore,
// health checks + alerts endpoints.
func TestBackupAndMonitoringLifecycle(t *testing.T) {
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

	// --- 1. Manual backup without databases ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backups", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("create backup: %d %v", resp.status, resp.body)
	}
	backup := resp.body
	backupID, _ := backup["id"].(string)

	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "create_backup" {
		t.Fatalf("expected create_backup job, got %v", claim.body)
	}
	// running after claim
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backups", nil)
	backups, _ := resp.body["backups"].([]any)
	b0, _ := backups[0].(map[string]any)
	if b0["status"] != "running" {
		t.Fatalf("backup after claim: %v want running", b0["status"])
	}
	resp = agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"archive_path": "/srv/epicpanel/backups/" + backupID, "size_bytes": 4096, "databases": []string{}},
	})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backups", nil)
	backups, _ = resp.body["backups"].([]any)
	b0, _ = backups[0].(map[string]any)
	if b0["status"] != "successful" {
		t.Fatalf("backup after success: %v want successful", b0["status"])
	}
	if b0["size_bytes"] != float64(4096) {
		t.Fatalf("backup size: %v", b0["size_bytes"])
	}

	// --- 2. Backup config: daily with retention 3 ---
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backup-config", map[string]any{
		"schedule": "daily", "retention": 3,
	})
	if resp.status != http.StatusNoContent {
		t.Fatalf("backup config: %d", resp.status)
	}
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backup-config", map[string]any{
		"schedule": "hourly",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad schedule: %d want 422", resp.status)
	}

	// --- 3. Restore (admin only) ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/backups/"+backupID+"/restore", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("restore: %d %v", resp.status, resp.body)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "restore_backup" {
		t.Fatalf("expected restore_backup job, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	// --- 4. Health check + alert flow (simulated via agent? no: direct endpoint) ---
	// The monitoring checker runs server-side; here we verify the endpoints
	// exist and return empty state.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/health", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("health: %d", resp.status)
	}
	if _, ok := resp.body["checks"]; !ok {
		t.Fatal("health response missing checks")
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/alerts", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("alerts: %d", resp.status)
	}
	if _, ok := resp.body["alerts"]; !ok {
		t.Fatal("alerts response missing alerts")
	}

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
	for _, want := range []string{"backup.created", "backup.restore_requested", "website.backup_config_updated"} {
		if !actions[want] {
			t.Fatalf("audit missing %q; got %v", want, actions)
		}
	}
}
