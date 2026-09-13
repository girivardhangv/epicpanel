package api

// Phase 12 — Security Hardening bridge tests. Wires the authzmatrix and
// securityaudit packages to the real server (hooks), then proves the
// mandatory checklist items against the live handler: RBAC route matrix,
// secret-leak scan, session security, and rate limiting.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/authzmatrix"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/securityaudit"
)

// phase12Routes is the full registered route inventory (audited against the
// mux registrations in server.go + handler Register funcs). The bridge
// asserts every entry is live on the real handler (OPTIONS -> 405) and feeds
// authzmatrix.RouteInventory. ServeMux cannot enumerate its own patterns, so
// this table IS the drift gate: removing/renaming a route without updating
// it fails the registration probe.
var phase12Routes = []string{
	"DELETE /v1/admin/alert-rules/{rule_id}",
	"DELETE /v1/admin/packages/{pkg_id}",
	"DELETE /v1/organizations/{org_id}/api-tokens/{token_id}",
	"DELETE /v1/organizations/{org_id}/backup-schedules/{schedule_id}",
	"DELETE /v1/organizations/{org_id}/backup-targets/{target_id}",
	"DELETE /v1/organizations/{org_id}/bots/{bot_id}",
	"DELETE /v1/organizations/{org_id}/bots/{bot_id}/env/{key}",
	"DELETE /v1/organizations/{org_id}/bots/{bot_id}/schedules/{schedule_id}",
	"DELETE /v1/organizations/{org_id}/crons/{cron_id}",
	"DELETE /v1/organizations/{org_id}/databases/{db_id}",
	"DELETE /v1/organizations/{org_id}/dns-records/{record_id}",
	"DELETE /v1/organizations/{org_id}/dns-zones/{zone_id}",
	"DELETE /v1/organizations/{org_id}/ftp-accounts/{account_id}",
	"DELETE /v1/organizations/{org_id}/members/{user_id}",
	"DELETE /v1/organizations/{org_id}/minecraft/{instance_id}",
	"DELETE /v1/organizations/{org_id}/minecraft/{instance_id}/schedules/{schedule_id}",
	"DELETE /v1/organizations/{org_id}/redirects/{redirect_id}",
	"DELETE /v1/organizations/{org_id}/servers/{server_id}",
	"DELETE /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}",
	"DELETE /v1/organizations/{org_id}/service-accounts/{sa_id}",
	"DELETE /v1/organizations/{org_id}/ssh-keys/{key_id}",
	"DELETE /v1/organizations/{org_id}/websites/{website_id}",
	"DELETE /v1/organizations/{org_id}/websites/{website_id}/domains/{domain_id}",
	"DELETE /v1/organizations/{org_id}/websites/{website_id}/files",
	"GET /healthz",
	"GET /metrics",
	"GET /readyz",
	"GET /v1/admin/alert-rules",
	"GET /v1/admin/alerts",
	"GET /v1/admin/billing/invoices",
	"GET /v1/admin/billing/plans",
	"GET /v1/admin/billing/products",
	"GET /v1/admin/billing/settings",
	"GET /v1/admin/billing/subscriptions",
	"GET /v1/admin/observability/customers",
	"GET /v1/admin/observability/nodes",
	"GET /v1/admin/observability/services",
	"GET /v1/admin/observability/workloads",
	"GET /v1/admin/packages",
	"GET /v1/admin/users",
	"GET /v1/adminview/accounts",
	"GET /v1/adminview/alerts",
	"GET /v1/adminview/backups",
	"GET /v1/adminview/databases",
	"GET /v1/adminview/dns-zones",
	"GET /v1/adminview/domains",
	"GET /v1/adminview/jobs",
	"GET /v1/adminview/jobs/dead-letter",
	"GET /v1/adminview/live",
	"GET /v1/adminview/organizations",
	"GET /v1/adminview/overview",
	"GET /v1/adminview/ports",
	"GET /v1/adminview/servers",
	"GET /v1/adminview/users",
	"GET /v1/agent/stream",
	"GET /v1/audit-logs",
	"GET /v1/auth/me",
	"GET /v1/jobs",
	"GET /v1/openapi.json",
	"GET /v1/organizations",
	"GET /v1/organizations/{org_id}",
	"GET /v1/organizations/{org_id}/alerts",
	"GET /v1/organizations/{org_id}/api-tokens",
	"GET /v1/organizations/{org_id}/backup-schedules",
	"GET /v1/organizations/{org_id}/backup-targets",
	"GET /v1/organizations/{org_id}/backups2",
	"GET /v1/organizations/{org_id}/billing/invoices",
	"GET /v1/organizations/{org_id}/billing/invoices/{invoice_id}",
	"GET /v1/organizations/{org_id}/billing/orders",
	"GET /v1/organizations/{org_id}/billing/overview",
	"GET /v1/organizations/{org_id}/billing/payment-method",
	"GET /v1/organizations/{org_id}/billing/products",
	"GET /v1/organizations/{org_id}/billing/subscriptions",
	"GET /v1/organizations/{org_id}/billing/subscriptions/{subscription_id}",
	"GET /v1/organizations/{org_id}/bots",
	"GET /v1/organizations/{org_id}/bots/runtime-offers",
	"GET /v1/organizations/{org_id}/bots/{bot_id}",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/console",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/console/ws",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/env",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/files",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/jobs",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/logs",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/metrics",
	"GET /v1/organizations/{org_id}/bots/{bot_id}/schedules",
	"GET /v1/organizations/{org_id}/databases",
	"GET /v1/organizations/{org_id}/databases/{db_id}",
	"GET /v1/organizations/{org_id}/databases/{db_id}/credentials",
	"GET /v1/organizations/{org_id}/databases/{db_id}/pma-sso",
	"GET /v1/organizations/{org_id}/domains",
	"GET /v1/organizations/{org_id}/members",
	"GET /v1/organizations/{org_id}/minecraft",
	"GET /v1/organizations/{org_id}/minecraft/provider-offers",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/backups",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/console",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/console/ws",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/files",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/jobs",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/metrics",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/properties",
	"GET /v1/organizations/{org_id}/minecraft/{instance_id}/schedules",
	"GET /v1/organizations/{org_id}/package",
	"GET /v1/organizations/{org_id}/servers",
	"GET /v1/organizations/{org_id}/servers/capacity",
	"GET /v1/organizations/{org_id}/servers/metrics",
	"GET /v1/organizations/{org_id}/servers/{server_id}",
	"GET /v1/organizations/{org_id}/servers/{server_id}/jobs",
	"GET /v1/organizations/{org_id}/servers/{server_id}/metrics",
	"GET /v1/organizations/{org_id}/servers/{server_id}/metrics/history",
	"GET /v1/organizations/{org_id}/servers/{server_id}/runtimes",
	"GET /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions",
	"GET /v1/organizations/{org_id}/service-accounts",
	"GET /v1/organizations/{org_id}/websites",
	"GET /v1/organizations/{org_id}/websites/{website_id}",
	"GET /v1/organizations/{org_id}/websites/{website_id}/application",
	"GET /v1/organizations/{org_id}/websites/{website_id}/application/logs",
	"GET /v1/organizations/{org_id}/websites/{website_id}/application/status",
	"GET /v1/organizations/{org_id}/websites/{website_id}/backups",
	"GET /v1/organizations/{org_id}/websites/{website_id}/config",
	"GET /v1/organizations/{org_id}/websites/{website_id}/crons",
	"GET /v1/organizations/{org_id}/websites/{website_id}/deployments",
	"GET /v1/organizations/{org_id}/websites/{website_id}/dns-zone",
	"GET /v1/organizations/{org_id}/websites/{website_id}/domains",
	"GET /v1/organizations/{org_id}/websites/{website_id}/files",
	"GET /v1/organizations/{org_id}/websites/{website_id}/files/content",
	"GET /v1/organizations/{org_id}/websites/{website_id}/files/download",
	"GET /v1/organizations/{org_id}/websites/{website_id}/ftp-accounts",
	"GET /v1/organizations/{org_id}/websites/{website_id}/health",
	"GET /v1/organizations/{org_id}/websites/{website_id}/jobs",
	"GET /v1/organizations/{org_id}/websites/{website_id}/redirects",
	"GET /v1/organizations/{org_id}/websites/{website_id}/ssh-keys",
	"GET /v1/organizations/{org_id}/websites/{website_id}/terminal",
	"GET /v1/organizations/{org_id}/websites/{website_id}/usage",
	"GET /v1/pma-gate",
	"GET /v1/settings",
	"GET /v1/setup/jobs",
	"GET /v1/setup/software",
	"GET /v1/setup/status",
	"GET /v1/ws",
	"PATCH /v1/admin/alert-rules/{rule_id}",
	"PATCH /v1/admin/billing/products/{product_id}",
	"PATCH /v1/admin/billing/settings",
	"PATCH /v1/admin/packages/{pkg_id}",
	"PATCH /v1/organizations/{org_id}",
	"PATCH /v1/organizations/{org_id}/bots/{bot_id}",
	"PATCH /v1/organizations/{org_id}/crons/{cron_id}",
	"PATCH /v1/organizations/{org_id}/dns-records/{record_id}",
	"PATCH /v1/organizations/{org_id}/domains/{domain_id}",
	"PATCH /v1/organizations/{org_id}/members/{user_id}",
	"PATCH /v1/organizations/{org_id}/minecraft/{instance_id}",
	"PATCH /v1/organizations/{org_id}/redirects/{redirect_id}",
	"PATCH /v1/organizations/{org_id}/servers/{server_id}/maintenance",
	"PATCH /v1/organizations/{org_id}/websites/{website_id}",
	"PATCH /v1/organizations/{org_id}/websites/{website_id}/application",
	"PATCH /v1/organizations/{org_id}/websites/{website_id}/backup-config",
	"PATCH /v1/organizations/{org_id}/websites/{website_id}/deployment-config",
	"PATCH /v1/organizations/{org_id}/websites/{website_id}/files",
	"PATCH /v1/settings/hostname",
	"POST /v1/admin/alert-rules",
	"POST /v1/admin/alerts/{alert_id}/ack",
	"POST /v1/admin/alerts/{alert_id}/resolve",
	"POST /v1/admin/billing/invoices",
	"POST /v1/admin/billing/invoices/{invoice_id}/void",
	"POST /v1/admin/billing/products",
	"POST /v1/admin/billing/subscriptions/{subscription_id}/retry",
	"POST /v1/admin/billing/subscriptions/{subscription_id}/terminate",
	"POST /v1/admin/organizations/{org_id}/package",
	"POST /v1/admin/packages",
	"POST /v1/admin/users",
	"POST /v1/adminview/accounts/{website_id}/resume",
	"POST /v1/adminview/accounts/{website_id}/suspend",
	"POST /v1/adminview/jobs/{job_id}/cancel",
	"POST /v1/adminview/jobs/{job_id}/retry",
	"POST /v1/agent/enroll",
	"POST /v1/agent/heartbeat",
	"POST /v1/agent/jobs/claim",
	"POST /v1/agent/jobs/{job_id}/progress",
	"POST /v1/agent/jobs/{job_id}/result",
	"POST /v1/auth/login",
	"POST /v1/auth/logout",
	"POST /v1/auth/mfa/disable",
	"POST /v1/auth/mfa/enable",
	"POST /v1/auth/mfa/setup",
	"POST /v1/auth/mfa/verify",
	"POST /v1/auth/register",
	"POST /v1/billing/webhook/{provider}",
	"POST /v1/organizations",
	"POST /v1/organizations/{org_id}/api-tokens",
	"POST /v1/organizations/{org_id}/backup-schedules",
	"POST /v1/organizations/{org_id}/backup-targets",
	"POST /v1/organizations/{org_id}/backups/{backup_id}/restore",
	"POST /v1/organizations/{org_id}/backups2",
	"POST /v1/organizations/{org_id}/backups2/{backup_id}/restore",
	"POST /v1/organizations/{org_id}/backups2/{backup_id}/verify",
	"POST /v1/organizations/{org_id}/billing/invoices/{invoice_id}/pay",
	"POST /v1/organizations/{org_id}/billing/orders",
	"POST /v1/organizations/{org_id}/billing/orders/{order_id}/pay",
	"POST /v1/organizations/{org_id}/billing/subscriptions/{subscription_id}/cancel",
	"POST /v1/organizations/{org_id}/bots",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/deploy-git",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/files/upload",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/kill",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/restart",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/schedules",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/start",
	"POST /v1/organizations/{org_id}/bots/{bot_id}/stop",
	"POST /v1/organizations/{org_id}/databases",
	"POST /v1/organizations/{org_id}/dns-zones/{zone_id}/publish",
	"POST /v1/organizations/{org_id}/dns-zones/{zone_id}/records",
	"POST /v1/organizations/{org_id}/domains/{domain_id}/ssl",
	"POST /v1/organizations/{org_id}/domains/{domain_id}/verify-dns",
	"POST /v1/organizations/{org_id}/ftp-accounts/{account_id}/password",
	"POST /v1/organizations/{org_id}/ftp-accounts/{account_id}/reveal",
	"POST /v1/organizations/{org_id}/members",
	"POST /v1/organizations/{org_id}/minecraft",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/backups",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/backups/{backup_id}/restore",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/console/command",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/files/upload",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/kill",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/restart",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/schedules",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/start",
	"POST /v1/organizations/{org_id}/minecraft/{instance_id}/stop",
	"POST /v1/organizations/{org_id}/servers",
	"POST /v1/organizations/{org_id}/servers/{server_id}/database-tools",
	"POST /v1/organizations/{org_id}/servers/{server_id}/detect-software",
	"POST /v1/organizations/{org_id}/servers/{server_id}/registration-token",
	"POST /v1/organizations/{org_id}/servers/{server_id}/runtimes",
	"POST /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions",
	"POST /v1/organizations/{org_id}/service-accounts",
	"POST /v1/organizations/{org_id}/websites",
	"POST /v1/organizations/{org_id}/websites/{website_id}/application",
	"POST /v1/organizations/{org_id}/websites/{website_id}/application/restart",
	"POST /v1/organizations/{org_id}/websites/{website_id}/application/start",
	"POST /v1/organizations/{org_id}/websites/{website_id}/application/stop",
	"POST /v1/organizations/{org_id}/websites/{website_id}/backups",
	"POST /v1/organizations/{org_id}/websites/{website_id}/crons",
	"POST /v1/organizations/{org_id}/websites/{website_id}/deploy",
	"POST /v1/organizations/{org_id}/websites/{website_id}/dns-zone",
	"POST /v1/organizations/{org_id}/websites/{website_id}/domains",
	"POST /v1/organizations/{org_id}/websites/{website_id}/files",
	"POST /v1/organizations/{org_id}/websites/{website_id}/files/upload",
	"POST /v1/organizations/{org_id}/websites/{website_id}/ftp-accounts",
	"POST /v1/organizations/{org_id}/websites/{website_id}/promote",
	"POST /v1/organizations/{org_id}/websites/{website_id}/redirects",
	"POST /v1/organizations/{org_id}/websites/{website_id}/resume",
	"POST /v1/organizations/{org_id}/websites/{website_id}/rollback",
	"POST /v1/organizations/{org_id}/websites/{website_id}/ssh-keys",
	"POST /v1/organizations/{org_id}/websites/{website_id}/staging",
	"POST /v1/organizations/{org_id}/websites/{website_id}/suspend",
	"POST /v1/organizations/{org_id}/websites/{website_id}/wordpress",
	"POST /v1/setup",
	"POST /v1/setup/software",
	"POST /v1/setup/verify-hostname",
	"PUT /v1/organizations/{org_id}/billing/payment-method",
	"PUT /v1/organizations/{org_id}/bots/{bot_id}/env",
	"PUT /v1/organizations/{org_id}/minecraft/{instance_id}/properties",
	"PUT /v1/organizations/{org_id}/websites/{website_id}/config/rewrite",
	"PUT /v1/organizations/{org_id}/websites/{website_id}/files/content",
}

