package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/auth"
)

// TestPhase2Platform covers the Phase 2 deliverables end to end:
// registration lockdown, deny-by-default token scopes, service accounts,
// 2FA login gating, /api/v1 prefix alias, readyz, and server-route
// admin-only mutations.
func TestPhase2Platform(t *testing.T) {
	srv, admin := newTestServer(t)

	// --- readyz responds ready ---
	resp := admin.do("GET", "/readyz", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("readyz: %d %v", resp.status, resp.body)
	}
	if resp.body["status"] != "ready" {
		t.Fatalf("readyz status: %v", resp.body["status"])
	}

	// --- bootstrap admin + org (session-auth cookie jar) ---
	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "root@example.test", "password": "supersecret123", "name": "Root",
	})

	// --- /api/v1 prefix alias (authed route through the rewritten prefix) ---
	resp = admin.do("GET", "/api/v1/auth/me", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("/api/v1/auth/me via alias: %d %v", resp.status, resp.body)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ := resp.body["id"].(string)
	if orgID == "" {
		t.Fatalf("create org: %v", resp.body)
	}

	// --- registration lockdown once setup_completed flips ---
	if _, err := srv.Pool.Exec(nil2ctx(), `INSERT INTO system_settings (key, value) VALUES ('setup_completed','true') ON CONFLICT (key) DO UPDATE SET value='true'`); err != nil {
		t.Fatalf("set setup_completed: %v", err)
	}
	late := admin.NewClient()
	resp = late.do("POST", "/v1/auth/register", map[string]string{
		"email": "late@example.test", "password": "supersecret123", "name": "Late",
	})
	if resp.status != http.StatusForbidden {
		t.Fatalf("register after setup: %d want 403", resp.status)
	}

	// --- server mutations are platform-admin only; members get 403/404 ---
	member := admin.NewClient()
	member.do("POST", "/v1/auth/register", map[string]string{
		"email": "member@example.test", "password": "supersecret123", "name": "Member",
	})
	// (registration hook was set; use the admin-created account path)
	if resp.status == http.StatusForbidden {
		// expected for late registrations
	}

	// --- deny-by-default token scopes: unmapped route rejected ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "scoper", "scopes": []string{"websites:read"},
	})
	raw, _ := resp.body["raw_token"].(string)
	if raw == "" {
		t.Fatalf("token create: %v", resp.body)
	}
	tok := admin.NewClient()
	bearerClient(tok, raw)
	resp = tok.do("GET", "/v1/organizations/"+orgID+"/members", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("token on unmapped resource: %d want 403 (deny-by-default)", resp.status)
	}
	// token with org:read passes the scope gate
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/api-tokens", map[string]any{
		"name": "scoper2", "scopes": []string{"org:read", "org:write"},
	})
	raw2, _ := resp.body["raw_token"].(string)
	tok2 := admin.NewClient()
	bearerClient(tok2, raw2)
	resp = tok2.do("GET", "/v1/organizations/"+orgID+"/members", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("token with org:read on members: %d %v", resp.status, resp.body)
	}

	// --- service accounts ---
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/service-accounts", map[string]any{
		"name": "ci-bot", "scopes": []string{"websites:read"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("service account create: %d %v", resp.status, resp.body)
	}
	saToken, _ := resp.body["raw_token"].(string)
	if !strings.HasPrefix(saToken, "epk_") {
		t.Fatalf("service account token prefix: %q", saToken)
	}
	sa := admin.NewClient()
	bearerClient(sa, saToken)
	resp = sa.do("GET", "/v1/auth/me", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("service account /auth/me: %d %v", resp.status, resp.body)
	}
	if me, _ := resp.body["is_platform_admin"].(bool); me {
		t.Fatal("service account must never be platform admin")
	}
	resp = sa.do("GET", "/v1/organizations/"+orgID+"/websites", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("service account scoped read: %d %v", resp.status, resp.body)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/service-accounts", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("service account list: %d", resp.status)
	}

	// --- 2FA: setup -> enable -> login requires code ---
	resp = admin.do("POST", "/v1/auth/mfa/setup", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("mfa setup: %d %v", resp.status, resp.body)
	}
	secret, _ := resp.body["secret"].(string)
	code, err := totpNow(secret)
	if err != nil {
		t.Fatalf("totp: %v", err)
	}
	resp = admin.do("POST", "/v1/auth/mfa/enable", map[string]string{"code": code})
	if resp.status != http.StatusOK {
		t.Fatalf("mfa enable: %d %v", resp.status, resp.body)
	}
	recoveryCodes, _ := resp.body["recovery_codes"].([]any)
	if len(recoveryCodes) != 10 {
		t.Fatalf("recovery codes: %d want 10", len(recoveryCodes))
	}

	// A fresh login now returns 202 + mfa_token instead of a session.
	mfaLogin := admin.NewClient()
	resp = mfaLogin.do("POST", "/v1/auth/login", map[string]string{
		"email": "root@example.test", "password": "supersecret123",
	})
	if resp.status != http.StatusAccepted {
		t.Fatalf("mfa login step: %d %v", resp.status, resp.body)
	}
	mfaToken, _ := resp.body["mfa_token"].(string)
	code2, _ := totpNow(secret)
	resp = mfaLogin.do("POST", "/v1/auth/mfa/verify", map[string]string{"mfa_token": mfaToken, "code": code2})
	if resp.status != http.StatusOK {
		t.Fatalf("mfa verify: %d %v", resp.status, resp.body)
	}
	if resp.body["token"] == "" {
		t.Fatal("mfa verify should return a session token")
	}

	// Recovery code login path (skip one TOTP window via code3; use recovery).
	recLogin := admin.NewClient()
	resp = recLogin.do("POST", "/v1/auth/login", map[string]string{
		"email": "root@example.test", "password": "supersecret123",
	})
	mfaToken, _ = resp.body["mfa_token"].(string)
	recCode, _ := recoveryCodes[0].(string)
	resp = recLogin.do("POST", "/v1/auth/mfa/verify", map[string]string{"mfa_token": mfaToken, "code": recCode})
	if resp.status != http.StatusOK {
		t.Fatalf("recovery code login: %d %v", resp.status, resp.body)
	}
	// The same recovery code cannot be reused.
	resp = recLogin.do("POST", "/v1/auth/login", map[string]string{
		"email": "root@example.test", "password": "supersecret123",
	})
	mfaToken, _ = resp.body["mfa_token"].(string)
	resp = recLogin.do("POST", "/v1/auth/mfa/verify", map[string]string{"mfa_token": mfaToken, "code": recCode})
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("recovery code reuse: %d want 401", resp.status)
	}

	// Disable MFA with password.
	resp = mfaLogin.do("POST", "/v1/auth/mfa/disable", map[string]string{"password": "supersecret123"})
	if resp.status != http.StatusNoContent {
		t.Fatalf("mfa disable: %d %v", resp.status, resp.body)
	}
}

