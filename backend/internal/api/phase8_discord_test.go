package api

// Phase 8 integration tests (DB: epicpanel_test_dc by default, override with
// EPICPANEL_TEST_DATABASE_URL). Covers: bot CRUD + plan gates, org scoping
// (cross-tenant 404), role enforcement, env write-only masking, secret
// scrubbing across API + job payloads, lifecycle state machine, runtime
// offers, schedules (no arbitrary commands), console input policy (no shell),
// and reconciliation vs injected agent truth (running + crash recovery).

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
	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

const secretValue = "super-secret-discord-token-42f8"

// botTestEnv boots the full app (auth/orgs) plus a phase-8 router sharing
// the same stores, behind one cookie jar.
type botTestEnv struct {
	srv      *Server
	app      *testClient // full app (register/login/orgs)
	bots     *testClient // phase-8 routes
	orgID    string
	serverID string
	botID    string
}

func newBotTestEnv(t *testing.T) *botTestEnv {
	t.Helper()
	if os.Getenv("EPICPANEL_TEST_DATABASE_URL") == "" {
		t.Setenv("EPICPANEL_TEST_DATABASE_URL",
			"postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_dc?sslmode=disable")
	}
	t.Setenv("EPICPANEL_DISABLE_BOT_LOOPS", "1")
	srv, client := newTestServer(t)

	// Deterministic tests drive reconciliation manually.
	mux := http.NewServeMux()
	authH := &auth.Handler{Users: srv.Users, Sessions: srv.Sessions, Audit: srv.Audit, Cfg: srv.Cfg,
		SetupDone: srv.setupCompleted, MFA: srv.MFA}
	mux.HandleFunc("POST /v1/auth/register", authH.Register)
	orgH := &organizations.Handler{Store: srv.Orgs, Audit: srv.Audit}
	orgH.Register(mux)
	registerPhase8(srv, mux)
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

	// Same jar (cookies ignore ports) against the phase-8 router.
	botClient := &testClient{t: t, base: ts.URL, http: client.http}

	env := &botTestEnv{srv: srv, app: client, bots: botClient}

	// Bootstrap: admin user + org on a Discord plan + a server row.
	client.do("POST", "/v1/auth/register", map[string]string{
		"email": "botadmin@example.test", "password": "supersecret123", "name": "Bot Admin",
	})
	resp := client.do("POST", "/v1/organizations", map[string]string{"name": "BotCo"})
	env.orgID, _ = resp.body["id"].(string)
	if env.orgID == "" {
		t.Fatalf("create org: %v", resp.body)
	}
	if _, err := srv.Pool.Exec(context.Background(),
		`UPDATE organizations SET package_id = (SELECT id FROM hosting_packages WHERE name = 'Discord Pro') WHERE id = $1`, env.orgID); err != nil {
		t.Fatalf("assign discord plan: %v", err)
	}
	resp = client.do("POST", "/v1/organizations/"+env.orgID+"/servers", map[string]string{"name": "node-1"})
	if srvMap, ok := resp.body["server"].(map[string]any); ok {
		env.serverID, _ = srvMap["id"].(string)
	}
	if env.serverID == "" {
		t.Fatalf("create server: %v", resp.body)
	}

	// Truncate bot tables (harness drop-list does not know them).
	ctx := context.Background()
	if _, err := srv.Pool.Exec(ctx, `UPDATE jobs SET bot_id = NULL`); err != nil {
		t.Fatalf("clear job links: %v", err)
	}
	for _, table := range []string{"bot_schedules", "bot_instances"} {
		if _, err := srv.Pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	return env
}

func (e *botTestEnv) createBot(t *testing.T, name string) response {
	t.Helper()
	return e.bots.do("POST", "/v1/organizations/"+e.orgID+"/bots", map[string]any{
		"name": name, "runtime": "node", "runtime_version": "22",
		"startup_file": "index.js", "server_id": e.serverID,
		"env_vars": map[string]string{"DISCORD_TOKEN": secretValue, "PUBLIC_VAR": "plain"},
	})
}

// botUserClient registers a fresh user (own jar) on the app server and
// returns a client bound to the phase-8 router (cookies ignore ports, so the
// session carries over).
func (e *botTestEnv) botUserClient(t *testing.T, email string) *testClient {
	t.Helper()
	c := e.app.NewClient()
	resp := c.do("POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "supersecret123", "name": email,
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register %s: %d %v", email, resp.status, resp.body)
	}
	return &testClient{t: t, base: e.bots.base, http: c.http}
}

// --- CRUD + plan gates ---

func TestBotCRUDAndPlanGate(t *testing.T) {
	env := newBotTestEnv(t)

	resp := env.createBot(t, "moderator-bot")
	if resp.status != http.StatusCreated {
		t.Fatalf("create bot: %d %v", resp.status, resp.body)
	}
	env.botID, _ = resp.body["id"].(string)
	if env.botID == "" {
		t.Fatalf("bot id missing: %v", resp.body)
	}
	if resp.body["status"] != "installing" {
		t.Errorf("initial status = %v, want installing", resp.body["status"])
	}

	// Install job exists, linked to the bot, payload has no plaintext secret.
	var jobID string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT id FROM jobs WHERE bot_id = $1 AND type = 'bot_install'`, env.botID).Scan(&jobID); err != nil {
		t.Fatalf("install job: %v", err)
	}
	var payloadRaw []byte
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE id = $1`, jobID).Scan(&payloadRaw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payloadRaw), secretValue) {
		t.Fatalf("install job payload leaks secret: %s", payloadRaw)
	}

	// Discord Pro allows 1 bot — the second create hits the plan gate.
	resp = env.createBot(t, "second-bot")
	if resp.status != http.StatusForbidden {
		t.Fatalf("second bot (plan max 1): %d %v, want 403", resp.status, resp.body)
	}

	// A web-plan org cannot create bots at all.
	resp = env.app.do("POST", "/v1/organizations", map[string]string{"name": "WebCo"})
	webOrg, _ := resp.body["id"].(string)
	if webOrg != "" {
		// keep the default plan (web) — create must be refused
		resp = env.bots.do("POST", "/v1/organizations/"+webOrg+"/bots", map[string]any{
			"name": "wb", "runtime": "node", "startup_file": "index.js",
		})
		if resp.status != http.StatusForbidden {
			t.Fatalf("bot on web plan: %d %v, want 403", resp.status, resp.body)
		}
	}

	// List + get carry env keys, never values.
	resp = env.bots.do("GET", "/v1/organizations/"+env.orgID+"/bots/"+env.botID, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("get bot: %d", resp.status)
	}
	if !bodyContains(resp.body, "DISCORD_TOKEN") {
		t.Error("env key should be listed")
	}
	if bodyContains(resp.body, secretValue) {
		t.Fatal("GET bot leaks the secret value")
	}

	// Delete queues the delete job.
	resp = env.bots.do("DELETE", "/v1/organizations/"+env.orgID+"/bots/"+env.botID, nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("delete bot: %d %v", resp.status, resp.body)
	}
	var status string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT status FROM bot_instances WHERE id = $1`, env.botID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "deleting" {
		t.Errorf("status after delete = %q, want deleting", status)
	}
}

// --- org scoping + role enforcement ---

func TestBotOrgScopingAndRoles(t *testing.T) {
	env := newBotTestEnv(t)
	resp := env.createBot(t, "scoped-bot")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.status, resp.body)
	}
	env.botID, _ = resp.body["id"].(string)

	// Second org (own admin) cannot see or mutate the bot: 404, not 403.
	outsider := env.botUserClient(t, "outsider@example.test")
	resp = outsider.do("POST", "/v1/organizations", map[string]string{"name": "OtherCo"})
	otherOrg, _ := resp.body["id"].(string)
	if otherOrg == "" {
		t.Fatalf("other org: %v", resp.body)
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		resp = outsider.do(method, "/v1/organizations/"+otherOrg+"/bots/"+env.botID, map[string]any{})
		if resp.status != http.StatusNotFound {
			t.Errorf("cross-tenant %s: %d, want 404", method, resp.status)
		}
	}
	resp = outsider.do("POST", "/v1/organizations/"+otherOrg+"/bots/"+env.botID+"/start", nil)
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant start: %d, want 404", resp.status)
	}
	// direct org-id reference (no bot in that org path) — same bot id in the
	// URL but a foreign org is always 404; even the real org path is denied
	// for non-members via 404 on the org itself.
	resp = outsider.do("GET", "/v1/organizations/"+env.orgID+"/bots/"+env.botID, nil)
	if resp.status != http.StatusNotFound {
		t.Errorf("non-member on real org: %d, want 404 (no existence leak)", resp.status)
	}

	// A viewer (billing rank < developer) can read but not mutate.
	viewer := env.botUserClient(t, "viewer@example.test")
	mresp := env.app.do("POST", "/v1/organizations/"+env.orgID+"/members", map[string]string{
		"email": "viewer@example.test", "role": "billing",
	})
	if mresp.status != http.StatusOK && mresp.status != http.StatusCreated {
		t.Fatalf("add viewer member: %d %v", mresp.status, mresp.body)
	}
	resp = viewer.do("GET", "/v1/organizations/"+env.orgID+"/bots", nil)
	if resp.status != http.StatusOK {
		t.Errorf("viewer read: %d, want 200", resp.status)
	}
	resp = viewer.do("POST", "/v1/organizations/"+env.orgID+"/bots/"+env.botID+"/stop", nil)
	if resp.status != http.StatusForbidden {
		t.Errorf("viewer stop: %d, want 403", resp.status)
	}
	resp = viewer.do("PUT", "/v1/organizations/"+env.orgID+"/bots/"+env.botID+"/env", map[string]any{"vars": map[string]string{}})
	if resp.status != http.StatusForbidden {
		t.Errorf("viewer env write: %d, want 403", resp.status)
	}
}

// --- env write-only masking + secret never in payloads ---

func TestBotEnvWriteOnly(t *testing.T) {
	env := newBotTestEnv(t)
	resp := env.createBot(t, "env-bot")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d", resp.status)
	}
	env.botID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/bots/" + env.botID

	// GET env: keys only.
	resp = env.bots.do("GET", base+"/env", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("get env: %d", resp.status)
	}
	if bodyContains(resp.body, secretValue) {
		t.Fatal("GET env leaks the secret value")
	}

	// PUT env replaces the set; response masked.
	resp = env.bots.do("PUT", base+"/env", map[string]any{
		"vars": map[string]string{"API_KEY": secretValue + "-2", "DEBUG": "1"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("put env: %d %v", resp.status, resp.body)
	}
	if bodyContains(resp.body, secretValue) {
		t.Fatal("PUT env response leaks the secret value")
	}

	// start job payload: env rides as ciphertext only.
	resp = env.bots.do("POST", base+"/start", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("start: %d %v", resp.status, resp.body)
	}
	var payloadRaw []byte
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE bot_id = $1 AND type = 'bot_start' ORDER BY created_at DESC LIMIT 1`,
		env.botID).Scan(&payloadRaw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payloadRaw), secretValue) {
		t.Fatal("start job payload leaks the secret value")
	}
	var payloadMap map[string]any
	if err := json.Unmarshal(payloadRaw, &payloadMap); err != nil {
		t.Fatal(err)
	}
	if enc, _ := payloadMap["env_enc"].(string); enc == "" {
		t.Error("start payload must carry env_enc ciphertext")
	}

	// Bot row at rest: the ciphertext column must not contain plaintext.
	var envEnc string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT env_enc FROM bot_instances WHERE id = $1`, env.botID).Scan(&envEnc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(envEnc, secretValue) {
		t.Fatal("env stored in plaintext at rest")
	}

	// Key deletion is masked too.
	resp = env.bots.do("DELETE", base+"/env/DEBUG", nil)
	if resp.status != http.StatusOK || bodyContains(resp.body, secretValue) {
		t.Fatalf("delete env key: %d %v", resp.status, resp.body)
	}
	// invalid key rejected
	resp = env.bots.do("DELETE", base+"/env/BAD%20KEY", nil)
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("invalid key: %d, want 422", resp.status)
	}
}

// --- runtime offers + java stub + no-shell console policy ---

func TestBotRuntimeOffersAndNoShell(t *testing.T) {
	env := newBotTestEnv(t)

	resp := env.bots.do("GET", "/v1/organizations/"+env.orgID+"/bots/runtime-offers", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("offers: %d", resp.status)
	}
	runtimesJSON, _ := json.Marshal(resp.body["runtimes"])
	if !strings.Contains(string(runtimesJSON), "node") || !strings.Contains(string(runtimesJSON), "python") {
		t.Errorf("offers missing node/python: %s", runtimesJSON)
	}

	// java is a foundation stub: creation refused with a clear message.
	resp = env.bots.do("POST", "/v1/organizations/"+env.orgID+"/bots", map[string]any{
		"name": "javabot", "runtime": "java", "startup_file": "Main.java", "server_id": env.serverID,
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("java create: %d %v, want 422", resp.status, resp.body)
	}

	// Schedules: custom commands would be shell-by-another-name — refused.
	resp = env.createBot(t, "sched-bot")
	env.botID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/bots/" + env.botID
	resp = env.bots.do("POST", base+"/schedules", map[string]string{"kind": "restart", "cron": "*/10 * * * *"})
	if resp.status != http.StatusCreated {
		t.Fatalf("restart schedule: %d %v", resp.status, resp.body)
	}
	resp = env.bots.do("POST", base+"/schedules", map[string]string{"kind": "command", "cron": "* * * * *"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("command schedule must be refused: %d", resp.status)
	}
	resp = env.bots.do("POST", base+"/schedules", map[string]string{"kind": "restart", "cron": "not a cron"})
	if resp.status != http.StatusUnprocessableEntity {
		t.Errorf("bad cron: %d, want 422", resp.status)
	}

	// Console input policy: there is no input endpoint at all (no shell, no
	// docker) — a POST to the console route must not exist.
	resp = env.bots.do("POST", base+"/console", map[string]string{"input": "rm -rf /"})
	if resp.status != http.StatusMethodNotAllowed && resp.status != http.StatusNotFound {
		t.Errorf("console input route must not exist: %d", resp.status)
	}
}

// --- lifecycle guards + reconciliation vs injected agent truth ---

func TestBotLifecycleAndReconciliation(t *testing.T) {
	env := newBotTestEnv(t)
	resp := env.createBot(t, "lifecycle-bot")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d", resp.status)
	}
	env.botID, _ = resp.body["id"].(string)
	base := "/v1/organizations/" + env.orgID + "/bots/" + env.botID
	ctx := context.Background()

	// start from installing → starting + bot_start job queued
	resp = env.bots.do("POST", base+"/start", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("start: %d %v", resp.status, resp.body)
	}
	assertBotStatus(t, env, env.botID, "starting")

	// Agent truth: unit active (LIVE sample) → running.
	pushBotSample(t, env, env.botID, "active", 3)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "running")

	// Crash: unit failed while desired running → crashed + policy restart
	// (on-failure, episode 1) → starting with a crash-recovery job.
	pushBotSample(t, env, env.botID, "failed", 1)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "starting")
	var restartCount, episodeRestarts int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT restart_count, episode_restarts FROM bot_instances WHERE id = $1`, env.botID).Scan(&restartCount, &episodeRestarts); err != nil {
		t.Fatal(err)
	}
	if restartCount != 1 || episodeRestarts != 1 {
		t.Errorf("crash counters = %d/%d, want 1/1", restartCount, episodeRestarts)
	}
	var pending int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE bot_id = $1 AND type = 'bot_start' AND status = 'pending' AND idempotency_key LIKE 'botcrash-%'`, env.botID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Errorf("crash recovery job pending = %d, want 1", pending)
	}

	// Recovered: agent truth active again → running.
	pushBotSample(t, env, env.botID, "active", 1)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "running")

	// stop → stopping; agent truth inactive → stopped.
	resp = env.bots.do("POST", base+"/stop", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("stop: %d", resp.status)
	}
	pushBotSample(t, env, env.botID, "inactive", 1)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "stopped")

	// double-stop is idempotent (no 409)
	resp = env.bots.do("POST", base+"/stop", nil)
	if resp.status != http.StatusAccepted {
		t.Errorf("double stop: %d, want 202", resp.status)
	}

	// "no" restart policy: crash leaves the bot crashed, no recovery job.
	if _, err := env.srv.Pool.Exec(ctx, `UPDATE bot_instances SET restart_policy = 'no' WHERE id = $1`, env.botID); err != nil {
		t.Fatal(err)
	}
	env.bots.do("POST", base+"/start", nil)
	pushBotSample(t, env, env.botID, "active", 0)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "running")
	pushBotSample(t, env, env.botID, "failed", 5)
	env.srv.reconcileBots(ctx)
	assertBotStatus(t, env, env.botID, "crashed")
	var recovery int
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE bot_id = $1 AND type = 'bot_start' AND status = 'pending'
		 AND idempotency_key LIKE 'botcrash-%'`, env.botID).Scan(&recovery); err != nil {
		t.Fatal(err)
	}
	if recovery != 1 {
		t.Errorf("crash-recovery jobs = %d, want 1 (only the on-failure crash; policy 'no' adds none)", recovery)
	}
}

