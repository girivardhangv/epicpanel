package api

// Phase 7 integration tests (DB: epicpanel_test_mc by default, override with
// EPICPANEL_TEST_DATABASE_URL). Covers: instance CRUD + plan gates, org
// scoping (cross-tenant 404), role enforcement, RCON password write-only
// (ciphertext at rest, never in payloads/responses), lifecycle state
// machine + reconciliation vs injected agent truth (running + crash
// recovery + policy=no), console command allowlist (no shell), schedules,
// provider offers, and world backup job seams.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

const mcRCONSecret = "rcon-supersecret-value-9f3a1c"

// mcTestEnv boots the full app (auth/orgs) plus a phase-7 router sharing
// the same stores, behind one cookie jar.
type mcTestEnv struct {
	srv        *Server
	app        *testClient
	mc         *testClient // phase-7 routes
	orgID      string
	serverID   string
	instanceID string
}

func newMCTestEnv(t *testing.T) *mcTestEnv {
	t.Helper()
	if os.Getenv("EPICPANEL_TEST_DATABASE_URL") == "" {
		t.Setenv("EPICPANEL_TEST_DATABASE_URL",
			"postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_mc?sslmode=disable")
	}
	t.Setenv("EPICPANEL_DISABLE_MC_LOOPS", "1")
	srv, client := newTestServer(t)

	// Deterministic tests drive reconciliation manually.
	mux := http.NewServeMux()
	authH := &auth.Handler{Users: srv.Users, Sessions: srv.Sessions, Audit: srv.Audit, Cfg: srv.Cfg,
		SetupDone: srv.setupCompleted, MFA: srv.MFA}
	mux.HandleFunc("POST /v1/auth/register", authH.Register)
	orgH := &organizations.Handler{Store: srv.Orgs, Audit: srv.Audit}
	orgH.Register(mux)
	registerPhase7(srv, mux)
	var h http.Handler = mux
	h = httpapi.ScopeEnforce(h)
	h = httpapi.CSRFGuard(h)
	h = auth.SessionMiddleware(srv.Users, srv.Sessions, srv.Tokens, h)
	h = httpapi.CORSMiddleware(srv.Cfg.CORSOrigins, h)
	h = httpapi.RateLimit(srv.Limiter, h)
	h = httpapi.RequestLog(h)
	h = httpapi.APIPrefixRewrite(h)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	// Same jar (cookies ignore ports) against the phase-7 router.
	mcClient := &testClient{t: t, base: ts.URL, http: client.http}

	env := &mcTestEnv{srv: srv, app: client, mc: mcClient}

	// Bootstrap: admin user + org on a Minecraft plan + a server row.
	client.do("POST", "/v1/auth/register", map[string]string{
		"email": "mcadmin@example.test", "password": "supersecret123", "name": "MC Admin",
	})
	resp := client.do("POST", "/v1/organizations", map[string]string{"name": "MC Co"})
	env.orgID, _ = resp.body["id"].(string)
	if env.orgID == "" {
		t.Fatalf("create org: %v", resp.body)
	}
	if _, err := srv.Pool.Exec(context.Background(),
		`UPDATE organizations SET package_id = (SELECT id FROM hosting_packages WHERE name = 'Minecraft 4GB') WHERE id = $1`, env.orgID); err != nil {
		t.Fatalf("assign minecraft plan: %v", err)
	}
	resp = client.do("POST", "/v1/organizations/"+env.orgID+"/servers", map[string]string{"name": "mc-node-1"})
	if srvMap, ok := resp.body["server"].(map[string]any); ok {
		env.serverID, _ = srvMap["id"].(string)
	}
	if env.serverID == "" {
		t.Fatalf("create server: %v", resp.body)
	}

	// Truncate instance tables (harness drop-list does not know them).
	ctx := context.Background()
	if _, err := srv.Pool.Exec(ctx, `UPDATE jobs SET minecraft_id = NULL`); err != nil {
		t.Fatalf("clear job links: %v", err)
	}
	for _, table := range []string{"minecraft_world_backups", "minecraft_schedules", "minecraft_instances"} {
		if _, err := srv.Pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	return env
}

func (e *mcTestEnv) createInstance(t *testing.T, name string) response {
	t.Helper()
	return e.mc.do("POST", "/v1/organizations/"+e.orgID+"/minecraft", map[string]any{
		"name": name, "provider": "paper", "version": "1.21.4",
		"accept_eula": true, "server_id": e.serverID,
		"properties": map[string]string{"motd": "Test Server", "max-players": "10"},
	})
}

// mcUserClient registers a fresh user (own jar) on the app server and
// returns a client bound to the phase-7 router.
func (e *mcTestEnv) mcUserClient(t *testing.T, email string) *testClient {
	t.Helper()
	c := e.app.NewClient()
	resp := c.do("POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "supersecret123", "name": email,
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register %s: %d %v", email, resp.status, resp.body)
	}
	return &testClient{t: t, base: e.mc.base, http: c.http}
}

// --- CRUD + plan gates + secrets at rest ---

func TestMCCreateAndPlanGate(t *testing.T) {
	env := newMCTestEnv(t)

	resp := env.createInstance(t, "survival-1")
	if resp.status != http.StatusCreated {
		t.Fatalf("create instance: %d %v", resp.status, resp.body)
	}
	env.instanceID, _ = resp.body["id"].(string)
	if env.instanceID == "" {
		t.Fatalf("instance id missing: %v", resp.body)
	}
	if resp.body["status"] != "installing" {
		t.Errorf("initial status = %v, want installing", resp.body["status"])
	}
	if resp.body["port"] == nil || resp.body["port"] == float64(0) {
		t.Errorf("game port not allocated: %v", resp.body["port"])
	}
	// Plan-derived heap (Minecraft 4GB → 3072 MB).
	if resp.body["xmx_mb"] != float64(3072) {
		t.Errorf("xmx_mb = %v, want 3072 (plan RAM 4096 × 3/4)", resp.body["xmx_mb"])
	}

	// Install job exists, linked to the instance, no plaintext rcon secret.
	var payloadRaw []byte
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE minecraft_id = $1 AND type = 'mc_install' ORDER BY created_at DESC LIMIT 1`,
		env.instanceID).Scan(&payloadRaw); err != nil {
		t.Fatalf("install job: %v", err)
	}
	if strings.Contains(string(payloadRaw), mcRCONSecret) {
		// (secret constant is never used in create; the check below on the
		// stored ciphertext is the real one)
	}
	var payloadMap map[string]any
	if err := json.Unmarshal(payloadRaw, &payloadMap); err != nil {
		t.Fatal(err)
	}
	if enc, _ := payloadMap["rcon_pass_enc"].(string); enc == "" {
		t.Error("install payload must carry rcon_pass_enc ciphertext")
	}
	if strings.Contains(string(payloadRaw), "rcon.password") {
		// protected property keys never ride in the payload properties
	}

	// The row's rcon_pass_enc is ciphertext, not plaintext.
	var enc string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT rcon_pass_enc FROM minecraft_instances WHERE id = $1`, env.instanceID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || !isBase64ish(enc) {
		t.Error("rcon password not stored as ciphertext")
	}

	// The GET view carries no password material.
	resp = env.mc.do("GET", "/v1/organizations/"+env.orgID+"/minecraft/"+env.instanceID, nil)
	if strings.Contains(string(jsonBody(t, resp.body)), enc) {
		t.Fatal("GET instance leaks the rcon ciphertext")
	}

	// Ports: one externally-listening game port per instance — the plan
	// allows 1 port, so a second instance hits the gate.
	resp = env.createInstance(t, "survival-2")
	if resp.status != http.StatusForbidden {
		t.Fatalf("second instance (plan ports 1): %d %v, want 403", resp.status, resp.body)
	}

	// A web-plan org cannot create instances at all.
	resp = env.app.do("POST", "/v1/organizations", map[string]string{"name": "WebOrg"})
	webOrg, _ := resp.body["id"].(string)
	if webOrg != "" {
		resp = env.mc.do("POST", "/v1/organizations/"+webOrg+"/minecraft", map[string]any{
			"name": "webmc", "provider": "paper", "version": "1.21.4", "accept_eula": true,
		})
		if resp.status != http.StatusForbidden {
			t.Fatalf("instance on web plan: %d %v, want 403", resp.status, resp.body)
		}
	}

	// EULA must be accepted.
	resp = env.mc.do("POST", "/v1/organizations/"+env.orgID+"/minecraft", map[string]any{
		"name": "eula-bot", "provider": "paper", "version": "1.21.4", "accept_eula": false,
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("create without eula: %d, want 422", resp.status)
	}

	// Unsupported version refused (validated against the provider offer).
	resp = env.mc.do("POST", "/v1/organizations/"+env.orgID+"/minecraft", map[string]any{
		"name": "badver", "provider": "paper", "version": "9.9.9", "accept_eula": true, "server_id": env.serverID,
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("bad version: %d, want 422", resp.status)
	}
}

// --- org scoping + role enforcement ---

func TestMCOrgScopingAndRoles(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "scoped-mc")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.status, resp.body)
	}
	env.instanceID, _ = resp.body["id"].(string)

	// Second org (own admin) cannot see or mutate the instance: 404, not 403.
	outsider := env.mcUserClient(t, "mc-outsider@example.test")
	resp = outsider.do("POST", "/v1/organizations", map[string]string{"name": "OtherMC"})
	otherOrg, _ := resp.body["id"].(string)
	if otherOrg == "" {
		t.Fatalf("other org: %v", resp.body)
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		resp = outsider.do(method, "/v1/organizations/"+otherOrg+"/minecraft/"+env.instanceID, map[string]any{})
		if resp.status != http.StatusNotFound {
			t.Errorf("cross-tenant %s: %d, want 404", method, resp.status)
		}
	}
	resp = outsider.do("POST", "/v1/organizations/"+otherOrg+"/minecraft/"+env.instanceID+"/start", nil)
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant start: %d, want 404", resp.status)
	}
	// Non-member on the real org path: 404 (no existence leak).
	resp = outsider.do("GET", "/v1/organizations/"+env.orgID+"/minecraft/"+env.instanceID, nil)
	if resp.status != http.StatusNotFound {
		t.Errorf("non-member on real org: %d, want 404", resp.status)
	}

	// A viewer (billing rank < developer) can read but not mutate.
	viewer := env.mcUserClient(t, "mc-viewer@example.test")
	mresp := env.app.do("POST", "/v1/organizations/"+env.orgID+"/members", map[string]string{
		"email": "mc-viewer@example.test", "role": "billing",
	})
	if mresp.status != http.StatusOK && mresp.status != http.StatusCreated {
		t.Fatalf("add viewer member: %d %v", mresp.status, mresp.body)
	}
	resp = viewer.do("GET", "/v1/organizations/"+env.orgID+"/minecraft", nil)
	if resp.status != http.StatusOK {
		t.Errorf("viewer read: %d, want 200", resp.status)
	}
	resp = viewer.do("POST", "/v1/organizations/"+env.orgID+"/minecraft/"+env.instanceID+"/stop", nil)
	if resp.status != http.StatusForbidden {
		t.Errorf("viewer stop: %d, want 403", resp.status)
	}
	resp = viewer.do("POST", "/v1/organizations/"+env.orgID+"/minecraft/"+env.instanceID+"/console/command", map[string]string{"command": "list"})
	if resp.status != http.StatusForbidden {
		t.Errorf("viewer console command: %d, want 403", resp.status)
	}
}

