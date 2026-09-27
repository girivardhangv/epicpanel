package api

import (
	"net/http"
	"strings"
	"testing"
)

// newPHPTestSite boots a server, installs PHP 8.3 and creates a ready-
// tracked website (job completed) for config-endpoint testing.
func newPHPTestSite(t *testing.T, admin, agent *testClient, orgID string) (serverID, websiteID string) {
	t.Helper()

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "cfg-web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "cfg-web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID = enroll.body["server"].(map[string]any)["id"].(string)

	admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/runtimes", map[string]string{"type": "php", "version": "8.3"})
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if j, _ := claim.body["job"].(map[string]any); j != nil {
		agent.do("POST", "/v1/agent/jobs/"+j["id"].(string)+"/result", map[string]any{"success": true})
	}

	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "cfgsite", "server_id": serverID, "runtime": "php", "runtime_version": "8.3",
		"primary_domain": "cfg.example.test",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("create php site: %d %v", resp.status, resp.body)
	}
	website := resp.body["website"].(map[string]any)
	websiteID, _ = website["id"].(string)
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if j, _ := claim.body["job"].(map[string]any); j != nil {
		agent.do("POST", "/v1/agent/jobs/"+j["id"].(string)+"/result", map[string]any{
			"success": true,
			"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
		})
	}
	return serverID, websiteID
}