// pushBotSample injects a LIVE metrics frame carrying the bot's discord
// envelope (what a real agent would stream for kind=discord units).
func pushBotSample(t *testing.T, env *botTestEnv, botID, status string, restarts int) {
	t.Helper()
	var serverID string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT server_id FROM bot_instances WHERE id = $1`, botID).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	sid, err := uuid.Parse(serverID)
	if err != nil {
		t.Fatal(err)
	}
	sample := agentproto.Sample{
		Node: agentproto.NodeSample{CPUPercent: 1.0},
		Apps: []agentproto.AppSample{{
			WebsiteID: botID, Kind: "discord", Status: status,
			CPUPercent: 12.5, MemoryBytes: 48 * 1024 * 1024,
			RestartCount: restarts, UptimeS: 60,
		}},
	}
	data, _ := json.Marshal(sample)
	frame := agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "test-session", Seq: time.Now().UnixNano(), Ts: time.Now().UTC(), Data: data}
	if !env.srv.LiveStore.Ingest(sid, frame) {
		t.Fatal("frame ingested as duplicate")
	}
}

func assertBotStatus(t *testing.T, env *botTestEnv, botID, want string) {
	t.Helper()
	var status string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT status FROM bot_instances WHERE id = $1`, botID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("bot status = %q, want %q", status, want)
	}
}