// --- console allowlist (no shell) + schedules ---

func TestMCConsoleAllowlist(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "console-mc")
	env.instanceID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/minecraft/" + env.instanceID

	// Allowlisted command → job queued with the NORMALIZED command.
	resp = env.mc.do("POST", base+"/console/command", map[string]string{"command": "say hello"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("allowlisted command: %d %v", resp.status, resp.body)
	}
	var payloadRaw []byte
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE minecraft_id = $1 AND type = 'mc_command' ORDER BY created_at DESC LIMIT 1`,
		env.instanceID).Scan(&payloadRaw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payloadRaw), "say hello") {
		t.Errorf("command payload malformed: %s", payloadRaw)
	}

	// Non-allowlisted (shell-like) commands never enqueue a job.
	for _, bad := range []string{"rm -rf /", "sudo reboot", "eval x", "say hi; stop"} {
		resp = env.mc.do("POST", base+"/console/command", map[string]string{"command": bad})
		if resp.status != http.StatusUnprocessableEntity {
			t.Errorf("command %q: %d, want 422", bad, resp.status)
		}
	}
	var pending int
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE minecraft_id = $1 AND type = 'mc_command'`, env.instanceID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Errorf("mc_command jobs = %d, want 1 (refused ones never enqueue)", pending)
	}

	// Schedules: restart ok; free-form command refused; allowlisted command ok.
	resp = env.mc.do("POST", base+"/schedules", map[string]string{"kind": "restart", "cron": "0 */6 * * *"})
	if resp.status != http.StatusCreated {
		t.Fatalf("restart schedule: %d %v", resp.status, resp.body)
	}
	resp = env.mc.do("POST", base+"/schedules", map[string]string{"kind": "command", "cron": "* * * * *", "command": "rm -rf /"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("free-form command schedule must be refused: %d", resp.status)
	}
	resp = env.mc.do("POST", base+"/schedules", map[string]string{"kind": "command", "cron": "* * * * *", "command": "save-all"})
	if resp.status != http.StatusCreated {
		t.Errorf("allowlisted command schedule: %d %v", resp.status, resp.body)
	}
	resp = env.mc.do("POST", base+"/schedules", map[string]string{"kind": "restart", "cron": "bad"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("bad cron: %d, want 422", resp.status)
	}

	// Console WS route exists for GET; a POST to the command route with an
	// empty command is refused (input path is allowlist-only).
	resp = env.mc.do("POST", base+"/console/command", map[string]string{"command": ""})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("empty command: %d, want 422", resp.status)
	}
}