func TestSiteConfigLifecycle(t *testing.T) {
	_, admin := newTestServer(t)
	dev := admin.NewClient()

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "cfg-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "CfgOrg"})
	orgID, _ := resp.body["id"].(string)

	dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "cfg-dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "cfg-dev@example.test", "role": "developer",
	})
	agent := admin.NewClient()
	_, websiteID := newPHPTestSite(t, admin, agent, orgID)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID + "/config"

	wordpress := map[string]any{
		"config": map[string]any{
			"schema": 2,
			"root_location": map[string]any{
				"try_files": "$uri $uri/ /index.php?$query_string",
			},
			"headers": []map[string]any{
				{"name": "X-Frame-Options", "value": "SAMEORIGIN", "always": true},
			},
			"client_max_body_size": "64m",
		},
	}
	resp = dev.do("PUT", base, wordpress)
	if resp.status != http.StatusOK {
		t.Fatalf("save config: %d %v", resp.status, resp.body)
	}
	if v, _ := resp.body["version"].(float64); v != 1 {
		t.Fatalf("first save should be version 1, got %v", resp.body["version"])
	}

	// GET returns both the structured document and the legacy key.
	resp = dev.do("GET", base, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("get config: %d %v", resp.status, resp.body)
	}
	cfgBody, _ := resp.body["config"].(map[string]any)
	if cfgBody == nil || cfgBody["root_location"] == nil {
		t.Fatalf("structured config not returned: %v", resp.body)
	}
	if _, ok := resp.body["rewrite_rules"]; !ok {
		t.Errorf("legacy rewrite_rules key must stay present for old clients: %v", resp.body)
	}

	// Second save bumps the version.
	wordpressCfg := wordpress["config"].(map[string]any)
	wordpressCfg["client_max_body_size"] = "128m"
	resp = dev.do("PUT", base, wordpress)
	if resp.status != http.StatusOK {
		t.Fatalf("second save: %d %v", resp.status, resp.body)
	}
	if v, _ := resp.body["version"].(float64); v != 2 {
		t.Fatalf("second save should be version 2, got %v", resp.body["version"])
	}

	// History lists both, newest first.
	resp = dev.do("GET", base+"/versions", nil)
	versions, _ := resp.body["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %v", resp.body)
	}
	if first, _ := versions[0].(map[string]any)["version"].(float64); first != 2 {
		t.Fatalf("versions must be newest-first, got %v", versions)
	}

	// Rollback to version 1 → creates version 3 with the old document.
	resp = dev.do("POST", base+"/rollback", map[string]any{"to_version": 1})
	if resp.status != http.StatusOK {
		t.Fatalf("rollback: %d %v", resp.status, resp.body)
	}
	if v, _ := resp.body["version"].(float64); v != 3 {
		t.Fatalf("rollback must create version 3, got %v", resp.body["version"])
	}
	if m, _ := resp.body["version"].(float64); m == 1 {
		t.Fatal("version numbers never decrease")
	}

	// Validate endpoint: dry-run OK, and warnings for would-drop sections.
	resp = dev.do("POST", base+"/validate", wordpress)
	if resp.status != http.StatusOK {
		t.Fatalf("validate: %d %v", resp.status, resp.body)
	}
	if valid, _ := resp.body["valid"].(bool); !valid {
		t.Fatalf("valid config must validate: %v", resp.body)
	}

	// Conflicting location: friendly, section-routing error.
	bad := map[string]any{
		"config": map[string]any{
			"schema": 2,
			"locations": []map[string]any{
				{"id": "l1", "kind": "prefix", "match": "/", "body": "expires 1h"},
			},
		},
	}
	resp = dev.do("PUT", base, bad)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("conflicting location must 422, got %d %v", resp.status, resp.body)
	}
	if msg, _ := resp.body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "Root Location") {
		t.Fatalf("error must route to the Root Location section, got: %s", msg)
	}

	// Restricted directive: actionable message, not "unsupported".
	badInclude := map[string]any{
		"config": map[string]any{"schema": 2, "server_directives": "include /etc/nginx/nginx.conf"},
	}
	resp = dev.do("PUT", base, badInclude)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("include must 422, got %d %v", resp.status, resp.body)
	}
	if msg, _ := resp.body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "platform-managed") {
		t.Fatalf("include error must be actionable, got: %s", msg)
	}

	// The failed saves must not have bumped the version or stored anything.
	resp = dev.do("GET", base+"/versions", nil)
	versions, _ = resp.body["versions"].([]any)
	if len(versions) != 3 {
		t.Fatalf("failed saves must not create versions, got %v", resp.body)
	}

	// Cross-tenant: a second org's dev cannot read or write the config.
	// (Register on the other client — register auto-logs-in on that client.)
	other := admin.NewClient()
	resp = other.do("POST", "/v1/auth/register", map[string]string{
		"email": "cfg-other@example.test", "password": "supersecret123", "name": "Other",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register other: %d %v", resp.status, resp.body)
	}
	resp2 := admin.do("POST", "/v1/organizations", map[string]string{"name": "OtherOrg"})
	otherOrg, _ := resp2.body["id"].(string)
	admin.do("POST", "/v1/organizations/"+otherOrg+"/members", map[string]string{
		"email": "cfg-other@example.test", "role": "developer",
	})
	resp = other.do("GET", "/v1/organizations/"+otherOrg+"/websites/"+websiteID+"/config", nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("cross-tenant config read must 404, got %d", resp.status)
	}
	resp = other.do("PUT", "/v1/organizations/"+otherOrg+"/websites/"+websiteID+"/config", wordpress)
	if resp.status != http.StatusNotFound {
		t.Fatalf("cross-tenant config write must 404, got %d", resp.status)
	}

	// Billing can read but not write. Register on the client itself —
	// register auto-logs-in (stealing an inherited cookie otherwise).
	biller := admin.NewClient()
	resp = biller.do("POST", "/v1/auth/register", map[string]string{
		"email": "cfg-bill@example.test", "password": "supersecret123", "name": "Bill",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register billing: %d %v", resp.status, resp.body)
	}
	add := admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "cfg-bill@example.test", "role": "billing",
	})
	if add.status != http.StatusCreated {
		t.Fatalf("add billing member: %d %v", add.status, add.body)
	}
	resp = biller.do("GET", base, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("billing read: %d %v", resp.status, resp.body)
	}
	resp = biller.do("PUT", base, wordpress)
	if resp.status != http.StatusForbidden {
		t.Fatalf("billing write must 403, got %d", resp.status)
	}

	// Legacy endpoint: contract unchanged (server-context snippet) and it
	// converges like before.
	resp = dev.do("PUT", base+"/rewrite", map[string]string{
		"rewrite_rules": "rewrite ^/legacy /target permanent",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("legacy rewrite save: %d %v", resp.status, resp.body)
	}
	resp = dev.do("PUT", base+"/rewrite", map[string]string{
		"rewrite_rules": "location /x {",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("legacy location block must still 422 with Locations hint, got %d", resp.status)
	}
}