// probeUUID substitutes route wildcards for live probing.
const probeUUID = "00000000-0000-0000-0000-000000000001"

func concretePath(pattern string) string {
	for _, seg := range []string{"org_id", "website_id", "server_id", "user_id", "bot_id", "instance_id", "db_id", "domain_id", "record_id", "zone_id", "account_id", "key_id", "token_id", "sa_id", "cron_id", "job_id", "alert_id", "rule_id", "schedule_id", "backup_id", "runtime_id", "subscription_id", "order_id", "invoice_id", "product_id", "pkg_id", "provider", "id"} {
		pattern = strings.ReplaceAll(pattern, "{"+seg+"}", probeUUID)
	}
	return pattern
}

// rawResp captures status + headers + raw body (the secret-leak scan needs
// bytes, not decoded JSON).
type rawResp struct {
	status int
	header http.Header
	body   string
}

func (r rawResp) decoded() map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// rawDo issues a request against the client's cookie jar with full response
// capture. Mutations carry the CSRF header unless explicitly suppressed.
func rawDo(c *testClient, method, path string, payload any, csrf bool, extra map[string]string) rawResp {
	c.t.Helper()
	var body *bytes.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-EpicPanel", "1")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return rawResp{status: resp.StatusCode, header: resp.Header, body: buf.String()}
}

