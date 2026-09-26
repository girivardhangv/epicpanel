package api

// Lifecycle reasons + the bandwidth resume guard (migration 0050, plan G1/G6).
// API-level tests following the phase4 suspend/resume round-trip:
// enqueue -> agent claim -> result -> fanout -> persisted state.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// suspendViaAgent enqueues a suspend_website job through the internal job
// store (bypassing the user API — the system-reserved reasons must never be
// forgeable there) and drives it through the agent round-trip so the fanout
// persists the status + reason.
func suspendViaAgent(t *testing.T, srv *Server, agent *testClient, websiteID, reason string, metadata map[string]any) {
	t.Helper()
	ctx := context.Background()
	ws, err := srv.Websites.GetByIDAny(ctx, uuid.MustParse(websiteID))
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	payload := websites.SuspendPayload{WebsiteID: websiteID, Reason: reason, Metadata: metadata}
	job, err := srv.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite, payload, "test_suspend_"+reason+"_"+websiteID)
	if err != nil {
		t.Fatalf("enqueue suspend: %v", err)
	}
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	if claim.status != http.StatusOK {
		t.Fatalf("claim suspend: %d %v", claim.status, claim.body)
	}
	res := agent.do("POST", "/v1/agent/jobs/"+job.ID.String()+"/result", map[string]any{
		"success": true, "result": map[string]any{"suspended": true},
	})
	if res.status != http.StatusOK {
		t.Fatalf("suspend result: %d %v", res.status, res.body)
	}
}

