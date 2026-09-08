// Phase 6 integration tests (DB: epicpanel_test_adm — P6's per-agent test
// database, override with EPICPANEL_TEST_DATABASE_URL). Covers: platform-admin-only
// authz on every adminview route (401 unauthenticated / 403 non-admin /
// tokens refused by middleware design), fleet overview counts, cross-org
// account listing with filters, account suspend/resume (job enqueue + audit +
// illegal-state 409), jobs console (filters, retry, cancel, dead-letter),
// live frame seeding and the ports audit view.

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

const phase6DBDefault = "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable"

// httpapiChain mirrors the production middleware order (server.go) minus the
// mux itself, so phase-6 routes run behind the same CSRF/session/scope stack.
func httpapiChain(srv *Server, next http.Handler) http.Handler {
	h := next
	h = httpapi.ScopeEnforce(h)
	h = httpapi.CSRFGuard(h)
	h = auth.SessionMiddleware(srv.Users, srv.Sessions, srv.Tokens, h)
	h = httpapi.CORSMiddleware(srv.Cfg.CORSOrigins, h)
	h = httpapi.RateLimit(srv.Limiter, h)
	h = httpapi.RequestLog(h)
	h = httpapi.APIPrefixRewrite(h)
	return h
}

// httptestServer runs a handler for the duration of the test.
func httptestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

type phase6Env struct {
	srv  *Server
	app  *testClient // full app (register/login/orgs)
	adm  *testClient // adminview routes (same session jar)
	usr  *testClient // adminview routes as a NON-admin
	org  string
	node string
	user string // admin user id
}

func newPhase6Env(t *testing.T) *phase6Env {
	t.Helper()
	if os.Getenv("EPICPANEL_TEST_DATABASE_URL") == "" {
		t.Setenv("EPICPANEL_TEST_DATABASE_URL", phase6DBDefault)
	}
	srv, client := newTestServer(t)

	// Admin-facing mux: session auth + CSRF only (adminview is cross-org by
	// design — there is no org path to resolve).
	mux := http.NewServeMux()
	registerPhase6(srv, mux)
	var h http.Handler = mux
	h = httpapiChain(srv, h)
	ts := httptestServer(t, h)

	env := &phase6Env{srv: srv, app: client}
	env.adm = &testClient{t: t, base: ts, http: client.http}

	// First registered user becomes the platform admin.
	resp := client.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Fleet Admin",
	})
	if u, ok := resp.body["user"].(map[string]any); ok {
		env.user, _ = u["id"].(string)
	}
	if env.user == "" {
		t.Fatalf("register admin: %v", resp.body)
	}

	// A second user that is NOT a platform admin.
	other := client.NewClient()
	other.do("POST", "/v1/auth/register", map[string]string{
		"email": "member@example.test", "password": "supersecret123", "name": "Member",
	})
	env.usr = &testClient{t: t, base: ts, http: other.http}
	return env
}

// seedNodeAndSite inserts a node + a ready website row directly (no runtime
// gates involved) so admin views have fleet data to aggregate.
func (e *phase6Env) seedNodeAndSite(t *testing.T, nodeName, siteName string, backendPort int) (string, string) {
	t.Helper()
	ctx := context.Background()
	org := uuid.New()
	if _, err := e.srv.Pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, created_by) VALUES ($1, 'FleetCo', $2, $3)`,
		org, "org-"+nodeName, e.user); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	e.org = org.String()
	var creator uuid.UUID
	if err := e.srv.Pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&creator); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	node := uuid.New()
	if _, err := e.srv.Pool.Exec(ctx,
		`INSERT INTO servers (id, organization_id, name, hostname, status, registered_by, enrolled_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, 'online', $5, now(), now())`,
		node, org, nodeName, nodeName+".fleet.test", creator); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	e.node = node.String()
	site := uuid.New()
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO websites (id, organization_id, server_id, name, primary_domain, runtime, status, backend_port, created_by)
		VALUES ($1, $2, $3, $4, $5, 'php', 'ready', $6, $7)`,
		site, org, node, siteName, siteName+".test", backendPort, creator); err != nil {
		t.Fatalf("seed website: %v", err)
	}
	return org.String(), site.String()
}