// TestPhase12AuthzMatrix is the RBAC centerpiece: it feeds the route
// inventory + probe handler into authzmatrix and runs the live matrix
// against a fully provisioned tenant set (admin + two orgs + API token +
// agent token).
func TestPhase12AuthzMatrix(t *testing.T) {
	srv, admin := newTestServer(t)
	ctx := context.Background()
	// --- tenants: platform admin + org A; plain user + org B ---
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "root@phase12.test", "password": "supersecret123", "name": "Root",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register admin: %d %v", resp.status, resp.body)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "OrgA"})
	orgA, _ := resp.body["id"].(string)
	if orgA == "" {
		t.Fatalf("create org A: %v", resp.body)
	}
	orgAUUID := mustParseUUID(t, orgA)

	userB := admin.NewClient()
	resp = userB.do("POST", "/v1/auth/register", map[string]string{
		"email": "member@phase12.test", "password": "supersecret123", "name": "Member",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register user B: %d %v", resp.status, resp.body)
	}
	resp = userB.do("POST", "/v1/organizations", map[string]string{"name": "OrgB"})
	orgB, _ := resp.body["id"].(string)
	if orgB == "" {
		t.Fatalf("create org B: %v", resp.body)
	}

	// --- org A API token (websites:read) + enrolled agent ---
	resp = admin.do("POST", "/v1/organizations/"+orgA+"/api-tokens", map[string]any{
		"name": "phase12-token", "scopes": []string{"websites:read"},
	})
	rawToken, _ := resp.body["raw_token"].(string)
	if rawToken == "" {
		t.Fatalf("api token create: %v", resp.body)
	}
	tokenClient := admin.NewClient()
	bearerClient(tokenClient, rawToken)

	resp = admin.do("POST", "/v1/organizations/"+orgA+"/servers", map[string]string{"name": "sec-node-01"})
	regToken, _ := resp.body["registration_token"].(string)
	if regToken == "" {
		t.Fatalf("server create: %v", resp.body)
	}
	agentClient := admin.NewClient()
	resp = agentClient.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "sec-node-01.local",
	})
	agentToken, _ := resp.body["agent_token"].(string)
	if agentToken == "" {
		t.Fatalf("agent enroll: %v", resp.body)
	}
	bearerClient(agentClient, agentToken)

	// --- wire the authzmatrix hooks to the real handler ---
	inventory := map[string]bool{}
	for _, r := range phase12Routes {
		inventory[r] = true
	}
	authzmatrix.RouteInventory = inventory
	authzmatrix.ProbeHandler = srv.Handler()

	t.Run("RouteTableCompleteness", authzmatrix.TestRouteTableCompleteness)
	t.Run("UnauthenticatedDenied", authzmatrix.TestUnauthenticatedDenied)

	// --- every inventoried route must be live on the real mux: an
	// unregistered-method request (PROPFIND) to a registered path yields
	// 405 with an Allow header, to an unregistered path 404. (OPTIONS is
	// answered by the CORS middleware and cannot probe the mux.) This
	// proves inventory <-> mux agreement.
	t.Run("RouteInventoryLive", func(t *testing.T) {
		probe := admin.NewClient() // anonymous copy: auth irrelevant for 405
		for _, r := range phase12Routes {
			parts := strings.SplitN(r, " ", 2)
			method, pattern := parts[0], parts[1]
			req, err := http.NewRequest("PROPFIND", admin.base+concretePath(pattern), nil)
			if err != nil {
				t.Fatalf("probe request %s: %v", r, err)
			}
			hresp, err := probe.http.Do(req)
			if err != nil {
				t.Fatalf("probe %s: %v", r, err)
			}
			code := hresp.StatusCode
			allow := hresp.Header.Get("Allow")
			hresp.Body.Close()
			if code != http.StatusMethodNotAllowed {
				t.Errorf("%s: PROPFIND returned %d, want 405 (route not registered?)", r, code)
				continue
			}
			if allow == "" {
				t.Errorf("%s: 405 without Allow header", r)
			}
			if !strings.Contains(allow, method) {
				t.Errorf("%s: Allow header %q missing declared method %s", r, allow, method)
			}
		}
	})

	// --- cross-tenant access must 404 (never 403 — no existence leak) ---
	t.Run("CrossTenantForbiddenIs404", func(t *testing.T) {
		type probe struct {
			method, path string
		}
		reads := []probe{
			{"GET", "/v1/organizations/" + orgA},
			{"GET", "/v1/organizations/" + orgA + "/websites"},
			{"POST", "/v1/organizations/" + orgA + "/websites"},
			{"GET", "/v1/organizations/" + orgA + "/servers"},
			{"GET", "/v1/organizations/" + orgA + "/databases"},
			{"GET", "/v1/organizations/" + orgA + "/domains"},
			{"GET", "/v1/organizations/" + orgA + "/package"},
			{"GET", "/v1/organizations/" + orgA + "/backups2"},
			{"POST", "/v1/organizations/" + orgA + "/backups2"},
			{"GET", "/v1/organizations/" + orgA + "/minecraft"},
			{"GET", "/v1/organizations/" + orgA + "/bots"},
			{"GET", "/v1/organizations/" + orgA + "/billing/subscriptions"},
			{"GET", "/v1/organizations/" + orgA + "/api-tokens"},
			{"GET", "/v1/organizations/" + orgA + "/members"},
			{"GET", "/v1/organizations/" + orgA + "/alerts"},
			{"GET", "/v1/audit-logs?organization_id=" + orgA},
		}
		for _, p := range reads {
			resp := userB.do(p.method, p.path, nil)
			if resp.status != http.StatusNotFound {
				t.Errorf("cross-tenant %s %s: %d, want 404 (body=%v)", p.method, p.path, resp.status, resp.body)
			}
		}
	})

	// --- positive control: the same reads succeed on the member's own org ---
	t.Run("OwnOrgReadable", func(t *testing.T) {
		for _, p := range []string{
			"/v1/organizations/" + orgB,
			"/v1/organizations/" + orgB + "/websites",
			"/v1/organizations/" + orgB + "/servers",
			"/v1/organizations/" + orgB + "/databases",
			"/v1/organizations/" + orgB + "/domains",
			"/v1/organizations/" + orgB + "/package",
			"/v1/organizations/" + orgB + "/backups2",
			"/v1/organizations/" + orgB + "/minecraft",
			"/v1/organizations/" + orgB + "/bots",
			"/v1/organizations/" + orgB + "/billing/subscriptions",
		} {
			resp := userB.do("GET", p, nil)
			if resp.status != http.StatusOK {
				t.Errorf("own org GET %s: %d %v, want 200", p, resp.status, resp.body)
			}
		}
	})

	// --- API tokens are org-confined: token(orgA) sees orgA, not orgB ---
	t.Run("APITokenOrgConfined", func(t *testing.T) {
		resp := tokenClient.do("GET", "/v1/organizations/"+orgA+"/websites", nil)
		if resp.status != http.StatusOK {
			t.Errorf("token on own org: %d %v want 200", resp.status, resp.body)
		}
		resp = tokenClient.do("GET", "/v1/organizations/"+orgB+"/websites", nil)
		if resp.status != http.StatusNotFound {
			t.Errorf("token cross-tenant: %d want 404", resp.status)
		}
	})

	// --- role gates inside the org: billing member is below developer and
	// org-admin routes (403), while org reads stay open ---
	t.Run("OrgRoleGates", func(t *testing.T) {
		resp := admin.do("POST", "/v1/organizations/"+orgA+"/members", map[string]string{
			"email": "member@phase12.test", "role": "billing",
		})
		if resp.status != http.StatusCreated {
			t.Fatalf("add member: %d %v", resp.status, resp.body)
		}
		type probe struct {
			method, path string
			want         int
		}
		cases := []probe{
			{"GET", "/v1/organizations/" + orgA + "/websites", http.StatusOK},
			{"POST", "/v1/organizations/" + orgA + "/websites", http.StatusForbidden},  // developer+
			{"GET", "/v1/organizations/" + orgA + "/minecraft", http.StatusOK},         // billing+
			{"POST", "/v1/organizations/" + orgA + "/minecraft", http.StatusForbidden}, // developer+
			{"GET", "/v1/organizations/" + orgA + "/backups2", http.StatusOK},          // billing+
			{"POST", "/v1/organizations/" + orgA + "/backups2", http.StatusForbidden},  // org admin+
			{"POST", "/v1/organizations/" + orgA + "/members", http.StatusForbidden},   // org admin+
			{"PATCH", "/v1/organizations/" + orgA, http.StatusForbidden},               // org admin+
		}
		for _, c := range cases {
			resp := userB.do(c.method, c.path, nil)
			if resp.status != c.want {
				t.Errorf("billing member %s %s: %d want %d", c.method, c.path, resp.status, c.want)
			}
		}
	})

	// --- admin surface: platform-admin SESSION only; members and API
	// tokens refused (403), unauthenticated 401 ---
	t.Run("AdminSurface", func(t *testing.T) {
		resp := userB.do("GET", "/v1/admin/users", nil)
		if resp.status != http.StatusForbidden {
			t.Errorf("admin users as member: %d want 403", resp.status)
		}
		resp = tokenClient.do("GET", "/v1/admin/users", nil)
		if resp.status != http.StatusForbidden {
			t.Errorf("admin users as API token: %d want 403", resp.status)
		}
		resp = tokenClient.do("GET", "/v1/admin/alerts", nil)
		if resp.status != http.StatusForbidden {
			t.Errorf("admin alerts as API token: %d want 403", resp.status)
		}
		resp = tokenClient.do("GET", "/v1/jobs", nil)
		if resp.status != http.StatusForbidden {
			t.Errorf("admin jobs as API token: %d want 403", resp.status)
		}
		resp = admin.do("GET", "/v1/admin/users", nil)
		if resp.status != http.StatusOK {
			t.Errorf("admin users as platform admin: %d %v want 200", resp.status, resp.body)
		}
	})

	// --- agent surface: agent tokens only. Sessions are refused (401),
	// API tokens fail earlier at the deny-by-default scope gate (403 —
	// no /v1/agent scope group exists), and a real agent token passes ---
	t.Run("AgentSurface", func(t *testing.T) {
		resp := userB.do("POST", "/v1/agent/heartbeat", nil)
		if resp.status != http.StatusUnauthorized {
			t.Errorf("heartbeat with session: %d want 401", resp.status)
		}
		resp = tokenClient.do("POST", "/v1/agent/heartbeat", nil)
		if resp.status == http.StatusOK {
			t.Error("heartbeat with API token must never succeed")
		}
		resp = agentClient.do("POST", "/v1/agent/heartbeat", nil)
		if resp.status != http.StatusOK {
			t.Errorf("heartbeat with agent token: %d %v want 200", resp.status, resp.body)
		}
	})

	// --- audit coverage: the mutations exercised above must each have
	// written an audit row (route-table-driven coverage check) ---
	t.Run("AuditCoverage", func(t *testing.T) {
		required := []string{"organization.created", "member.added", "api_token.created", "agent.enrolled", "user.registered"}
		rows, err := srv.Pool.Query(ctx, `SELECT DISTINCT action FROM audit_logs WHERE organization_id = $1 OR organization_id IS NULL`, orgAUUID)
		if err != nil {
			t.Fatalf("query audit: %v", err)
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				t.Fatalf("scan: %v", err)
			}
			seen[action] = true
		}
		for _, action := range required {
			if !seen[action] {
				t.Errorf("mutation without audit row: %s (have %v)", action, seen)
			}
		}
	})
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