// TestPhaseLifecycleReasons covers the suspend body validation, the
// reason-aware fanout persistence and the website JSON surface.
func TestPhaseLifecycleReasons(t *testing.T) {
	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)

	// bandwidth_exhausted is system-reserved: the user API must reject it.
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend",
		map[string]string{"reason": "bandwidth_exhausted"})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("reserved reason must be rejected: %d %v", resp.status, resp.body)
	}
	// attack is system-reserved too.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend",
		map[string]string{"reason": "attack"})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("reserved reason attack must be rejected: %d %v", resp.status, resp.body)
	}
	// Unknown reason rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend",
		map[string]string{"reason": "because"})
	if resp.status != http.StatusUnprocessableEntity && resp.status != http.StatusBadRequest {
		t.Fatalf("unknown reason must be rejected: %d %v", resp.status, resp.body)
	}

	// Valid user reason with an operator message.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend",
		map[string]string{"reason": "abuse", "message": "phishing report"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("suspend: %d %v", resp.status, resp.body)
	}
	jobID, _ := resp.body["job_id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "suspend_website" {
		t.Fatalf("claim: %v", claim.body)
	}
	pb, _ := json.Marshal(job["payload"])
	var sp websites.SuspendPayload
	if err := json.Unmarshal(pb, &sp); err != nil {
		t.Fatalf("payload decode: %v (%s)", err, pb)
	}
	if sp.Reason != "abuse" || sp.Message != "phishing report" {
		t.Fatalf("payload must carry reason + message: %s", pb)
	}
	_ = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true, "result": map[string]any{}})

	// The fanout persisted the reason; the website JSON surfaces it.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "suspended" {
		t.Fatalf("status: %v want suspended", resp.body["status"])
	}
	if resp.body["suspension_reason"] != "abuse" {
		t.Fatalf("suspension_reason: %v want abuse", resp.body["suspension_reason"])
	}
	if _, ok := resp.body["suspended_at"]; !ok {
		t.Fatalf("suspended_at missing: %v", resp.body)
	}

	// The audit row carries the reason.
	var reason string
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT metadata->>'reason' FROM audit_logs WHERE action = 'website.suspend'
		 ORDER BY created_at DESC LIMIT 1`).Scan(&reason); err != nil || reason != "abuse" {
		t.Fatalf("audit reason: %q err=%v", reason, err)
	}

	// Resume clears the reason.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("resume: %d %v", resp.status, resp.body)
	}
	rj, _ := resp.body["job_id"].(string)
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+rj+"/result", map[string]any{"success": true, "result": map[string]any{}})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "ready" || resp.body["suspension_reason"] != nil {
		t.Fatalf("resume must clear reason: status=%v reason=%v", resp.body["status"], resp.body["suspension_reason"])
	}

	// Default (no body) suspend = manual.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend", nil)
	if resp.status != http.StatusAccepted {
		t.Fatalf("suspend default: %d %v", resp.status, resp.body)
	}
	dj, _ := resp.body["job_id"].(string)
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+dj+"/result", map[string]any{"success": true, "result": map[string]any{}})
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["suspension_reason"] != "manual" {
		t.Fatalf("default reason: %v want manual", resp.body["suspension_reason"])
	}
}

// TestPhaseResumeGuardQuotaBoundary is the byte-exact quota boundary suite:
// a site suspended for bandwidth_exhausted stays suspended while
// current-month usage >= effective limit — including the exact boundary —
// unless the admin forces (audited).
func TestPhaseResumeGuardQuotaBoundary(t *testing.T) {
	const limitBytes = int64(10) * 1024 * 1024 * 1024 // 10 GiB
	const limitMB = int64(10240)

	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)
	ctx := context.Background()

	// Per-site override: 10 GiB monthly (no hosting-package dependency).
	if _, err := srv.Pool.Exec(ctx,
		`UPDATE websites SET bandwidth_limit_mb = $1 WHERE id = $2`, limitMB, uuid.MustParse(websiteID)); err != nil {
		t.Fatalf("set override: %v", err)
	}

	suspendViaAgent(t, srv, agent, websiteID, string(websites.ReasonBandwidthExhausted), map[string]any{
		"used_bytes":  float64(limitBytes) - 1024,
		"limit_bytes": float64(limitBytes),
	})

	// used = limit - 1 byte -> below the boundary -> resume allowed.
	setBandwidthUsage(t, srv, websiteID, limitBytes-1)
	if resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil); resp.status != http.StatusAccepted {
		t.Fatalf("resume below limit: %d %v", resp.status, resp.body)
	}
	drainResume(t, srv, agent, websiteID)

	// Re-suspend for the == boundary case.
	suspendViaAgent(t, srv, agent, websiteID, string(websites.ReasonBandwidthExhausted), nil)

	// used == limit (byte-exact, 10737418240) -> 409 bandwidth_quota_exhausted.
	setBandwidthUsage(t, srv, websiteID, limitBytes)
	resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("resume at exact limit must be 409: %d %v", resp.status, resp.body)
	}
	errObj, _ := resp.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "bandwidth_quota_exhausted" {
		t.Fatalf("error code: %v", resp.body)
	}
	details, _ := errObj["details"].(map[string]any)
	if details == nil || details["used_bytes"] != float64(limitBytes) || details["limit_bytes"] != float64(limitBytes) {
		t.Fatalf("guard details must be byte-exact: %v", errObj)
	}

	// used = limit + 1 byte -> still 409.
	setBandwidthUsage(t, srv, websiteID, limitBytes+1)
	if resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil); resp.status != http.StatusConflict {
		t.Fatalf("resume over limit: %d %v", resp.status, resp.body)
	}

	// force=true bypasses and is audited.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", map[string]bool{"force": true})
	if resp.status != http.StatusAccepted {
		t.Fatalf("forced resume: %d %v", resp.status, resp.body)
	}
	rj, _ := resp.body["job_id"].(string)
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+rj+"/result", map[string]any{"success": true, "result": map[string]any{}})
	if resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil); resp.body["status"] != "ready" {
		t.Fatalf("forced resume status: %v", resp.body["status"])
	}
	var n int
	if err := srv.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE action = 'website.resume_forced' AND resource_id = $1`, websiteID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("resume_forced audit rows: %d err=%v", n, err)
	}

	// Non-bandwidth reasons resume freely even with the quota exhausted.
	suspendViaAgent(t, srv, agent, websiteID, string(websites.ReasonManual), nil)
	setBandwidthUsage(t, srv, websiteID, limitBytes*2)
	if resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil); resp.status != http.StatusAccepted {
		t.Fatalf("manual reason must bypass the bandwidth guard: %d %v", resp.status, resp.body)
	}
	drainResume(t, srv, agent, websiteID)
}

// TestPhaseResumeGuardUnlimitedOverride: bandwidth_limit_mb = 0 means
// unlimited — the guard never blocks.
func TestPhaseResumeGuardUnlimitedOverride(t *testing.T) {
	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)
	if _, err := srv.Pool.Exec(context.Background(),
		`UPDATE websites SET bandwidth_limit_mb = 0 WHERE id = $1`, uuid.MustParse(websiteID)); err != nil {
		t.Fatalf("set override: %v", err)
	}
	suspendViaAgent(t, srv, agent, websiteID, string(websites.ReasonBandwidthExhausted), nil)
	setBandwidthUsage(t, srv, websiteID, 1<<40)
	if resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/resume", nil); resp.status != http.StatusAccepted {
		t.Fatalf("unlimited override must resume: %d %v", resp.status, resp.body)
	}
	drainResume(t, srv, agent, websiteID)
}