func TestPhase6Authz(t *testing.T) {
	e := newPhase6Env(t)

	for _, path := range []string{
		"/v1/adminview/overview", "/v1/adminview/servers", "/v1/adminview/accounts",
		"/v1/adminview/organizations", "/v1/adminview/domains", "/v1/adminview/databases",
		"/v1/adminview/backups", "/v1/adminview/dns-zones", "/v1/adminview/ports",
		"/v1/adminview/alerts", "/v1/adminview/users", "/v1/adminview/jobs",
		"/v1/adminview/jobs/dead-letter", "/v1/adminview/live",
	} {
		if got := e.adm.do("GET", path, nil); got.status != http.StatusOK {
			t.Fatalf("admin GET %s: expected 200, got %d (%v)", path, got.status, got.body)
		}
		if got := e.usr.do("GET", path, nil); got.status != http.StatusForbidden {
			t.Fatalf("non-admin GET %s: expected 403, got %d", path, got.status)
		}
	}

	// Unauthenticated: a fresh client without cookies.
	anon := e.adm.NewClient()
	if got := anon.do("GET", "/v1/adminview/overview", nil); got.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated overview: expected 401, got %d", got.status)
	}

	// Mutations are gated identically.
	if got := e.usr.do("POST", "/v1/adminview/accounts/"+uuid.NewString()+"/suspend", nil); got.status != http.StatusForbidden {
		t.Fatalf("non-admin suspend: expected 403, got %d", got.status)
	}
	if got := e.usr.do("POST", "/v1/adminview/jobs/"+uuid.NewString()+"/retry", nil); got.status != http.StatusForbidden {
		t.Fatalf("non-admin retry: expected 403, got %d", got.status)
	}
	if got := e.usr.do("POST", "/v1/adminview/jobs/"+uuid.NewString()+"/cancel", nil); got.status != http.StatusForbidden {
		t.Fatalf("non-admin cancel: expected 403, got %d", got.status)
	}
}

func TestPhase6OverviewAndFleet(t *testing.T) {
	e := newPhase6Env(t)
	e.seedNodeAndSite(t, "node-a", "shop", 6601)

	resp := e.adm.do("GET", "/v1/adminview/overview", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("overview: %d %v", resp.status, resp.body)
	}
	accounts, _ := resp.body["accounts"].(map[string]any)
	if accounts == nil || accounts["total"].(float64) < 1 {
		t.Fatalf("overview accounts missing: %v", accounts)
	}
	if accounts["active"].(float64) != 1 {
		t.Fatalf("expected 1 active account, got %v", accounts["active"])
	}
	nodes, _ := resp.body["nodes"].(map[string]any)
	if nodes["total"].(float64) != 1 {
		t.Fatalf("expected 1 node, got %v", nodes)
	}
	portsSummary, _ := resp.body["ports"].(map[string]any)
	if portsSummary["apache"] == nil {
		t.Fatalf("ports summary missing apache range: %v", portsSummary)
	}
	dist, _ := resp.body["distribution"].(map[string]any)
	if dist["by_plan"] == nil || dist["by_status"] == nil || dist["by_node"] == nil {
		t.Fatalf("distribution incomplete: %v", dist)
	}

	servers := e.adm.do("GET", "/v1/adminview/servers", nil)
	if servers.status != http.StatusOK {
		t.Fatalf("servers: %d", servers.status)
	}
	list, _ := servers.body["servers"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 server row, got %d", len(list))
	}
	row, _ := list[0].(map[string]any)
	if row["name"] != "node-a" || row["websites"].(float64) != 1 {
		t.Fatalf("server row mismatch: %v", row)
	}

	// Live frames seed (no stream connected — empty is honest).
	live := e.adm.do("GET", "/v1/adminview/live", nil)
	if live.status != http.StatusOK {
		t.Fatalf("live: %d", live.status)
	}
}

