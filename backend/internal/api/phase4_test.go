package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// phase4Setup enrolls a server and returns the shared handles the Phase-4
// tests need (mirrors TestWebsiteLifecycle's setup).
func phase4Setup(t *testing.T) (orgID, serverID, websiteID string, admin, agent *testClient) {
	t.Helper()
	_, admin = newTestServer(t)

	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	if resp.status != http.StatusOK && resp.status != http.StatusCreated {
		t.Fatalf("register: %d %v", resp.status, resp.body)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ = resp.body["id"].(string)

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent = admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	agentToken, _ := enroll.body["agent_token"].(string)
	setBearer(agent, agentToken)
	serverID = enroll.body["server"].(map[string]any)["id"].(string)

	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "shop", "server_id": serverID, "runtime": "static", "primary_domain": "shop.example.test",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create website: %d %v", resp.status, resp.body)
	}
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ = website["id"].(string)
	unixUser, _ := website["unix_user"].(string)
	jobID, _ := resp.body["job_id"].(string)

	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if claim.status != http.StatusOK {
		t.Fatalf("claim: %d %v", claim.status, claim.body)
	}
	res := agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": unixUser, "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})
	if res.status != http.StatusOK {
		t.Fatalf("report success: %d %v", res.status, res.body)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" {
		t.Fatalf("website not ready: %v", resp.body["status"])
	}
	return orgID, serverID, websiteID, admin, agent
}

// TestPhase4SuspendResume covers the lifecycle DoD: provision -> suspend ->
// resume under the job pipeline, plus role and status guards.
func TestPhase4SuspendResume(t *testing.T) {
	orgID, _, websiteID, admin, agent := phase4Setup(t)

	// Suspend: 202 + job; status unchanged until the agent reports success.
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("suspend: %d %v", resp.status, resp.body)
	}
	suspendJobID, _ := resp.body["job_id"].(string)
	if suspendJobID == "" {
		t.Fatalf("suspend job id missing: %v", resp.body)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" {
		t.Fatalf("status must stay ready until agent success: %v", resp.body["status"])
	}

	// Double suspend is rejected while one is already in flight? No: the job
	// is idempotent-keyed, so a second suspend enqueues nothing new but is
	// still 202. Resume while ready must be a 409.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("resume while ready: %d want 409", resp.status)
	}

	// Agent claims + succeeds -> suspended.
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if claim.status != http.StatusOK {
		t.Fatalf("claim suspend: %d %v", claim.status, claim.body)
	}
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "suspend_website" {
		t.Fatalf("claimed job wrong: %v", claim.body)
	}
	res := agent.do("POST", "/v1/agent/jobs/"+suspendJobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"suspended": true},
	})
	if res.status != http.StatusOK {
		t.Fatalf("suspend result: %d %v", res.status, res.body)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "suspended" {
		t.Fatalf("status after suspend success: %v want suspended", resp.body["status"])
	}

	// Suspend while suspended -> 409. Resume works.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend", nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("suspend while suspended: %d want 409", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("resume: %d %v", resp.status, resp.body)
	}
	resumeJobID, _ := resp.body["job_id"].(string)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if claim.status != http.StatusOK {
		t.Fatalf("claim resume: %d", claim.status)
	}
	res = agent.do("POST", "/v1/agent/jobs/"+resumeJobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"resumed": true},
	})
	if res.status != http.StatusOK {
		t.Fatalf("resume result: %d", res.status)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" {
		t.Fatalf("status after resume: %v want ready", resp.body["status"])
	}

	// Failed resume keeps suspended.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("suspend 2: %d", resp.status)
	}
	sj, _ := resp.body["job_id"].(string)
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+sj+"/result", map[string]any{"success": true, "result": map[string]any{}})
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil)
	rj, _ := resp.body["job_id"].(string)
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+rj+"/result", map[string]any{"success": false, "error": "restart failed"})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "suspended" {
		t.Fatalf("failed resume must keep suspended: %v", resp.body["status"])
	}
}

