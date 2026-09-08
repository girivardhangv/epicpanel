package api

import (
	"net/http"
	"testing"
)

// TestDomainAndSSLLifecycle covers Phase 7:
// alias add/remove with global uniqueness, DNS verification flow,
// SSL mode transitions with cert issuance jobs, vhost reconcile payloads.
func TestDomainAndSSLLifecycle(t *testing.T) {
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

	// Site with primary domain
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "shop", "server_id": serverID, "primary_domain": "shop.example.test",
	})
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)
	// provision job queued at creation; claim + succeed it
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	jobID, _ := job["id"].(string)
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// --- 1. Primary domain auto-registered ---
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", nil)
	domains, _ := resp.body["domains"].([]any)
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain (primary), got %d", len(domains))
	}
	primary, _ := domains[0].(map[string]any)
	if primary["kind"] != "primary" || primary["domain"] != "shop.example.test" {
		t.Fatalf("primary domain: %v", primary)
	}
	primaryID, _ := primary["id"].(string)

	// --- 2. Global domain uniqueness: second website can't claim the primary ---
	resp2 := admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "other", "server_id": serverID, "primary_domain": "other.example.test",
	})
	otherWebsite, _ := resp2.body["website"].(map[string]any)
	otherID, _ := otherWebsite["id"].(string)
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+otherID+"/domains", map[string]string{
		"domain": "shop.example.test", "kind": "alias",
	})
	if resp.status != http.StatusConflict {
		t.Fatalf("global domain uniqueness: %d want 409", resp.status)
	}
	_ = otherID

	// --- 3. invalid hostname rejected ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", map[string]string{
		"domain": "Not A Hostname",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad hostname: %d want 422", resp.status)
	}

	// --- 4. SSL mode set -> issue_certificate job -> agent succeeds -> active ---
	// (a vhost-reconcile provision job may also be queued; drain provisions
	// until the cert job is found)
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/domains/"+primaryID+"/ssl", map[string]string{"mode": "selfsigned"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("set ssl: %d %v", resp.status, resp.body)
	}
	if resp.body["ssl_state"] != "issuing" {
		t.Fatalf("ssl state: %v want issuing", resp.body["ssl_state"])
	}
	var certJobID string
	for i := 0; i < 5 && certJobID == ""; i++ {
		claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
		job, _ = claim.body["job"].(map[string]any)
		if job == nil {
			break
		}
		switch job["type"] {
		case "issue_certificate":
			certJobID = job["id"].(string)
		case "provision_website":
			agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})
		default:
			t.Fatalf("unexpected job type %v", job["type"])
		}
	}
	if certJobID == "" {
		t.Fatal("issue_certificate job never claimed")
	}
	resp = agent.do("POST", "/v1/agent/jobs/"+certJobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"domain": "shop.example.test", "not_after": "2027-01-01T00:00:00Z", "issuer": "self-signed"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("cert result: %d", resp.status)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/domains/"+primaryID+"/ssl", nil)
	_ = resp // no GET ssl endpoint; verify via domains list
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", nil)
	domains, _ = resp.body["domains"].([]any)
	primary, _ = domains[0].(map[string]any)
	if primary["ssl_state"] != "active" {
		t.Fatalf("ssl state after success: %v want active", primary["ssl_state"])
	}

	// --- 5. Alias add triggers a vhost reconcile with BOTH domains in payload ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", map[string]string{
		"domain": "www.shop.example.test", "kind": "alias",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("add alias: %d %v", resp.status, resp.body)
	}
	aliasID := resp.body["id"].(string)
	// Drain reconciles (cert-activation one may precede the alias one).
	found := false
	for i := 0; i < 5 && !found; i++ {
		claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
		job, _ = claim.body["job"].(map[string]any)
		if job == nil {
			break
		}
		if job["type"] != "provision_website" {
			t.Fatalf("expected provision_website, got %v", job["type"])
		}
		payloadMap, _ := job["payload"].(map[string]any)
		domainList, _ := payloadMap["domains"].([]any)
		if len(domainList) == 2 {
			found = true
		}
		jobID, _ = job["id"].(string)
		agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true})
	}
	if !found {
		t.Fatal("reconcile with primary+alias payload never seen")
	}

	// --- 6. DNS verification flow ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/domains/"+primaryID+"/verify-dns", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("verify dns: %d", resp.status)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "verify_domain" {
		t.Fatalf("expected verify_domain job, got %v", claim.body)
	}
	jobID, _ = job["id"].(string)
	agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"matches": true, "resolved": []string{"203.0.113.10"}},
	})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", nil)
	domains, _ = resp.body["domains"].([]any)
	primary, _ = domains[0].(map[string]any)
	if primary["dns_points_to_server"] != true {
		t.Fatalf("dns_points_to_server: %v", primary["dns_points_to_server"])
	}

	// --- 7. Failed issuance marks failed with error ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/domains/"+aliasID+"/ssl", map[string]string{"mode": "letsencrypt"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("set alias ssl: %d", resp.status)
	}
	for attempt := 0; attempt < 3; attempt++ {
		claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
		job, _ = claim.body["job"].(map[string]any)
		if job == nil {
			t.Fatalf("expected cert job claim attempt %d", attempt+1)
		}
		if job["type"] != "issue_certificate" {
			t.Fatalf("expected issue_certificate, got %v", job["type"])
		}
		jobID, _ = job["id"].(string)
		agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
			"success": false, "error": "acme: dns problem",
		})
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", nil)
	domains, _ = resp.body["domains"].([]any)
	for _, item := range domains {
		d, _ := item.(map[string]any)
		if d["id"] == aliasID {
			if d["ssl_state"] != "failed" {
				t.Fatalf("alias ssl_state: %v want failed", d["ssl_state"])
			}
			if d["ssl_error"] == "" {
				t.Fatal("ssl_error should be recorded")
			}
		}
	}

	// --- 8. Alias removal -> reconcile; audit trail ---
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains/"+aliasID, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("delete alias: %d", resp.status)
	}
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains/"+primaryID, nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("delete primary: %d want 409 (primary not deletable)", resp.status)
	}
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "provision_website" {
		t.Fatalf("expected reconcile after alias removal, got %v", claim.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{"success": true})

	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	logs, _ := resp.body["logs"].([]any)
	actions := map[string]bool{}
	for _, l := range logs {
		entry, _ := l.(map[string]any)
		if a, ok := entry["action"].(string); ok {
			actions[a] = true
		}
	}
	for _, want := range []string{"domain.added", "domain.removed", "domain.ssl_mode_set", "domain.dns_check_requested"} {
		if !actions[want] {
			t.Fatalf("audit missing %q; got %v", want, actions)
		}
	}
}
