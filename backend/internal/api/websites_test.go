package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestWebsiteLifecycle exercises the desired-state flow:
// create website -> job queued -> agent claims -> reports success -> ready,
// then delete -> job queued -> agent claims -> reports success -> row removed.
// Agent actions use the registration/enrollment path from Phase 2.
func TestWebsiteLifecycle(t *testing.T) {
	_, admin := newTestServer(t)
	dev := admin.NewClient()
	outsider := admin.NewClient()

	// --- setup: admin, org, dev member, outsider, enrolled server ---
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
	outsider.do("POST", "/v1/auth/register", map[string]string{
		"email": "out@example.test", "password": "supersecret123", "name": "Out",
	})

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent := admin.NewClient() // will become the agent client (Bearer)
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	agentToken, _ := enroll.body["agent_token"].(string)
	setBearer(agent, agentToken)

	// --- 1. Non-admin users cannot create sites (cPanel account model) ---
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "denied",
	})
	if resp.status != http.StatusForbidden {
		t.Fatalf("dev site creation: %d want 403", resp.status)
	}

	// --- 2. Create website (admin only) ---
	serverID := enroll.body["server"].(map[string]any)["id"].(string)
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "shop", "server_id": serverID, "runtime": "static", "primary_domain": "shop.example.test",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create website: %d %v", resp.status, resp.body)
	}
	website, _ := resp.body["website"].(map[string]any)
	if website["status"] != "pending" {
		t.Fatalf("website status: %v want pending", website["status"])
	}
	unixUser, _ := website["unix_user"].(string)
	if unixUser == "" {
		t.Fatalf("unix_user missing: %v", website)
	}
	jobID, _ := resp.body["job_id"].(string)

	// --- 2b. Invalid names/domains rejected (also admin-only) ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "Bad Name!", "server_id": serverID,
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad name: %d", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "okname", "server_id": serverID, "primary_domain": "not a domain",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad domain: %d", resp.status)
	}

	// --- 4. Outsider cannot view the website ---
	websiteID, _ := website["id"].(string)
	resp = outsider.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("outsider view: %d want 404", resp.status)
	}

	// --- 5. Agent claims the job; website becomes provisioning ---
	claimResp := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claimResp.body["job"].(map[string]any)
	if job == nil {
		t.Fatalf("expected job claim, got: %v", claimResp.body)
	}
	if job["type"] != "provision_website" {
		t.Fatalf("job type: %v", job["type"])
	}
	var payload struct {
		UnixUser string `json:"unix_user"`
		Name     string `json:"name"`
	}
	payloadBytes, err := json.Marshal(job["payload"])
	if err != nil {
		t.Fatalf("payload re-marshal: %v", err)
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.UnixUser != unixUser {
		t.Fatalf("payload unix_user %q != website unix_user %q", payload.UnixUser, unixUser)
	}

	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "provisioning" {
		t.Fatalf("status after claim: %v want provisioning", resp.body["status"])
	}

	// --- 6. Agent reports success; website becomes ready ---
	resp = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": unixUser, "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("report success: %d %v", resp.status, resp.body)
	}

	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" {
		t.Fatalf("status after success: %v want ready", resp.body["status"])
	}
	if resp.body["document_root"] != "/srv/epicpanel/websites/"+websiteID+"/public" {
		t.Fatalf("document_root: %v", resp.body["document_root"])
	}

	// --- 7. A failed provision marks the website failed (retry then terminal) ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "broken", "server_id": serverID,
	})
	broken, _ := resp.body["website"].(map[string]any)
	brokenID, _ := broken["id"].(string)
	claimResp = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job2, _ := claimResp.body["job"].(map[string]any)
	if job2 == nil {
		t.Fatal("expected second job claim")
	}
	job2ID, _ := job2["id"].(string)

	// First failure attempt -> job goes back to pending.
	failResp := agent.do("POST", "/v1/agent/jobs/"+job2ID+"/result", map[string]any{
		"success": false, "error": "simulated transient failure",
	})
	t.Logf("failure report response: status=%d job=%v", failResp.status, failResp.body)
	if got, _ := failResp.body["job"].(map[string]any); got != nil {
		if got["status"] != "pending" {
			t.Fatalf("job status after attempt-1 failure: %v want pending (attempts=%v max=%v)", got["status"], got["attempts"], got["max_attempts"])
		}
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+brokenID, nil)
	if resp.body["status"] != "provisioning" {
		t.Fatalf("status after retryable failure: %v want provisioning (job should be pending again)", resp.body["status"])
	}

	// Fail twice more (attempts 2 and 3); the third failure is terminal.
	for attempt := 2; attempt <= 3; attempt++ {
		claimResp = agent.do("POST", "/v1/agent/jobs/claim", nil)
		jobN, _ := claimResp.body["job"].(map[string]any)
		if jobN == nil {
			t.Fatalf("expected job reclaimed for attempt %d", attempt)
		}
		if attempt == 3 && jobN["id"] != job2ID {
			t.Fatalf("expected same job reclaimed, got: %v", claimResp.body)
		}
		agent.do("POST", "/v1/agent/jobs/"+job2ID+"/result", map[string]any{
			"success": false, "error": "simulated failure",
		})
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+brokenID, nil)
	if resp.body["status"] != "failed" {
		t.Fatalf("status after terminal failure: %v want failed", resp.body["status"])
	}

	// --- 8. Delete flow: admin deletes; agent claims delete job; row removed ---
	// (developer cannot delete)
	resp = dev.do("DELETE", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("dev delete: %d want 403", resp.status)
	}
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("admin delete: %d", resp.status)
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "deleting" {
		t.Fatalf("status after delete request: %v want deleting", resp.body["status"])
	}

	claimResp = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job3, _ := claimResp.body["job"].(map[string]any)
	if job3 == nil || job3["type"] != "delete_website" {
		t.Fatalf("expected delete job, got: %v", claimResp.body)
	}
	job3ID, _ := job3["id"].(string)
	resp = agent.do("POST", "/v1/agent/jobs/"+job3ID+"/result", map[string]any{"success": true})
	if resp.status != http.StatusOK {
		t.Fatalf("report delete success: %d", resp.status)
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("website after delete: %d want 404", resp.status)
	}

	// --- 9. Job history retained for the deleted website? (row deleted -> jobs orphaned; acceptable) ---
	// Audit trail must contain website events.
	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	logs, _ := resp.body["logs"].([]any)
	actions := map[string]bool{}
	for _, l := range logs {
		entry, _ := l.(map[string]any)
		if a, ok := entry["action"].(string); ok {
			actions[a] = true
		}
	}
	for _, want := range []string{"website.created", "job.claimed", "job.success", "website.delete_requested"} {
		if !actions[want] {
			t.Fatalf("audit missing %q; got %v", want, actions)
		}
	}
}