// --- console snapshot scrub (control-plane edge, defense in depth) ---

func TestBotConsoleTailScrub(t *testing.T) {
	env := newBotTestEnv(t)
	resp := env.createBot(t, "console-bot")
	if resp.status != http.StatusCreated {
		t.Fatalf("create: %d", resp.status)
	}
	env.botID, _ = resp.body["id"].(string)

	botUUID, err := uuid.Parse(env.botID)
	if err != nil {
		t.Fatal(err)
	}

	// Worst case: an agent WITHOUT scrubbing produced a raw log line. The
	// control-plane ring scrubs on append (defense in depth).
	discord.AppendConsole(botUUID, []discord.ConsoleLine{
		{Seq: 1, Ts: time.Now().UTC().Format(time.RFC3339), Text: "token=" + secretValue},
		{Seq: 2, Ts: time.Now().UTC().Format(time.RFC3339), Text: "ready"},
	}, []string{secretValue})

	tail := discord.ConsoleTail(botUUID, 10)
	joined := ""
	for _, l := range tail {
		joined += l.Text + "\n"
	}
	if strings.Contains(joined, secretValue) {
		t.Fatal("console ring leaks the secret value")
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Errorf("expected redaction marker, got %q", joined)
	}
}

// --- helpers ---

func bodyContains(body map[string]any, needle string) bool {
	b, _ := json.Marshal(body)
	return strings.Contains(string(b), needle)
}