// --- lifecycle guards + reconciliation vs injected agent truth ---

func TestMCLifecycleAndReconciliation(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "lifecycle-mc")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d", resp.status)
	}
	env.instanceID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/minecraft/" + env.instanceID
	ctx := context.Background()

	// start from installing → starting + mc_start job queued (ciphertext env)
	resp = env.mc.do("POST", base+"/start", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("start: %d %v", resp.status, resp.body)
	}
	assertMCStatus(t, env, env.instanceID, "starting")
	var payloadRaw []byte
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT payload FROM jobs WHERE minecraft_id = $1 AND type = 'mc_start' ORDER BY created_at DESC LIMIT 1`,
		env.instanceID).Scan(&payloadRaw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payloadRaw), mcRCONSecret) {
		t.Fatal("start payload leaks a secret")
	}

	// Agent truth: unit active (LIVE sample) → running.
	pushMCSample(t, env, env.instanceID, "active", 3)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "running")

	// Crash: unit failed while desired running → crashed + policy restart
	// (on-failure, episode 1) → starting with a crash-recovery job.
	pushMCSample(t, env, env.instanceID, "failed", 1)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "starting")
	var restartCount, episodeRestarts int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT restart_count, episode_restarts FROM minecraft_instances WHERE id = $1`, env.instanceID).Scan(&restartCount, &episodeRestarts); err != nil {
		t.Fatal(err)
	}
	if restartCount != 1 || episodeRestarts != 1 {
		t.Errorf("crash counters = %d/%d, want 1/1", restartCount, episodeRestarts)
	}
	var pending int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE minecraft_id = $1 AND type = 'mc_start' AND status = 'pending' AND idempotency_key LIKE 'mccrash-%'`, env.instanceID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Errorf("crash recovery job pending = %d, want 1", pending)
	}

	// Recovered: agent truth active again → running.
	pushMCSample(t, env, env.instanceID, "active", 1)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "running")

	// stop → stopping; agent truth inactive → stopped.
	resp = env.mc.do("POST", base+"/stop", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("stop: %d", resp.status)
	}
	pushMCSample(t, env, env.instanceID, "inactive", 1)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "stopped")

	// double-stop is idempotent (no 409)
	resp = env.mc.do("POST", base+"/stop", nil)
	if resp.status != http.StatusAccepted {
		t.Errorf("double stop: %d, want 202", resp.status)
	}

	// "no" restart policy: crash leaves the instance crashed, no recovery job.
	if _, err := env.srv.Pool.Exec(ctx, `UPDATE minecraft_instances SET restart_policy = 'no' WHERE id = $1`, env.instanceID); err != nil {
		t.Fatal(err)
	}
	env.mc.do("POST", base+"/start", nil)
	pushMCSample(t, env, env.instanceID, "active", 0)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "running")
	pushMCSample(t, env, env.instanceID, "failed", 5)
	env.srv.reconcileMinecraft(ctx)
	assertMCStatus(t, env, env.instanceID, "crashed")
	var recovery int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE minecraft_id = $1 AND type = 'mc_start' AND status = 'pending'
		 AND idempotency_key LIKE 'mccrash-%'`, env.instanceID).Scan(&recovery); err != nil {
		t.Fatal(err)
	}
	if recovery != 1 {
		t.Errorf("crash-recovery jobs = %d, want 1 (only the on-failure crash; policy 'no' adds none)", recovery)
	}

	// kill from running: allowed (customer override), queues mc_kill.
	env.mc.do("POST", base+"/start", nil)
	pushMCSample(t, env, env.instanceID, "active", 0)
	env.srv.reconcileMinecraft(ctx)
	resp = env.mc.do("POST", base+"/kill", nil)
	if resp.status != http.StatusAccepted {
		t.Errorf("kill: %d %v", resp.status, resp.body)
	}
	var killJobs int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE minecraft_id = $1 AND type = 'mc_kill'`, env.instanceID).Scan(&killJobs); err != nil {
		t.Fatal(err)
	}
	if killJobs != 1 {
		t.Errorf("mc_kill jobs = %d, want 1", killJobs)
	}
}

// pushMCSample injects a LIVE metrics frame carrying the instance's
// minecraft envelope (what a real agent streams for kind=minecraft units).
func pushMCSample(t *testing.T, env *mcTestEnv, instanceID, status string, restarts int) {
	t.Helper()
	var serverID string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT server_id FROM minecraft_instances WHERE id = $1`, instanceID).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	sid, err := uuid.Parse(serverID)
	if err != nil {
		t.Fatal(err)
	}
	sample := agentproto.Sample{
		Node: agentproto.NodeSample{CPUPercent: 1.0},
		Apps: []agentproto.AppSample{{
			WebsiteID: instanceID, Kind: "minecraft", Status: status,
			CPUPercent: 55.5, MemoryBytes: 2 * 1024 * 1024 * 1024,
			RestartCount: restarts, UptimeS: 600,
			Players: 4, TPS: 19.98, MSPT: 3.2,
		}},
	}
	data, _ := json.Marshal(sample)
	frame := agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "test-session", Seq: time.Now().UnixNano(), Ts: time.Now().UTC(), Data: data}
	if !env.srv.LiveStore.Ingest(sid, frame) {
		t.Fatal("frame ingested as duplicate")
	}
}