// TestPhaseOverLimitSuspendCarriesQuotaMetadata: the enforce outcome fanout
// suspends for bandwidth with the system-reserved reason AND the quota
// metadata (used/limit/period_start/resets_at) the agent needs to render the
// bandwidth-exhausted page, and persists it on success.
func TestPhaseOverLimitSuspendCarriesQuotaMetadata(t *testing.T) {
	const limitBytes = int64(5) * 1024 * 1024 * 1024 // 5 GiB
	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)

	// Override the limit so the fanout resolves it.
	if _, err := srv.Pool.Exec(context.Background(),
		`UPDATE websites SET bandwidth_limit_mb = $1 WHERE id = $2`, limitBytes/(1024*1024), uuid.MustParse(websiteID)); err != nil {
		t.Fatalf("set override: %v", err)
	}

	// Fabricate an enforce outcome: 6 GiB used vs the 5 GiB budget (breach).
	usedMB := float64(6 * 1024)
	outcome := enforceOutcome{
		WebsiteID: websiteID,
		Plan:      "test",
		Usage:     map[string]float64{"bandwidth": usedMB},
		Breaches: []agentBreach{{
			Resource: "bandwidth",
			Usage:    "6144.0MB",
			Limit:    "5120.0MB",
			Action:   "suspend",
		}},
	}
	raw, _ := json.Marshal(outcome)
	wid := uuid.MustParse(websiteID)
	job := &jobs.Job{Type: jobs.TypeEnforceLimits, Status: jobs.StatusSuccess, WebsiteID: &wid}
	srv.applyEnforceOutcome(context.Background(), job, raw)

	// The suspend job carries the reason + metadata.
	var payloadRaw []byte
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT payload FROM jobs WHERE type = 'suspend_website' AND website_id = $1
		 ORDER BY created_at DESC LIMIT 1`, wid).Scan(&payloadRaw); err != nil {
		t.Fatalf("suspend job query: %v", err)
	}
	var sp websites.SuspendPayload
	if err := json.Unmarshal(payloadRaw, &sp); err != nil {
		t.Fatalf("payload: %v (%s)", err, payloadRaw)
	}
	if sp.Reason != string(websites.ReasonBandwidthExhausted) {
		t.Fatalf("reason: %q want bandwidth_exhausted", sp.Reason)
	}
	wantUsed := int64(6 * 1024 * 1024 * 1024)
	if got := int64(sp.Metadata["used_bytes"].(float64)); got != wantUsed {
		t.Fatalf("used_bytes: %d want %d", got, wantUsed)
	}
	if got := int64(sp.Metadata["limit_bytes"].(float64)); got != limitBytes {
		t.Fatalf("limit_bytes: %d want %d", got, limitBytes)
	}
	for _, k := range []string{"period_start", "resets_at"} {
		if _, ok := sp.Metadata[k].(string); !ok {
			t.Fatalf("metadata %s missing: %v", k, sp.Metadata)
		}
	}

	// Agent success -> suspended with the persisted reason.
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	jb, _ := claim.body["job"].(map[string]any)
	if jb == nil || jb["type"] != "suspend_website" {
		t.Fatalf("claim: %v", claim.body)
	}
	_ = agent.do("POST", "/v1/agent/jobs/"+jb["id"].(string)+"/result", map[string]any{"success": true, "result": map[string]any{}})
	resp := admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID, nil)
	if resp.body["status"] != "suspended" || resp.body["suspension_reason"] != "bandwidth_exhausted" {
		t.Fatalf("fanout state: %v / %v", resp.body["status"], resp.body["suspension_reason"])
	}
	meta, _ := resp.body["suspension_metadata"].(map[string]any)
	if meta == nil || meta["resets_at"] == nil {
		t.Fatalf("persisted metadata: %v", resp.body["suspension_metadata"])
	}
}

// TestPhaseSuspendIdempotentConcurrent fires concurrent double-suspends:
// the idempotency key must collapse them into one job.
func TestPhaseSuspendIdempotentConcurrent(t *testing.T) {
	orgID, _, websiteID, admin, _, _ := phase4SetupWithServer(t)

	const n = 8
	var wg sync.WaitGroup
	ids := make([]string, n)
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/suspend", nil)
			if resp.status != http.StatusAccepted {
				t.Errorf("suspend %d: %d %v", i, resp.status, resp.body)
				return
			}
			mu.Lock()
			ids[i], _ = resp.body["job_id"].(string)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	first := ids[0]
	if first == "" {
		t.Fatalf("no job id")
	}
	for i, id := range ids {
		if id != first {
			t.Fatalf("concurrent suspend %d produced a second job: %s vs %s", i, id, first)
		}
	}
}

// --- helpers ----------------------------------------------------------------

// phase4SetupWithServer extends phase4Setup with the raw Server handle.
func phase4SetupWithServer(t *testing.T) (orgID, serverID, websiteID string, admin, agent *testClient, srv *Server) {
	t.Helper()
	srv, admin = newTestServer(t)

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
	return orgID, serverID, websiteID, admin, agent, srv
}

// setBandwidthUsage upserts the current-month workload_resource_usage row
// with a byte-exact usage (stored as fractional MB in the NUMERIC column so
// the guard compares real bytes).
func setBandwidthUsage(t *testing.T, srv *Server, websiteID string, usedBytes int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := srv.Pool.Exec(ctx, `
		INSERT INTO workload_resource_usage (organization_id, website_id, resource, period_start, used, limit_value, unit, measured_by)
		SELECT organization_id, id, 'bandwidth', date_trunc('month', now()), $2::bigint::numeric / 1048576, 0, 'MB', 'test'
		FROM websites WHERE id = $1
		ON CONFLICT (website_id, resource, period_start) DO UPDATE SET used = EXCLUDED.used
	`, uuid.MustParse(websiteID), usedBytes); err != nil {
		t.Fatalf("set usage: %v", err)
	}
}

// drainResume claims + succeeds the pending resume job (if any).
func drainResume(t *testing.T, srv *Server, agent *testClient, websiteID string) {
	t.Helper()
	ctx := context.Background()
	ws, err := srv.Websites.GetByIDAny(ctx, uuid.MustParse(websiteID))
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	var jobID string
	if err := srv.Pool.QueryRow(ctx,
		`SELECT id::text FROM jobs WHERE website_id = $1 AND type = 'resume_website' AND status = 'pending'
		 ORDER BY created_at DESC LIMIT 1`, ws.ID).Scan(&jobID); err != nil {
		return // nothing pending
	}
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	_ = agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true, "result": map[string]any{}})
}

// TestPhaseQuotaEndpoints covers the quota read/patch surface: plan default,
// per-site override set/clear, validation, and the bandwidth summary shape.
func TestPhaseQuotaEndpoints(t *testing.T) {
	orgID, _, websiteID, admin, _, srv := phase4SetupWithServer(t)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID

	// Default: plan-sourced (whatever the fixture package carries).
	resp := admin.do("GET", base+"/quota", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("quota: %d %v", resp.status, resp.body)
	}
	q, _ := resp.body["bandwidth"].(map[string]any)
	if q == nil || q["period"] != "monthly" || q["source"] != "plan" {
		t.Fatalf("quota block: %v", resp.body)
	}

	// PATCH sets the override; GET reports source=site and the byte math.
	resp = admin.do("PATCH", base+"/quota", map[string]any{"bandwidth_limit_mb": 10240})
	if resp.status != http.StatusOK {
		t.Fatalf("patch quota: %d %v", resp.status, resp.body)
	}
	resp = admin.do("GET", base+"/quota", nil)
	q, _ = resp.body["bandwidth"].(map[string]any)
	if q == nil || q["source"] != "site" {
		t.Fatalf("override source: %v", resp.body)
	}
	if q["limit_bytes"].(float64) != float64(10240*1024*1024) {
		t.Fatalf("limit_bytes: %v", q["limit_bytes"])
	}

	// Negative limit rejected; null clears back to plan.
	if resp := admin.do("PATCH", base+"/quota", map[string]any{"bandwidth_limit_mb": -1}); resp.status == http.StatusOK {
		t.Fatalf("negative limit must not be accepted: %v", resp.body)
	}
	resp = admin.do("PATCH", base+"/quota", map[string]any{"bandwidth_limit_mb": nil})
	if resp.status != http.StatusOK {
		t.Fatalf("clear override: %d %v", resp.status, resp.body)
	}
	resp = admin.do("GET", base+"/quota", nil)
	q, _ = resp.body["bandwidth"].(map[string]any)
	if q == nil || q["source"] != "plan" {
		t.Fatalf("cleared override must fall back to plan: %v", resp.body)
	}

	// Bandwidth summary: shape + usage reflection.
	setBandwidthUsage(t, srv, websiteID, 5*1024*1024*1024)
	resp = admin.do("GET", base+"/bandwidth", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("bandwidth: %d %v", resp.status, resp.body)
	}
	if _, ok := resp.body["rate"]; !ok {
		t.Fatalf("bandwidth summary missing rate: %v", resp.body)
	}
	tr, _ := resp.body["traffic"].(map[string]any)
	if tr == nil || tr["total_bytes"].(float64) != float64(5*1024*1024*1024) {
		t.Fatalf("traffic totals: %v", resp.body)
	}
}

// mustServerFromSetup re-fetches the Server for direct store access in
// tests that only carry the HTTP clients.
func TestPhaseBandwidthHistory(t *testing.T) {
	orgID, _, websiteID, admin, _, srv := phase4SetupWithServer(t)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID
	ctx := context.Background()

	h1 := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	h2 := time.Now().UTC().Truncate(time.Hour).Add(-1 * time.Hour)
	for _, h := range []time.Time{h1, h2} {
		if _, err := srv.Pool.Exec(ctx, `
			INSERT INTO website_bandwidth_samples (website_id, hour_bucket, tx_bytes, requests)
			VALUES ($1, $2, 1000, 10) ON CONFLICT (website_id, hour_bucket)
			DO UPDATE SET tx_bytes = website_bandwidth_samples.tx_bytes + 1000`,
			uuid.MustParse(websiteID), h); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
		if _, err := srv.Pool.Exec(ctx, `
			INSERT INTO website_bandwidth_daily (website_id, day, tx_bytes, requests)
			VALUES ($1, $2::date, 2000, 20) ON CONFLICT (website_id, day)
			DO UPDATE SET tx_bytes = website_bandwidth_daily.tx_bytes + 2000`,
			uuid.MustParse(websiteID), h.Format("2006-01-02")); err != nil {
			t.Fatalf("seed daily: %v", err)
		}
	}

	from := h1.Add(-time.Hour).Format(time.RFC3339)
	to := h2.Add(2 * time.Hour).Format(time.RFC3339)
	resp := admin.do("GET", base+"/bandwidth/history?from="+from+"&to="+to+"&interval=hour", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("history hour: %d %v", resp.status, resp.body)
	}
	data, _ := resp.body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("hour buckets: %d (%v)", len(data), resp.body)
	}
	total := 0.0
	for _, d := range data {
		total += d.(map[string]any)["total_bytes"].(float64)
	}
	if total != 2000 {
		t.Fatalf("hourly total: %v", total)
	}

	resp = admin.do("GET", base+"/bandwidth/history?from="+from+"&to="+to+"&interval=day", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("history day: %d %v", resp.status, resp.body)
	}
	data, _ = resp.body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("daily buckets must fold one day: %d (%v)", len(data), resp.body)
	}
	if data[0].(map[string]any)["total_bytes"].(float64) != 4000 {
		t.Fatalf("daily total: %v", data[0])
	}

	if resp := admin.do("GET", base+"/bandwidth/history?interval=minute", nil); resp.status == http.StatusOK {
		t.Fatalf("invalid interval must be rejected")
	}
	if resp := admin.do("GET", base+"/bandwidth/history?from=nonsense", nil); resp.status == http.StatusOK {
		t.Fatalf("invalid from must be rejected")
	}
}

// TestPhaseQuotaOverrideReachesEnforcePayload is the anti-drift proof: the
// per-site override must flow into THE shared enforce payload builder so
// agent-side budget == quota API == resume guard.
func TestPhaseQuotaOverrideReachesEnforcePayload(t *testing.T) {
	orgID, _, websiteID, admin, _, srv := phase4SetupWithServer(t)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID
	ctx := context.Background()

	resp := admin.do("PATCH", base+"/quota", map[string]any{"bandwidth_limit_mb": 5120})
	if resp.status != http.StatusOK {
		t.Fatalf("patch: %d %v", resp.status, resp.body)
	}
	ws, err := srv.Websites.GetByIDAny(ctx, uuid.MustParse(websiteID))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	payload, err := srv.dynamicEffectivePayload(ctx, ws)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.BandwidthMB != 5120 {
		t.Fatalf("enforce payload bandwidth: %d want 5120", payload.BandwidthMB)
	}
	// Clearing the override returns the payload to the plan budget.
	if resp := admin.do("PATCH", base+"/quota", map[string]any{"bandwidth_limit_mb": nil}); resp.status != http.StatusOK {
		t.Fatalf("clear: %d", resp.status)
	}
	ws, _ = srv.Websites.GetByIDAny(ctx, ws.ID)
	payload2, err := srv.dynamicEffectivePayload(ctx, ws)
	if err != nil {
		t.Fatalf("payload2: %v", err)
	}
	if payload2.BandwidthMB == 5120 {
		t.Fatalf("cleared override must not pin the payload budget")
	}
}

// TestPhaseTerminatePurgeLifecycle drives the full termination lifecycle
// over the HTTP API: confirm-gated terminate -> agent round-trip ->
// terminated status -> blocked suspend/resume/delete -> confirm-gated
// purge (the destructive delete job) -> row removed.
func TestPhaseTerminatePurgeLifecycle(t *testing.T) {
	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID

	// Termination requires an explicit confirm.
	if resp := admin.do("POST", base+"/terminate", map[string]any{"reason": "nope"}); resp.status == http.StatusAccepted {
		t.Fatalf("terminate without confirm must not enqueue: %v", resp.body)
	}
	resp := admin.do("POST", base+"/terminate", map[string]any{"reason": "abuse cleanup", "confirm": true})
	if resp.status != http.StatusAccepted {
		t.Fatalf("terminate: %d %v", resp.status, resp.body)
	}
	jobID, _ := resp.body["job_id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "terminate_website" {
		t.Fatalf("claim: %v", claim.body)
	}
	var tp websites.TerminatePayload
	switch pv := job["payload"].(type) {
	case string:
		if raw, derr := base64.StdEncoding.DecodeString(pv); derr == nil {
			_ = json.Unmarshal(raw, &tp)
		}
	case map[string]any:
		if b, merr := json.Marshal(pv); merr == nil {
			_ = json.Unmarshal(b, &tp)
		}
	}
	if tp.Reason != "abuse cleanup" {
		t.Fatalf("terminate payload: %v", job["payload"])
	}
	if res := agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{"success": true, "result": map[string]any{"terminated": true}}); res.status != http.StatusOK {
		t.Fatalf("terminate result: %d %v", res.status, res.body)
	}

	resp = admin.do("GET", base, nil)
	if resp.body["status"] != "terminated" {
		t.Fatalf("status: %v want terminated", resp.body["status"])
	}
	if resp.body["terminated_at"] == nil || resp.body["termination_reason"] != "abuse cleanup" {
		t.Fatalf("termination surface: %v", resp.body)
	}

	// A terminated site is a dead end until purge: no suspend/resume/delete.
	if resp := admin.do("POST", base+"/terminate", map[string]any{"confirm": true}); resp.status == http.StatusAccepted {
		t.Fatalf("re-terminate must conflict")
	}
	if resp := admin.do("POST", base+"/resume", nil); resp.status == http.StatusAccepted {
		t.Fatalf("resume of terminated must conflict")
	}
	if resp := admin.do("POST", base+"/suspend", nil); resp.status == http.StatusAccepted {
		t.Fatalf("suspend of terminated must conflict")
	}
	if resp := admin.do("DELETE", base, nil); resp.status == http.StatusNoContent {
		t.Fatalf("plain delete of terminated must conflict (use purge)")
	}

	// Purge is confirm-gated and only reachable from terminated.
	if resp := admin.do("POST", base+"/purge", nil); resp.status == http.StatusAccepted {
		t.Fatalf("purge without confirm must not enqueue")
	}
	resp = admin.do("POST", base+"/purge", map[string]any{"confirm": true})
	if resp.status != http.StatusAccepted {
		t.Fatalf("purge: %d %v", resp.status, resp.body)
	}
	_ = agent.do("POST", "/v1/agent/jobs/claim", nil)
	var purgeJobID string
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM jobs WHERE website_id = $1 AND type = 'delete_website'
		 ORDER BY created_at DESC LIMIT 1`, uuid.MustParse(websiteID)).Scan(&purgeJobID); err == nil {
		_ = agent.do("POST", "/v1/agent/jobs/"+purgeJobID+"/result", map[string]any{"success": true, "result": map[string]any{}})
	}
	if resp := admin.do("GET", base, nil); resp.status != http.StatusNotFound {
		t.Fatalf("purged website must be gone: %d %v", resp.status, resp.body)
	}
}