// TestPhase12SecretLeak plants a password and an API token, exercises the
// auth + token + audit surfaces, and proves neither value ever appears in
// any response body, log line, or audit row.
func TestPhase12SecretLeak(t *testing.T) {
	srv, admin := newTestServer(t)

	plantedPassword := "Zq9!Tr0ut#Meadow7Kelp"

	// Hook the process-wide log sink: everything any subsystem logs during
	// the flow is captured and scanned.
	var logBuf bytes.Buffer
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	captured := map[string]string{} // label -> response body

	resp := rawDo(admin, "POST", "/v1/auth/register", map[string]string{
		"email": "leak@phase12.test", "password": plantedPassword, "name": "Leak",
	}, true, nil)
	if resp.status != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.status, resp.body)
	}
	captured["register"] = resp.body

	resp = rawDo(admin, "POST", "/v1/auth/login", map[string]string{
		"email": "leak@phase12.test", "password": plantedPassword,
	}, true, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("login: %d %s", resp.status, resp.body)
	}
	captured["login"] = resp.body

	// Failed login: the wrong password transits a request body and must not
	// surface in any log or response.
	rawDo(admin, "POST", "/v1/auth/login", map[string]string{
		"email": "leak@phase12.test", "password": plantedPassword + "-wrong",
	}, true, nil)

	resp = rawDo(admin, "GET", "/v1/auth/me", nil, false, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("me: %d %s", resp.status, resp.body)
	}
	captured["me"] = resp.body

	resp = rawDo(admin, "POST", "/v1/organizations", map[string]string{"name": "LeakOrg"}, true, nil)
	if resp.status != http.StatusCreated {
		t.Fatalf("org: %d %s", resp.status, resp.body)
	}
	captured["org-create"] = resp.body
	orgID, _ := resp.decoded()["id"].(string)

	resp = rawDo(admin, "POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "leak-probe", "scopes": []string{"websites:read"},
	}, true, nil)
	if resp.status != http.StatusCreated {
		t.Fatalf("token create: %d %s", resp.status, resp.body)
	}
	captured["token-create"] = resp.body
	plantedToken, _ := resp.decoded()["raw_token"].(string)
	if plantedToken == "" {
		t.Fatalf("no raw_token in create response: %s", resp.body)
	}

	resp = rawDo(admin, "GET", "/v1/organizations/"+orgID+"/api-tokens", nil, false, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("token list: %d %s", resp.status, resp.body)
	}
	captured["token-list"] = resp.body

	resp = rawDo(admin, "GET", "/v1/audit-logs?organization_id="+orgID, nil, false, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("audit logs: %d %s", resp.status, resp.body)
	}
	captured["audit-logs"] = resp.body

	// --- assertions ---
	// 1. The planted password appears in ZERO captured responses.
	findings := securityaudit.ScanPlant("responses", []string{plantedPassword}, captured["register"], captured["login"], captured["me"], captured["org-create"], captured["token-create"], captured["token-list"], captured["audit-logs"])
	for _, f := range findings {
		if f.Pattern == "planted_secret" {
			t.Errorf("planted password leaked into %s", f.Location)
		}
	}
	// 2. The raw API token appears in EXACTLY ONE response: its creation
	// (show-once contract). Never again afterwards.
	if got := strings.Count(captured["token-create"], plantedToken); got != 1 {
		t.Fatalf("token-create response: raw token count %d, want 1", got)
	}
	for label, body := range captured {
		if label == "token-create" {
			continue
		}
		if strings.Contains(body, plantedToken) {
			t.Errorf("raw api token leaked into %s", label)
		}
	}
	// 3. Non-issuance responses are pattern-clean (no secret-material fields).
	if f := securityaudit.Scan("reads", captured["me"], captured["token-list"], captured["audit-logs"]); len(f) != 0 {
		t.Errorf("secret-material findings in read responses: %+v", f)
	}
	// 4. Log sink: planted values never logged; no DSNs or private keys either.
	logPayload := logBuf.String()
	if f := securityaudit.ScanPlant("logs", []string{plantedPassword, plantedToken}, logPayload); len(f) != 0 {
		t.Errorf("secret findings in process logs: %+v", f)
	}
	for _, f := range securityaudit.Scan("logs", logPayload) {
		if f.Pattern == "postgres_dsn" || f.Pattern == "private_key_pem" {
			t.Errorf("high-signal secret pattern %q in logs at %s", f.Pattern, f.Location)
		}
	}
	// 5. Database audit rows: planted values never persisted.
	rows, err := srv.Pool.Query(context.Background(),
		`SELECT action, resource_id, metadata::text FROM audit_logs`)
	if err != nil {
		t.Fatalf("query audit rows: %v", err)
	}
	defer rows.Close()
	var dbPayload []string
	for rows.Next() {
		var action, resourceID, metadata string
		if err := rows.Scan(&action, &resourceID, &metadata); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		dbPayload = append(dbPayload, action+" "+resourceID+" "+metadata)
	}
	if f := securityaudit.ScanPlant("audit-db", []string{plantedPassword, plantedToken}, dbPayload...); len(f) != 0 {
		t.Errorf("planted secrets found in audit_logs rows: %+v", f)
	}
}