// TestPhase4AliasesAndRedirects covers alias CRUD and redirect management +
// reconcile enqueue on change.
func TestPhase4AliasesAndRedirects(t *testing.T) {
	orgID, _, websiteID, admin, _ := phase4Setup(t)

	// Add alias.
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", map[string]any{
		"kind": "alias", "domain": "blog.example.test",
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("alias create: %d %v", resp.status, resp.body)
	}
	aliasID, _ := resp.body["id"].(string)
	if aliasID == "" {
		if nested, _ := resp.body["domain"].(map[string]any); nested != nil {
			aliasID, _ = nested["id"].(string)
		}
	}
	if aliasID == "" {
		t.Fatalf("alias body: %v", resp.body)
	}

	// Duplicate alias rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains", map[string]any{
		"kind": "alias", "domain": "blog.example.test",
	})
	if resp.status != http.StatusConflict && resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate alias: %d", resp.status)
	}

	// Redirect against the site's domain.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/redirects", map[string]any{
		"from_domain": "blog.example.test", "to_url": "https://docs.example.test", "status_code": 301,
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("redirect create: %d %v", resp.status, resp.body)
	}
	rdID, _ := resp.body["id"].(string)
	if rdID == "" {
		if nested, _ := resp.body["redirect"].(map[string]any); nested != nil {
			rdID, _ = nested["id"].(string)
		}
	}
	if rdID == "" {
		t.Fatalf("redirect body: %v", resp.body)
	}

	// Invalid target URL rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/redirects", map[string]any{
		"from_domain": "blog.example.test", "to_url": "javascript:alert(1)",
	})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("bad redirect target: %d", resp.status)
	}

	// List includes it.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/redirects", nil)
	list, _ := resp.body["redirects"].([]any)
	if len(list) != 1 {
		t.Fatalf("redirect list: %v", resp.body)
	}

	// Disable via PATCH.
	resp = admin.do("PATCH", "/v1/organizations/"+orgID+"/redirects/"+rdID, map[string]any{"enabled": false})
	if resp.status != http.StatusOK {
		t.Fatalf("redirect patch: %d %v", resp.status, resp.body)
	}

	// Delete redirect + alias.
	if resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/redirects/"+rdID, nil); resp.status != http.StatusOK && resp.status != http.StatusNoContent {
		t.Fatalf("redirect delete: %d", resp.status)
	}
	if resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/domains/"+aliasID, nil); resp.status != http.StatusOK && resp.status != http.StatusNoContent {
		t.Fatalf("alias delete: %d %v", resp.status, resp.body)
	}
}

// TestPhase4DNSZone covers zone lifecycle: create -> records -> validation ->
// publish enqueue.
func TestPhase4DNSZone(t *testing.T) {
	orgID, _, websiteID, admin, agent := phase4Setup(t)

	// Zone for the primary domain.
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dns-zone", map[string]any{
		"domain": "shop.example.test",
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("zone create: %d %v", resp.status, resp.body)
	}
	zoneID, _ := resp.body["id"].(string)
	if zoneID == "" {
		if nested, _ := resp.body["zone"].(map[string]any); nested != nil {
			zoneID, _ = nested["id"].(string)
		}
	}
	if zoneID == "" {
		t.Fatalf("zone body: %v", resp.body)
	}

	// Duplicate zone rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dns-zone", map[string]any{
		"domain": "shop.example.test",
	})
	if resp.status != http.StatusConflict && resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate zone: %d", resp.status)
	}

	// A record at the apex.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/dns-zones/"+zoneID+"/records", map[string]any{
		"name": "@", "type": "A", "value": "192.0.2.10",
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("record create: %d %v", resp.status, resp.body)
	}
	recID, _ := resp.body["id"].(string)
	if recID == "" {
		if rec, _ := resp.body["record"].(map[string]any); rec != nil {
			recID, _ = rec["id"].(string)
		}
	}

	// Invalid value rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/dns-zones/"+zoneID+"/records", map[string]any{
		"name": "v4", "type": "A", "value": "not-an-ip",
	})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("bad A value: %d", resp.status)
	}
	// MX requires numeric priority.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/dns-zones/"+zoneID+"/records", map[string]any{
		"name": "@", "type": "MX", "value": "mail.example.test.", "priority": 10,
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("MX create: %d %v", resp.status, resp.body)
	}

	// Get zone + records.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/dns-zone", nil)
	z, _ := resp.body["zone"].(map[string]any)
	if z == nil {
		t.Fatalf("zone get: %v", resp.body)
	}
	records, _ := resp.body["records"].([]any)
	if len(records) != 2 {
		t.Fatalf("records: %v", resp.body)
	}

	// Publish enqueues sync_dns_zone with a serial key.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/dns-zones/"+zoneID+"/publish", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("publish: %d %v", resp.status, resp.body)
	}
	pubJobID, _ := resp.body["job_id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "sync_dns_zone" {
		t.Fatalf("publish claim: %v", claim.body)
	}
	var payload struct {
		WebsiteID string `json:"website_id"`
		Zone      struct {
			Domain string `json:"domain"`
			Serial int64  `json:"serial"`
		} `json:"zone"`
		Records []map[string]any `json:"records"`
	}
	pb, _ := json.Marshal(job["payload"])
	if err := json.Unmarshal(pb, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.Zone.Domain != "shop.example.test" || payload.Zone.Serial <= 0 || len(payload.Records) != 2 {
		t.Fatalf("publish payload wrong: %s", pb)
	}
	_ = agent.do("POST", "/v1/agent/jobs/"+pubJobID+"/result", map[string]any{
		"success": true,
		"result":  map[string]any{"records": 2, "serial": payload.Zone.Serial},
	})

	// Record delete + zone delete.
	if resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/dns-records/"+recID, nil); resp.status != http.StatusOK && resp.status != http.StatusNoContent {
		t.Fatalf("record delete: %d", resp.status)
	}
	if resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/dns-zones/"+zoneID, nil); resp.status != http.StatusOK && resp.status != http.StatusNoContent {
		t.Fatalf("zone delete: %d", resp.status)
	}
}