func TestPhase6AccountsCrossOrgAndLifecycle(t *testing.T) {
	e := newPhase6Env(t)
	orgA, siteA := e.seedNodeAndSite(t, "node-a", "shop", 6601)

	// Second org + site on the same node (cross-org visibility is the point).
	org2 := uuid.New()
	node2 := uuid.New()
	site2 := uuid.New()
	var creator uuid.UUID
	_ = e.srv.Pool.QueryRow(context.Background(), `SELECT id FROM users LIMIT 1`).Scan(&creator)
	if _, err := e.srv.Pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, slug, created_by) VALUES ($1, 'OtherCo', 'other-co', $2)`, org2, creator); err != nil {
		t.Fatalf("org2: %v", err)
	}
	if _, err := e.srv.Pool.Exec(context.Background(),
		`INSERT INTO servers (id, organization_id, name, hostname, status, registered_by, enrolled_at, last_seen_at)
		 VALUES ($1, $2, 'node-b', 'node-b.fleet.test', 'online', $3, now(), now())`, node2, org2, creator); err != nil {
		t.Fatalf("node2: %v", err)
	}
	if _, err := e.srv.Pool.Exec(context.Background(),
		`INSERT INTO websites (id, organization_id, server_id, name, primary_domain, runtime, status, backend_port, created_by)
		 VALUES ($1, $2, $3, 'blog', 'blog.test', 'php', 'ready', 7101, $4)`, site2, org2, node2, creator); err != nil {
		t.Fatalf("site2: %v", err)
	}

	// Cross-org list: both orgs visible to the platform admin.
	accounts := e.adm.do("GET", "/v1/adminview/accounts", nil)
	if accounts.status != http.StatusOK {
		t.Fatalf("accounts: %d %v", accounts.status, accounts.body)
	}
	list, _ := accounts.body["accounts"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected 2 accounts across orgs, got %d", len(list))
	}

	// Filters: by org, by status, by search.
	if got := e.adm.do("GET", "/v1/adminview/accounts?org_id="+orgA, nil); got.status != http.StatusOK {
		t.Fatalf("org filter: %d", got.status)
	} else if list, _ := got.body["accounts"].([]any); len(list) != 1 {
		t.Fatalf("org filter: expected 1, got %d", len(list))
	}
	if got := e.adm.do("GET", "/v1/adminview/accounts?q=blog", nil); got.status != http.StatusOK {
		t.Fatalf("search: %d", got.status)
	} else if list, _ := got.body["accounts"].([]any); len(list) != 1 {
		t.Fatalf("search: expected 1, got %d", len(list))
	}

	// Suspend (idempotent key: a second call collapses onto the same job).
	susp := e.adm.do("POST", "/v1/adminview/accounts/"+siteA+"/suspend", nil)
	if susp.status != http.StatusAccepted {
		t.Fatalf("suspend: %d %v", susp.status, susp.body)
	}
	jobID, _ := susp.body["job_id"].(string)
	if jobID == "" {
		t.Fatalf("suspend returned no job id: %v", susp.body)
	}
	if again := e.adm.do("POST", "/v1/adminview/accounts/"+siteA+"/suspend", nil); again.status != http.StatusAccepted {
		t.Fatalf("idempotent suspend: %d", again.status)
	}
	var jobCount int
	_ = e.srv.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE type = 'suspend_website' AND website_id = $1`, siteA).Scan(&jobCount)
	if jobCount != 1 {
		t.Fatalf("expected 1 suspend job, got %d", jobCount)
	}

	// Audit trail exists for the admin action.
	var audits int
	_ = e.srv.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = 'adminview.account.suspend' AND resource_id = $1`, siteA).Scan(&audits)
	if audits == 0 {
		t.Fatalf("suspend was not audited")
	}

	// Illegal state: suspending an already-pending-suspend account still 409s
	// only if status changed (agent transition not applied yet) — so expect
	// 409 when the site is NOT ready anymore.
	if _, err := e.srv.Pool.Exec(context.Background(),
		`UPDATE websites SET status = 'suspended' WHERE id = $1`, siteA); err != nil {
		t.Fatalf("force status: %v", err)
	}
	if got := e.adm.do("POST", "/v1/adminview/accounts/"+siteA+"/suspend", nil); got.status != http.StatusConflict {
		t.Fatalf("suspend of suspended site: expected 409, got %d", got.status)
	}

	// Resume (idempotent: a second call collapses onto the same job).
	res := e.adm.do("POST", "/v1/adminview/accounts/"+siteA+"/resume", nil)
	if res.status != http.StatusAccepted {
		t.Fatalf("resume: %d %v", res.status, res.body)
	}
	resumeJob, _ := res.body["job_id"].(string)
	if again := e.adm.do("POST", "/v1/adminview/accounts/"+siteA+"/resume", nil); again.status != http.StatusAccepted || again.body["job_id"] != resumeJob {
		t.Fatalf("idempotent resume: %d %v (want same job %s)", again.status, again.body, resumeJob)
	}

	// Unknown website 404s (cross-tenant isolation keeps ids opaque).
	if got := e.adm.do("POST", "/v1/adminview/accounts/"+uuid.NewString()+"/suspend", nil); got.status != http.StatusNotFound {
		t.Fatalf("unknown website: expected 404, got %d", got.status)
	}
}

func TestPhase6JobsConsoleLifecycle(t *testing.T) {
	e := newPhase6Env(t)
	_, _ = e.seedNodeAndSite(t, "node-a", "shop", 6601)

	ctx := context.Background()
	nodeID, _ := uuid.Parse(e.node)

	// A terminal failure: enqueue, claim 3x and report failure each time.
	job, err := e.srv.Jobs.Enqueue(ctx, nodeID, nil, "provision_website", map[string]string{"demo": "1"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	for i := 0; i < 3; i++ {
		claimed, err := e.srv.Jobs.ClaimNext(ctx, nodeID)
		if err != nil || claimed == nil {
			t.Fatalf("claim %d: %v %v", i, claimed, err)
		}
		if _, err := e.srv.Jobs.ReportResult(ctx, claimed.ID, false, nil, "boom "+time.Now().Format(time.Stamp)); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	failed, err := e.srv.Jobs.GetByID(ctx, job.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("expected terminal failure, got %v %v", failed, err)
	}

	// Console lists it under the failed filter.
	resp := e.adm.do("GET", "/v1/adminview/jobs?status=failed", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("jobs list: %d", resp.status)
	}
	list, _ := resp.body["jobs"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 failed job, got %d", len(list))
	}

	// Dead-letter view shows it too.
	dl := e.adm.do("GET", "/v1/adminview/jobs/dead-letter", nil)
	if dl.status != http.StatusOK {
		t.Fatalf("dead-letter: %d", dl.status)
	}

	// Retry resets it to pending with attempts cleared.
	retry := e.adm.do("POST", "/v1/adminview/jobs/"+job.ID.String()+"/retry", nil)
	if retry.status != http.StatusOK || retry.body["status"] != "pending" {
		t.Fatalf("retry: %d %v", retry.status, retry.body)
	}
	requeued, _ := e.srv.Jobs.GetByID(ctx, job.ID)
	if requeued.Status != "pending" || requeued.Attempts != 0 || requeued.Error != "" {
		t.Fatalf("requeued job mismatch: %+v", requeued)
	}

	// Cancel the pending job (mark failed w/ honest error).
	cancel := e.adm.do("POST", "/v1/adminview/jobs/"+job.ID.String()+"/cancel", nil)
	if cancel.status != http.StatusNoContent {
		t.Fatalf("cancel: %d %v", cancel.status, cancel.body)
	}
	cancelled, _ := e.srv.Jobs.GetByID(ctx, job.ID)
	if cancelled.Status != "failed" || cancelled.Error != "cancelled by administrator" {
		t.Fatalf("cancelled job mismatch: %+v", cancelled)
	}

	// Running jobs cannot be cancelled (lease-governed; honest refusal).
	if _, err := e.srv.Pool.Exec(ctx,
		`UPDATE jobs SET status = 'running', claimed_at = now(), lease_expires_at = now() + interval '1 minute' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("force running: %v", err)
	}
	if got := e.adm.do("POST", "/v1/adminview/jobs/"+job.ID.String()+"/cancel", nil); got.status != http.StatusConflict {
		t.Fatalf("cancel running: expected 409, got %d", got.status)
	}

	// Audit records exist for both mutations.
	var audits int
	_ = e.srv.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action IN ('adminview.job.retry','adminview.job.cancel')`).Scan(&audits)
	if audits < 2 {
		t.Fatalf("expected retry+cancel audits, got %d", audits)
	}
}

func TestPhase6SecondaryViews(t *testing.T) {
	e := newPhase6Env(t)
	_, site := e.seedNodeAndSite(t, "node-a", "shop", 6601)
	ctx := context.Background()

	// Domain, database, backup, zone rows for the seeded account.
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO domains (organization_id, website_id, domain, kind, ssl_mode, ssl_state)
		SELECT organization_id, id, 'shop.test', 'primary', 'letsencrypt', 'active' FROM websites WHERE id = $1`, site); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO databases (organization_id, server_id, website_id, engine, name, db_user, status, created_by)
		SELECT organization_id, server_id, id, 'mariadb', 'ep_shop', 'ep_shop', 'ready', created_by FROM websites WHERE id = $1`, site); err != nil {
		t.Fatalf("seed database: %v", err)
	}
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO dns_zones (website_id, organization_id, domain)
		SELECT id, organization_id, 'shop.test' FROM websites WHERE id = $1`, site); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO backups (organization_id, website_id, type, status, trigger_type, size_bytes, finished_at)
		SELECT organization_id, id, 'full', 'successful', 'manual', 1024, now() FROM websites WHERE id = $1`, site); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	if _, err := e.srv.Pool.Exec(ctx, `
		INSERT INTO alerts (organization_id, type, severity, resource_type, resource_id, resource_name, message)
		SELECT organization_id, 'node.offline', 'critical', 'server', id, name, 'node went dark' FROM servers LIMIT 1`); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	check := func(path, key string, want int) {
		t.Helper()
		resp := e.adm.do("GET", path, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.status)
		}
		list, _ := resp.body[key].([]any)
		if len(list) != want {
			t.Fatalf("%s: expected %d rows, got %d (%v)", path, want, len(list), resp.body)
		}
	}
	check("/v1/adminview/domains", "domains", 1)
	check("/v1/adminview/databases", "databases", 1)
	check("/v1/adminview/dns-zones", "zones", 1)
	check("/v1/adminview/backups", "backups", 1)
	check("/v1/adminview/alerts", "alerts", 1)
	check("/v1/adminview/organizations", "organizations", 1)

	// Ports audit view: allocated backend ports + honest scope note.
	ports := e.adm.do("GET", "/v1/adminview/ports", nil)
	if ports.status != http.StatusOK {
		t.Fatalf("ports: %d", ports.status)
	}
	allocs, _ := ports.body["allocations"].([]any)
	if len(allocs) != 1 {
		t.Fatalf("ports allocations: expected 1, got %d", len(allocs))
	}
	if note, _ := ports.body["note"].(string); note == "" {
		t.Fatalf("ports view must carry the honest scope note: %v", ports.body)
	}

	// Users view includes security posture.
	users := e.adm.do("GET", "/v1/adminview/users", nil)
	if users.status != http.StatusOK {
		t.Fatalf("users: %d", users.status)
	}
	list, _ := users.body["users"].([]any)
	if len(list) != 2 {
		t.Fatalf("users: expected 2, got %d", len(list))
	}
	if users.body["security"] == nil {
		t.Fatalf("users security summary missing")
	}
}