// setCookieAttrs parses Set-Cookie headers for one cookie name into an
// attribute map (net/http cannot round-trip SameSite/HttpOnly from parsed
// headers, so the flags are inspected as written on the wire).
func setCookieAttrs(t *testing.T, h http.Header, name string) (value string, attrs map[string]string) {
	t.Helper()
	attrs = map[string]string{}
	for _, sc := range h.Values("Set-Cookie") {
		parts := strings.Split(sc, ";")
		nv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
		if len(nv) != 2 || nv[0] != name {
			continue
		}
		value = nv[1]
		for _, p := range parts[1:] {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) == 2 {
				attrs[strings.ToLower(kv[0])] = kv[1]
			} else {
				attrs[strings.ToLower(kv[0])] = ""
			}
		}
	}
	return value, attrs
}

// TestPhase12SessionSecurity covers cookie flags, session rotation on the
// 2FA-gated login, logout revocation, absolute TTL, and the CSRF guard.
func TestPhase12SessionSecurity(t *testing.T) {
	srv, admin := newTestServer(t)

	// --- cookie flags: HttpOnly + SameSite always; Secure only when the
	// production cookie config is set (dev defaults keep plain-http testing
	// possible) ---
	resp := rawDo(admin, "POST", "/v1/auth/register", map[string]string{
		"email": "sess@phase12.test", "password": "supersecret123", "name": "Sess",
	}, true, nil)
	if resp.status != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.status, resp.body)
	}
	sessValue, sessAttrs := setCookieAttrs(t, resp.header, "epicpanel_session")
	if sessValue == "" {
		t.Fatalf("no session cookie set: %v", resp.header.Values("Set-Cookie"))
	}
	if _, ok := sessAttrs["httponly"]; !ok {
		t.Error("session cookie must be HttpOnly")
	}
	if samesite, ok := sessAttrs["samesite"]; !ok || !strings.EqualFold(samesite, "Lax") {
		t.Errorf("session cookie SameSite = %q, want Lax", samesite)
	}
	if _, ok := sessAttrs["secure"]; ok {
		t.Error("session cookie must not be Secure in dev config")
	}
	if path, ok := sessAttrs["path"]; !ok || path != "/" {
		t.Errorf("session cookie path = %q, want /", path)
	}

	// --- Secure cookie flag under production config (standalone handler
	// with CookieSecure=true) ---
	mux := http.NewServeMux()
	prodH := &auth.Handler{
		Users:    srv.Users,
		Sessions: srv.Sessions,
		Audit:    srv.Audit,
		Cfg:      config.Config{CookieName: "epicpanel_session", CookieSecure: true, SessionTTL: time.Hour},
	}
	mux.HandleFunc("POST /v1/auth/register", prodH.Register)
	prod := httptest.NewServer(mux)
	defer prod.Close()
	preq, _ := http.NewRequest("POST", prod.URL+"/v1/auth/register",
		strings.NewReader(`{"email":"prod@phase12.test","password":"supersecret123","name":"Prod"}`))
	preq.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatalf("prod register: %v", err)
	}
	pbody, _ := io.ReadAll(presp.Body)
	presp.Body.Close()
	if presp.StatusCode != http.StatusCreated {
		t.Fatalf("prod register: %d %s", presp.StatusCode, pbody)
	}
	prodValue, prodAttrs := setCookieAttrs(t, presp.Header, "epicpanel_session")
	if prodValue == "" {
		t.Fatalf("no production session cookie: %v", presp.Header.Values("Set-Cookie"))
	}
	// Per-request Secure (Phase 12 rev.): Secure is set when the request is
	// HTTPS (r.TLS or X-Forwarded-Proto=https), never over plain HTTP — a
	// global Secure flag silently broke http:// installs (browsers drop the
	// cookie). Probe both transports here.
	preq2, _ := http.NewRequest("POST", prod.URL+"/v1/auth/register",
		strings.NewReader(`{"email":"prod2@phase12.test","password":"supersecret123","name":"Prod2"}`))
	preq2.Header.Set("Content-Type", "application/json")
	// Simulate TLS termination by a trusted proxy:
	preq2.Header.Set("X-Forwarded-Proto", "https")
	presp2, err := http.DefaultClient.Do(preq2)
	if err != nil {
		t.Fatalf("prod register (https): %v", err)
	}
	defer presp2.Body.Close()
	_, prodAttrsHTTPS := setCookieAttrs(t, presp2.Header, "epicpanel_session")
	if _, ok := prodAttrsHTTPS["secure"]; !ok {
		t.Errorf("HTTPS request cookie must set Secure: %v", presp2.Header.Values("Set-Cookie"))
	}
	// The plain-HTTP request above must NOT set Secure (browser would drop it):
	if _, ok := prodAttrs["secure"]; ok {
		t.Errorf("plain-HTTP cookie must NOT set Secure (browser drops it, breaking login): %v", presp.Header.Values("Set-Cookie"))
	}
	if _, ok := prodAttrs["httponly"]; !ok {
		t.Error("cookie must set HttpOnly")
	}

	// --- 2FA enable -> login issues a ROTATED session (new token, new
	// cookie); the pre-2FA token is not silently reused ---
	resp = rawDo(admin, "POST", "/v1/auth/mfa/setup", nil, true, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("mfa setup: %d %s", resp.status, resp.body)
	}
	secret, _ := resp.decoded()["secret"].(string)
	code, err := totpNow(secret)
	if err != nil {
		t.Fatalf("totp: %v", err)
	}
	resp = rawDo(admin, "POST", "/v1/auth/mfa/enable", map[string]string{"code": code}, true, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("mfa enable: %d %s", resp.status, resp.body)
	}
	oldToken, _ := resp.decoded()["token"].(string)
	if oldToken == "" {
		oldToken = cookieValue(t, admin, "epicpanel_session")
	}

	mfaLogin := admin.NewClient()
	resp = rawDo(mfaLogin, "POST", "/v1/auth/login", map[string]string{
		"email": "sess@phase12.test", "password": "supersecret123",
	}, true, nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("mfa login: %d %s", resp.status, resp.body)
	}
	mfaToken, _ := resp.decoded()["mfa_token"].(string)
	code, _ = totpNow(secret)
	resp = rawDo(mfaLogin, "POST", "/v1/auth/mfa/verify", map[string]string{"mfa_token": mfaToken, "code": code}, true, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("mfa verify: %d %s", resp.status, resp.body)
	}
	newToken, _ := resp.decoded()["token"].(string)
	if newToken == "" {
		t.Fatal("mfa verify did not issue a session token")
	}
	if newToken == oldToken {
		t.Error("session token not rotated by the 2FA login")
	}
	rotatedValue, _ := setCookieAttrs(t, resp.header, "epicpanel_session")
	if rotatedValue != "" && rotatedValue == oldToken {
		t.Error("rotated cookie still carries the pre-2FA session value")
	}

	// --- logout revokes the session server-side ---
	anon := admin.NewClient()
	resp = rawDo(anon, "POST", "/v1/auth/register", map[string]string{
		"email": "revoke@phase12.test", "password": "supersecret123", "name": "Rev",
	}, true, nil)
	if resp.status != http.StatusCreated {
		t.Fatalf("register revoke user: %d %s", resp.status, resp.body)
	}
	logoutToken, _ := resp.decoded()["token"].(string)
	resp = rawDo(anon, "POST", "/v1/auth/logout", nil, true, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", resp.status, resp.body)
	}
	resp = rawDo(anon, "GET", "/v1/auth/me", nil, false, map[string]string{"Authorization": "Bearer " + logoutToken})
	if resp.status != http.StatusUnauthorized {
		t.Errorf("me with revoked token: %d want 401", resp.status)
	}
	// The cleared cookie no longer authenticates either.
	resp = rawDo(anon, "GET", "/v1/auth/me", nil, false, nil)
	if resp.status != http.StatusUnauthorized {
		t.Errorf("me with cleared cookie: %d want 401", resp.status)
	}

	// --- absolute session TTL is enforced (idle expiry is an explicit
	// exception — see docs/security-checklist.md) ---
	var created, expires time.Time
	if err := srv.Pool.QueryRow(context.Background(), `
		SELECT s.created_at, s.expires_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE u.email = $1
		ORDER BY s.created_at DESC LIMIT 1`, "sess@phase12.test").Scan(&created, &expires); err != nil {
		t.Fatalf("query session ttl: %v", err)
	}
	ttl := expires.Sub(created)
	if ttl < 59*time.Minute || ttl > 61*time.Minute {
		t.Errorf("session ttl = %v, want ~1h (configured SessionTTL)", ttl)
	}

	// --- CSRF: cookie-authenticated mutations without the header fail ---
	noCSRF := rawDo(admin, "POST", "/v1/organizations", map[string]string{"name": "NoHeader"}, false, nil)
	if noCSRF.status != http.StatusForbidden {
		t.Errorf("mutation without CSRF header: %d want 403", noCSRF.status)
	}
	withCSRF := rawDo(admin, "POST", "/v1/organizations", map[string]string{"name": "WithHeader"}, true, nil)
	if withCSRF.status != http.StatusCreated {
		t.Errorf("mutation with CSRF header: %d %s want 201", withCSRF.status, withCSRF.body)
	}
}