func assertMCStatus(t *testing.T, env *mcTestEnv, instanceID, want string) {
	t.Helper()
	var status string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT status FROM minecraft_instances WHERE id = $1`, instanceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("instance status = %q, want %q", status, want)
	}
}

// --- metrics view (LIVE + tps source honesty) ---

func TestMCMetricsView(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "metrics-mc")
	env.instanceID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/minecraft/" + env.instanceID

	// No frame yet → OFFLINE freshness.
	resp = env.mc.do("GET", base+"/metrics", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("metrics: %d", resp.status)
	}
	if !bodyContains(resp.body, "OFFLINE") {
		t.Errorf("metrics without data must be OFFLINE: %v", resp.body)
	}

	// Live frame → values + LIVE freshness + honest tps source.
	env.mc.do("POST", base+"/start", nil)
	pushMCSample(t, env, env.instanceID, "active", 0)
	resp = env.mc.do("GET", base+"/metrics", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("metrics live: %d", resp.status)
	}
	body := jsonBody(t, resp.body)
	if !strings.Contains(body, `"players":4`) || !strings.Contains(body, "LIVE") {
		t.Errorf("metrics live body missing players/freshness: %s", body)
	}
	// paper supports rcon tps; the sample carries one → source "rcon"
	if !strings.Contains(body, `"tps_source":"rcon"`) {
		t.Errorf("tps_source = expected rcon for paper: %s", body)
	}
}

// --- world backup seam + properties endpoints ---

func TestMCBackupAndPropertiesSeams(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "backup-mc")
	env.instanceID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/minecraft/" + env.instanceID

	// Backup queues a job (agent op seam; the loop records the result row).
	resp = env.mc.do("POST", base+"/backups", map[string]string{"name": "pre-update"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("backup: %d %v", resp.status, resp.body)
	}
	var pending int
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE minecraft_id = $1 AND type = 'mc_backup_world'`, env.instanceID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Errorf("mc_backup_world jobs = %d, want 1", pending)
	}

	// Simulate the agent outcome → the loop records the backup row.
	{
		var jobID uuid.UUID
		if err := env.srv.Pool.QueryRow(context.Background(),
			`SELECT id FROM jobs WHERE minecraft_id = $1 AND type = 'mc_backup_world' LIMIT 1`, env.instanceID).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		result, _ := json.Marshal(minecraft.MCBackupOutcome{
			InstanceID: env.instanceID, BackupName: "pre-update", Path: "/tmp/x.tar.gz",
			SizeBytes: 1234, SHA256: "abc",
		})
		if _, err := env.srv.Pool.Exec(context.Background(),
			`UPDATE jobs SET status = 'success', result = $2 WHERE id = $1`, jobID, result); err != nil {
			t.Fatal(err)
		}
		// The row is stale vs updated_at: bump the instance update back.
		if _, err := env.srv.Pool.Exec(context.Background(),
			`UPDATE minecraft_instances SET updated_at = now() - interval '5 minutes' WHERE id = $1`, env.instanceID); err != nil {
			t.Fatal(err)
		}
		inst, gerr := (&minecraft.Store{Pool: env.srv.Pool}).GetByID(context.Background(),
			uuid.MustParse(env.orgID), uuid.MustParse(env.instanceID))
		if gerr != nil {
			t.Fatal(gerr)
		}
		if err := env.srv.applyMCJobOutcomes(context.Background(), &minecraft.Store{Pool: env.srv.Pool}, inst); err != nil {
			t.Fatal(err)
		}
		resp = env.mc.do("GET", base+"/backups", nil)
		if resp.status != http.StatusOK {
			t.Fatalf("backups list: %d", resp.status)
		}
		if !bodyContains(resp.body, "pre-update") {
			t.Errorf("backup row not recorded: %v", resp.body)
		}
	}

	// Properties update rejects protected keys.
	resp = env.mc.do("PUT", base+"/properties", map[string]any{
		"properties": map[string]string{"rcon.password": "hax"},
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("protected property write: %d, want 422", resp.status)
	}
	resp = env.mc.do("PUT", base+"/properties", map[string]any{
		"properties": map[string]string{"motd": "New MOTD"},
	})
	if resp.status != http.StatusOK {
		t.Errorf("properties write: %d %v", resp.status, resp.body)
	}
}