// TestBandwidthRecalcRouteAndRepair drives the recalculation/reconciliation
// surface end-to-end: admin POST .../bandwidth/recalculate enqueues a
// recalc_bandwidth job; the agent round-trip feeds the fanout, which repairs
// the monthly authority with GREATEST (report-only runs touch nothing; a
// lower recalculated value never lowers billed usage).
func TestBandwidthRecalcRouteAndRepair(t *testing.T) {
	orgID, _, websiteID, admin, agent, srv := phase4SetupWithServer(t)
	base := "/v1/organizations/" + orgID + "/websites/" + websiteID
	ctx := context.Background()

	// Range validation.
	if resp := admin.do("POST", base+"/bandwidth/recalculate", map[string]any{
		"from_day": "2026-09-10", "to_day": "2026-09-01",
	}); resp.status == http.StatusAccepted {
		t.Fatalf("inverted range must be rejected: %v", resp.body)
	}

	// Seed a current-month usage row (the GREATEST authority under repair).
	seed := func(usedMB float64) {
		if _, err := srv.Pool.Exec(ctx, `
			INSERT INTO workload_resource_usage
				(organization_id, website_id, resource, period_start, used, limit_value, unit, measured_by)
			VALUES ($1, $2, 'bandwidth', date_trunc('month', now()), $3, 10240, 'MB', 'test')
			ON CONFLICT (website_id, resource, period_start) DO UPDATE SET used = EXCLUDED.used,
				measured_by = EXCLUDED.measured_by`, orgID, websiteID, usedMB); err != nil {
			t.Fatalf("seed usage row: %v", err)
		}
	}
	usedMB := func() float64 {
		var used float64
		if err := srv.Pool.QueryRow(ctx, `SELECT used FROM workload_resource_usage
			WHERE website_id = $1 AND resource = 'bandwidth' AND period_start = date_trunc('month', now())`,
			websiteID).Scan(&used); err != nil {
			t.Fatalf("read usage row: %v", err)
		}
		return used
	}
	seed(100)

	runRecalc := func(apply bool, totalBytes int64) {
		t.Helper()
		today := time.Now().UTC().Format("2006-01-02")
		resp := admin.do("POST", base+"/bandwidth/recalculate", map[string]any{
			"from_day": today, "to_day": today, "apply": apply,
		})
		if resp.status != http.StatusAccepted {
			t.Fatalf("recalculate: %d %v", resp.status, resp.body)
		}
		jobID, _ := resp.body["job_id"].(string)
		claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
		job, _ := claim.body["job"].(map[string]any)
		if job == nil || job["type"] != "recalc_bandwidth" {
			t.Fatalf("claim: %v", claim.body)
		}
		res := agent.do("POST", "/v1/agent/jobs/"+jobID+"/result", map[string]any{
			"success": true,
			"result": map[string]any{
				"website_id": websiteID, "from_day": today, "to_day": today,
				"days": []map[string]any{{
					"date": today, "request_bytes": 1024,
					"response_bytes": totalBytes - 1024, "total_bytes": totalBytes,
				}},
				"total_bytes": totalBytes, "files_scanned": 1, "records_matched": 2,
			},
		})
		if res.status != http.StatusOK {
			t.Fatalf("recalc result: %d %v", res.status, res.body)
		}
	}

	// Reconciliation only (apply=false): the raw report lands in the job
	// result, the authoritative row stays untouched.
	runRecalc(false, 900*1024*1024)
	if got := usedMB(); got != 100 {
		t.Fatalf("report-only run must not touch usage, got %v", got)
	}

	// Repair (apply=true) with a HIGHER recalculated value: usage rises to it.
	runRecalc(true, 250*1024*1024)
	if got := usedMB(); got != 250 {
		t.Fatalf("repair must raise usage to the recalculated value, got %v", got)
	}

	// Repair with a LOWER recalculated value: GREATEST keeps the high-water.
	runRecalc(true, 50*1024*1024)
	if got := usedMB(); got != 250 {
		t.Fatalf("repair must never lower usage, got %v", got)
	}
}