func cookieValue(t *testing.T, c *testClient, name string) string {
	t.Helper()
	u, err := url.Parse(c.base)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	for _, cookie := range c.http.Jar.Cookies(u) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// TestPhase12RateLimit proves the per-route-class limiter: auth endpoints
// exhaust a small budget and lock out (including valid credentials), while
// general API traffic runs on a far larger budget.
func TestPhase12RateLimit(t *testing.T) {
	_, admin := newTestServer(t)

	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "ratelimit@phase12.test", "password": "supersecret123", "name": "RL",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register: %d %v", resp.status, resp.body)
	}

	// Auth class: burst is 20 (register consumed one) — hammer logins until
	// the bucket empties.
	sawLimit := false
	var limited rawResp
	for i := 0; i < 30 && !sawLimit; i++ {
		limited = rawDo(admin, "POST", "/v1/auth/login", map[string]string{
			"email": "ratelimit@phase12.test", "password": "wrong-password",
		}, true, nil)
		if limited.status == http.StatusTooManyRequests {
			sawLimit = true
		} else if limited.status != http.StatusUnauthorized {
			t.Fatalf("unexpected login status %d", limited.status)
		}
	}
	if !sawLimit {
		t.Fatal("auth rate limit never engaged after 30 attempts")
	}
	if limited.header.Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}

	// Even the CORRECT password is refused while locked out.
	resp2 := rawDo(admin, "POST", "/v1/auth/login", map[string]string{
		"email": "ratelimit@phase12.test", "password": "supersecret123",
	}, true, nil)
	if resp2.status != http.StatusTooManyRequests {
		t.Errorf("valid login during lockout: %d want 429", resp2.status)
	}

	// General API class: 50 requests on a fresh route class all pass.
	for i := 0; i < 50; i++ {
		r := admin.do("GET", "/v1/organizations", nil)
		if r.status != http.StatusOK {
			t.Fatalf("general class request %d: %d %v", i, r.status, r.body)
		}
	}
}

// The test harness is skipped without a database; guard CI runs with a clear
// message instead of silent skips in the checklist report.
func TestMain(m *testing.M) {
	if os.Getenv("EPICPANEL_TEST_DATABASE_URL") == "" {
		fmt.Println("phase12: EPICPANEL_TEST_DATABASE_URL not set; integration tests will skip")
	}
	os.Exit(m.Run())
}