// TestPhase4FTPAccounts covers account CRUD, one-time password semantics and
// the sync job payload carrying one-way hashes only.
func TestPhase4FTPAccounts(t *testing.T) {
	orgID, _, websiteID, admin, agent := phase4Setup(t)

	// Create SFTP account with an explicit password.
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/ftp-accounts", map[string]any{
		"protocol": "sftp", "label": "deploy", "password": "s3cret-Passw0rd",
	})
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		t.Fatalf("ftp create: %d %v", resp.status, resp.body)
	}
	acct, _ := resp.body["account"].(map[string]any)
	if acct == nil {
		acct = resp.body
	}
	acctID, _ := acct["id"].(string)
	firstPassword, _ := resp.body["password"].(string)
	if firstPassword == "" {
		firstPassword, _ = acct["password"].(string)
	}
	if firstPassword == "" {
		t.Fatalf("create must return the password once: %v", resp.body)
	}

	// List does NOT include passwords.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/ftp-accounts", nil)
	list, _ := resp.body["ftp_accounts"].([]any)
	if len(list) != 1 {
		t.Fatalf("ftp list: %v", resp.body)
	}
	row, _ := list[0].(map[string]any)
	if _, has := row["password"]; has {
		t.Fatalf("list must not include passwords: %v", row)
	}

	// The sync job payload carries only a $6$ hash — never plaintext.
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "sync_ftp_accounts" {
		t.Fatalf("ftp claim: %v", claim.body)
	}
	pb, _ := json.Marshal(job["payload"])
	if want := `"password":"s3cret-Passw0rd"`; len(pb) > 0 && containsString(string(pb), want) {
		t.Fatalf("plaintext password leaked in job payload: %s", pb)
	}
	var payload struct {
		WebsiteID string `json:"website_id"`
		Accounts  []struct {
			UserName      string `json:"user_name"`
			Protocol      string `json:"protocol"`
			PasswordCrypt string `json:"password_crypt"`
			HomeDir       string `json:"home_dir"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(pb, &payload); err != nil {
		t.Fatalf("payload decode: %v (%s)", err, pb)
	}
	if len(payload.Accounts) != 1 {
		t.Fatalf("payload accounts: %s", pb)
	}
	a := payload.Accounts[0]
	if a.Protocol != "sftp" || len(a.PasswordCrypt) < 20 || a.PasswordCrypt[:3] != "$6$" {
		t.Fatalf("account spec wrong: %+v", a)
	}
	if a.UserName[:7] != "ep-ftp-" {
		t.Fatalf("username prefix: %q", a.UserName)
	}
	_ = agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true, "result": map[string]any{"created": 1},
	})

	// Rotate -> new password returned once; new sync enqueued.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/ftp-accounts/"+acctID+"/password", map[string]any{
		"password": "NewPassword99",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("rotate: %d %v", resp.status, resp.body)
	}
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil) // drain sync job

	// Reveal (audited).
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/ftp-accounts/"+acctID+"/reveal", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("reveal: %d %v", resp.status, resp.body)
	}

	// Bad protocol rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/ftp-accounts", map[string]any{
		"protocol": "telnet", "label": "x",
	})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("bad protocol: %d", resp.status)
	}

	// Delete.
	if resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/ftp-accounts/"+acctID, nil); resp.status != http.StatusOK && resp.status != http.StatusNoContent {
		t.Fatalf("ftp delete: %d", resp.status)
	}
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil) // drain final sync
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