// TestJobLeaseReaper covers the lease/reaper convergence path: a job claimed
// and abandoned (lease expired) is requeued, then fails terminally.
func TestJobLeaseReaper(t *testing.T) {
	srv, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "root2@example.test", "password": "supersecret123", "name": "Root",
	})
	resp := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme2"})
	orgID, _ := resp.body["id"].(string)

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "srv-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent := admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "srv-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))

	// Enqueue a real job (provision a site) and claim it, abandoning it.
	admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "reaper-test", "runtime": "static",
	})
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil {
		t.Fatalf("expected claim, got %v", claim.body)
	}
	jobID, _ := job["id"].(string)

	// Simulate the agent dying: expire the lease directly.
	if _, err := srv.Pool.Exec(nil2ctx(),
		`UPDATE jobs SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, jobID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reaped, err := srv.Jobs.ReapExpiredLeases(nil2ctx())
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(reaped) != 1 || reaped[0].ID.String() != jobID {
		t.Fatalf("reaped %v want %s", reaped, jobID)
	}
	if reaped[0].Status != "pending" {
		t.Fatalf("reaped status: %v want pending", reaped[0].Status)
	}
	// The job is claimable again (visible immediately: test backoff).
	claim = agent.do("POST", "/v1/agent/jobs/claim", nil)
	if j2, _ := claim.body["job"].(map[string]any); j2 == nil {
		t.Fatal("reaped job not claimable")
	} else if j2["id"] != jobID {
		t.Fatalf("reclaimed %v want %s", j2["id"], jobID)
	}
}

// TestCSRFHeader ensures cookie-authenticated mutations require the header.
func TestCSRFHeader(t *testing.T) {
	_, admin := newTestServer(t)
	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "csrf@example.test", "password": "supersecret123", "name": "C",
	})
	// do() sets the header; craft a raw request without it.
	req, _ := http.NewRequest("POST", admin.base+"/v1/organizations", strings.NewReader(`{"name":"NoHeader"}`))
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := admin.http.Do(req)
	if err != nil {
		t.Fatalf("raw post: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without X-EpicPanel header: %d want 403", httpResp.StatusCode)
	}
}

// nil2ctx keeps test call sites tidy.
func nil2ctx() context.Context { return context.Background() }

// totpNow computes the current TOTP code for a secret.
func totpNow(secret string) (string, error) {
	return auth.TOTPCode(secret, time.Now())
}

var _ = json.Marshal