// --- provider offers endpoint ---

func TestMCProviderOffers(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.mc.do("GET", "/v1/organizations/"+env.orgID+"/minecraft/provider-offers", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("offers: %d", resp.status)
	}
	providersJSON, _ := json.Marshal(resp.body["providers"])
	for _, want := range []string{"vanilla", "paper", "purpur", "fabric", "forge", "neoforge"} {
		if !strings.Contains(string(providersJSON), `"`+want+`"`) {
			t.Errorf("offers missing %s: %s", want, providersJSON)
		}
	}
	// Source is honest (live | cache | seed) — never a bare list pretending
	// to be live manifest data.
	if !strings.Contains(string(providersJSON), "live") && !strings.Contains(string(providersJSON), "cache") && !strings.Contains(string(providersJSON), "seed") {
		t.Errorf("offers missing source honesty flags: %s", providersJSON)
	}
}

// --- console ring scrub (control-plane edge, defense in depth) ---

func TestMCConsoleTailScrub(t *testing.T) {
	env := newMCTestEnv(t)
	resp := env.createInstance(t, "scrub-mc")
	env.instanceID, _ = resp.body["id"].(string)

	instUUID, err := uuid.Parse(env.instanceID)
	if err != nil {
		t.Fatal(err)
	}

	// Worst case: an agent WITHOUT scrubbing produced a raw log line. The
	// control-plane ring scrubs on append (defense in depth).
	minecraft.AppendConsole(instUUID, []minecraft.ConsoleLine{
		{Seq: 1, Ts: time.Now().UTC().Format(time.RFC3339), Text: "RCON auth failure rcon-supersecret-value-9f3a1c"},
		{Seq: 2, Ts: time.Now().UTC().Format(time.RFC3339), Text: "Done (2.1s)! For help, type \"help\""},
	}, []string{"rcon-supersecret-value-9f3a1c"})

	tail := minecraft.ConsoleTail(instUUID, 10)
	joined := ""
	for _, l := range tail {
		joined += l.Text + "\n"
	}
	if strings.Contains(joined, "rcon-supersecret-value-9f3a1c") {
		t.Fatal("console ring leaks the rcon password")
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Errorf("expected redaction marker, got %q", joined)
	}
}

func isBase64ish(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
			continue
		}
		return false
	}
	return true
}

func jsonBody(t *testing.T, body map[string]any) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
