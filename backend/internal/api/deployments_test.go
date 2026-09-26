package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
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
	// The deploy-success fanout converges serving (running directory takes
	// effect with the release): drain the provision job it enqueues.
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if job, _ = claim.body["job"].(map[string]any); job != nil && job["type"] == "provision_website" {
		agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
			"success": true,
			"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
		})
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

// TestDeployWebDirAndServingConverge covers the running-directory feature:
// web_dir on the deployment config (Forge-style repo-relative web dir, e.g.
// "public" for Laravel), its validation, its ride on the deploy job payload,
// and the serving-converge contract — reconcileWebsiteServing must keep BOTH
// the site docroot suffix and the deploy web dir (anti-drift: a domain or
// SSL change used to re-render the vhost with the running dir dropped).
func TestDeployWebDirAndServingConverge(t *testing.T) {
	srv, admin := newTestServer(t)
	agent := admin.NewClient()

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "webdir-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "LaravelCo"})
	orgID, _ := resp.body["id"].(string)
	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "shop", "server_id": enroll.body["server"].(map[string]any)["id"],
		"primary_domain": "shop.example.test",
	})
	website, _ := resp.body["website"].(map[string]any)
	if website == nil {
		t.Fatalf("website create failed: %d %v", resp.status, resp.body)
	}
	websiteID, _ := website["id"].(string)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID

	// Complete the initial provision job so later claims reach the deploy.
	claim0 := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job0, _ := claim0.body["job"].(map[string]any)
	if job0 == nil || job0["type"] != "provision_website" {
		t.Fatalf("expected initial provision job, got %v", claim0.body)
	}
	agent.do("POST", "/v1/agent/jobs/"+job0["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// Config with a running directory + private-repo token.
	resp = admin.do("PATCH", base+"/deployment-config", map[string]any{
		"repo_url": "https://github.com/example/laravel-app.git", "branch": "main",
		"deploy_token": "ghp_secret123", "web_dir": "public",
	})
	if resp.status != http.StatusNoContent {
		t.Fatalf("deploy config: %d %v", resp.status, resp.body)
	}
	resp = admin.do("GET", base, nil)
	if resp.body["deploy_web_dir"] != "public" {
		t.Fatalf("site must surface deploy_web_dir, got %v", resp.body["deploy_web_dir"])
	}

	// Traversal / nonsense is refused.
	for _, bad := range []string{"../etc", "a/b/c/d/e"} {
		resp = admin.do("PATCH", base+"/deployment-config", map[string]any{"web_dir": bad})
		if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
			t.Fatalf("web_dir %q must be rejected, got %d", bad, resp.status)
		}
	}

	// The deploy job payload carries the running directory.
	resp = admin.do("POST", base+"/deploy", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("deploy: %d %v", resp.status, resp.body)
	}
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "deploy_website" {
		t.Fatalf("claim: %v", claim.body)
	}
	var dp map[string]any
	switch pv := job["payload"].(type) {
	case string:
		if raw, err := base64.StdEncoding.DecodeString(pv); err == nil {
			_ = json.Unmarshal(raw, &dp)
		}
	case map[string]any:
		dp = pv
	}
	if dp["web_dir"] != "public" {
		t.Fatalf("deploy payload must carry web_dir=public, got %v", dp["web_dir"])
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true, "result": map[string]any{"commit_sha": "deadbee", "release_dir": "/srv/x", "log": ""},
	})

	// Serving converge keeps the running directory: mark a site docroot
	// suffix (one-click-Laravel-style) AND keep deploy_web_dir, reconcile,
	// then inspect the pending provision payload for BOTH values.
	if err := srv.Websites.SetDocrootSuffix(context.Background(),
		uuid.MustParse(websiteID), "app/public"); err != nil {
		t.Fatalf("set docroot suffix: %v", err)
	}
	srv.reconcileWebsiteServing(context.Background(), uuid.MustParse(websiteID),
		uuid.MustParse(orgID), uuid.MustParse(enroll.body["server"].(map[string]any)["id"].(string)))
	var payloadRaw []byte
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE website_id = $1 AND type = 'provision_website'
		 ORDER BY created_at DESC LIMIT 1`, uuid.MustParse(websiteID)).Scan(&payloadRaw); err != nil {
		t.Fatalf("read provision payload: %v", err)
	}
	var pp map[string]any
	if err := json.Unmarshal(payloadRaw, &pp); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if pp["docroot_suffix"] != "app/public" {
		t.Fatalf("reconcile must carry docroot_suffix (vhost drift bug), got %v", pp["docroot_suffix"])
	}
	if pp["web_dir"] != "public" {
		t.Fatalf("reconcile must carry web_dir (vhost drift bug), got %v", pp["web_dir"])
	}
}

// TestRunningDirectoryConvergence — the two convergence paths for web_dir:
// (1) a successful deploy immediately re-renders the serving config (the
// running dir takes effect with the release it belongs to, not an hour
// later); (2) a web_dir change on a site WITH a release also converges,
// while a change on a never-deployed site must NOT (the new docroot would
// resolve to an empty directory and 403 the site before its first deploy).
func TestRunningDirectoryConvergence(t *testing.T) {
	srv, admin := newTestServer(t)
	agent := admin.NewClient()
	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "conv-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "ConvCo"})
	orgID, _ := resp.body["id"].(string)
	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID := enroll.body["server"].(map[string]any)["id"].(string)
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "conv", "server_id": serverID, "primary_domain": "conv.example.test",
	})
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ := website["id"].(string)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID

	pendingProvisions := func() int {
		var n int
		if err := srv.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM jobs WHERE website_id = $1 AND type = 'provision_website' AND status = 'pending'`,
			websiteID).Scan(&n); err != nil {
			t.Fatalf("count provisions: %v", err)
		}
		return n
	}
	// Drain the initial provision job.
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})

	// web_dir change on a NEVER-DEPLOYED site: no immediate converge.
	admin.do("PATCH", base+"/deployment-config", map[string]any{
		"repo_url": "https://github.com/example/app.git", "branch": "main", "web_dir": "public",
	})
	if n := pendingProvisions(); n != 0 {
		t.Fatalf("web_dir change without a release must not converge, got %d pending provisions", n)
	}

	// Deploy: claim + report success -> the fanout must converge (1 pending
	// provision carrying web_dir).
	admin.do("POST", base+"/deploy", nil)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	if job["type"] != "deploy_website" {
		t.Fatalf("expected deploy job, got %v", job["type"])
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true, "result": map[string]any{"commit_sha": "f00d", "release_dir": "/srv/x", "log": ""},
	})
	if n := pendingProvisions(); n != 1 {
		t.Fatalf("successful deploy must converge serving, got %d pending provisions", n)
	}
	var payloadRaw []byte
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE website_id = $1 AND type = 'provision_website' AND status = 'pending'
		 ORDER BY created_at DESC LIMIT 1`, websiteID).Scan(&payloadRaw); err != nil {
		t.Fatalf("read provision payload: %v", err)
	}
	var pp map[string]any
	if err := json.Unmarshal(payloadRaw, &pp); err != nil {
		t.Fatal(err)
	}
	if pp["web_dir"] != "public" {
		t.Fatalf("post-deploy converge must carry web_dir, got %v", pp["web_dir"])
	}

	// Complete the converge, then change web_dir again: WITH a release the
	// change converges immediately.
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ = claim.body["job"].(map[string]any)
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public/public"},
	})
	admin.do("PATCH", base+"/deployment-config", map[string]any{"web_dir": "site/public"})
	if n := pendingProvisions(); n != 1 {
		t.Fatalf("web_dir change with a release must converge, got %d pending provisions", n)
	}
}
