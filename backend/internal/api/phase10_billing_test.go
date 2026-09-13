package api

// Phase 10 API integration tests (DB: epicpanel_test_bl). Covers:
//   - full HTTP purchase flow: product -> order -> pay -> PROVISIONING
//     dispatch job -> job truth -> ACTIVE;
//   - webhook endpoint: signature enforcement (401 on tamper), replay
//     idempotency (replayed:true, no double dispatch);
//   - org scoping (cross-tenant 404) + role gates (billing read /
//     admin purchase);
//   - admin WHM surface: plans<->products, invoice issue/void, settings;
//   - invoice pay recovery endpoint;
//   - payment-method write-only (token stored, never returned);
//   - suspension dispatch via existing workload jobs (grace -> suspend).

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

	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/billing"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

type billingTestEnv struct {
	srv    *Server
	app    *testClient // full app (register/login/orgs)
	bl     *testClient // billing routes
	orgID  string
	prodID string
	botID  string
}

func newBillingTestEnv(t *testing.T) *billingTestEnv {
	t.Helper()
	if os.Getenv("EPICPANEL_TEST_DATABASE_URL") == "" {
		t.Setenv("EPICPANEL_TEST_DATABASE_URL",
			"postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_bl?sslmode=disable")
	}
	t.Setenv("EPICPANEL_DISABLE_BILLING_LOOPS", "1")
	t.Setenv("EPICPANEL_DISABLE_BOT_LOOPS", "1")
	t.Setenv("EPICPANEL_DISABLE_MC_LOOPS", "1")
	srv, client := newTestServer(t)

	// Deterministic tests drive convergence manually: a billing router over
	// the same stores (mirrors the phase-8 test harness).
	mux := http.NewServeMux()
	authH := &auth.Handler{Users: srv.Users, Sessions: srv.Sessions, Audit: srv.Audit, Cfg: srv.Cfg,
		SetupDone: srv.setupCompleted, MFA: srv.MFA}
	mux.HandleFunc("POST /v1/auth/register", authH.Register)
	orgH := &organizations.Handler{Store: srv.Orgs, Audit: srv.Audit}
	orgH.Register(mux)
	registerPhase10(srv, mux)
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

	// The harness drop-list does not know the newer tables: clear billing
	// + workload rows so tests start isolated (mirrors the phase-8 env).
	ctx := context.Background()
	for _, table := range []string{
		"schedule_tasks", "server_variables", "server_databases", "server_activities",
		"subusers", "mount_servers", "mount_eggs", "mounts",
		"egg_variables", "eggs", "nests",
		"billing_payments", "billing_orders", "bot_schedules", "bot_instances",
		"minecraft_world_backups", "minecraft_schedules", "minecraft_instances",
		"jobs",
	} {
		if _, err := srv.Pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if _, err := srv.Pool.Exec(ctx,
		`UPDATE subscriptions SET last_job_id = NULL, website_id = NULL, bot_id = NULL, instance_id = NULL`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"invoices", "subscriptions", "customers", "websites"} {
		if _, err := srv.Pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}

	blClient := &testClient{t: t, base: ts.URL, http: client.http}
	env := &billingTestEnv{srv: srv, app: client, bl: blClient}

	// Bootstrap: admin user + org + a Discord-plan product.
	client.do("POST", "/v1/auth/register", map[string]string{
		"email": "bladmin@example.test", "password": "supersecret123", "name": "BL Admin",
	})
	resp := client.do("POST", "/v1/organizations", map[string]string{"name": "BillCo"})
	env.orgID, _ = resp.body["id"].(string)
	if env.orgID == "" {
		t.Fatalf("create org: %v", resp.body)
	}

	// An online server so auto-placement can provision (billing uses the
	// same AutoPickServer path as the workload APIs). Direct SQL: the
	// scoped billing router does not mount the server-create route.
	var serverID uuid.UUID
	if err := srv.Pool.QueryRow(ctx, `
		INSERT INTO servers (organization_id, name, status, last_seen_at, maintenance_mode, registered_by)
		VALUES ($1, 'node-billing', 'online', now(), FALSE, (SELECT id FROM users WHERE email = 'bladmin@example.test'))
		RETURNING id`, blUUID(t, env.orgID)).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	// The bot provisioner auto-picks by runtime; enroll node 22.
	if _, err := srv.Pool.Exec(ctx, `
		INSERT INTO runtimes (server_id, type, version, status, created_by)
		VALUES ($1, 'node', '22', 'available', (SELECT id FROM users WHERE email = 'bladmin@example.test'))`,
		serverID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	// Link the product to a Discord plan: the purchase grants the plan to
	// the org at provisioning time (plans <-> products).
	var discordPlanID string
	if err := srv.Pool.QueryRow(context.Background(),
		`SELECT id FROM hosting_packages WHERE name = 'Discord Pro'`).Scan(&discordPlanID); err != nil {
		t.Fatalf("discord plan: %v", err)
	}
	created := blClient.do("POST", "/v1/admin/billing/products", map[string]any{
		"name": "Discord Starter", "type": "discord", "price_minor": 300,
		"description": "one bot", "plan_id": discordPlanID,
	})
	env.prodID, _ = created.body["id"].(string)
	if env.prodID == "" {
		t.Fatalf("seed product: %d %v", created.status, created.body)
	}
	return env
}

func (e *billingTestEnv) checkout(t *testing.T) (orderID, subID string) {
	t.Helper()
	resp := e.bl.do("POST", "/v1/organizations/"+e.orgID+"/billing/orders", map[string]any{
		"product_id": e.prodID, "billing_period": "monthly", "provider": "fake",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("checkout: %d %v", resp.status, resp.body)
	}
	order, _ := resp.body["order"].(map[string]any)
	sub, _ := resp.body["subscription"].(map[string]any)
	if order == nil || sub == nil {
		t.Fatalf("checkout body missing order/subscription: %v", resp.body)
	}
	oid, _ := order["id"].(string)
	sid, _ := sub["id"].(string)
	return oid, sid
}

// payThisOrder runs the capture for an existing order via the pay endpoint.
func (e *billingTestEnv) payThisOrder(t *testing.T, orderID, methodRef string) response {
	t.Helper()
	return e.bl.do("POST", "/v1/organizations/"+e.orgID+"/billing/orders/"+orderID+"/pay",
		map[string]any{"method_ref": methodRef})
}

// --- the full HTTP purchase flow ----------------------------------------------

func TestBillingPurchaseFlowToActive(t *testing.T) {
	env := newBillingTestEnv(t)
	orderID, subID := env.checkout(t)

	// PENDING before payment.
	resp := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if resp.body["provision_state"] != "PENDING" {
		t.Fatalf("initial state = %v, want PENDING", resp.body["provision_state"])
	}

	// Pay with the deterministic fake gateway.
	resp = env.payThisOrder(t, orderID, "tok_visa")
	if resp.status != http.StatusOK {
		t.Fatalf("pay: %d %v", resp.status, resp.body)
	}
	resp = env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if resp.body["provision_state"] != "PROVISIONING" {
		t.Fatalf("state after pay = %v, want PROVISIONING", resp.body["provision_state"])
	}

	// A state-changing job was dispatched with a billing idempotency key.
	var jobID, jobKey string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT j.id, COALESCE(j.idempotency_key,'') FROM jobs j
		 WHERE j.idempotency_key LIKE 'billingprovision-%' LIMIT 1`).Scan(&jobID, &jobKey); err != nil {
		t.Fatalf("billing provision job missing: %v", err)
	}

	// Re-paying the same order is a conflict (already paid), not a double
	// dispatch.
	resp = env.payThisOrder(t, orderID, "tok_visa")
	if resp.status != http.StatusConflict {
		t.Errorf("double pay status = %d, want 409", resp.status)
	}

	// Job truth arrives -> ACTIVE (server-side convergence, not client claim).
	env.svcOf().ApplyJobOutcome(context.Background(), billing.JobOutcome{
		SubscriptionID: blUUID(t, subID), Success: true,
	})
	resp = env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if resp.body["provision_state"] != "ACTIVE" {
		t.Fatalf("state after job truth = %v, want ACTIVE", resp.body["provision_state"])
	}
	if resp.body["renewal_status"] != "ok" {
		t.Errorf("renewal_status = %v, want ok", resp.body["renewal_status"])
	}

	// The invoice exists, is paid, and the number is minted.
	invoices := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/invoices", nil)
	list, _ := invoices.body["invoices"].([]any)
	if len(list) != 1 {
		t.Fatalf("invoices = %d, want 1", len(list))
	}
	inv, _ := list[0].(map[string]any)
	if inv["status"] != "paid" {
		t.Errorf("invoice status = %v, want paid", inv["status"])
	}
	if num, _ := inv["number"].(string); !strings.HasPrefix(num, "INV-") {
		t.Errorf("invoice number = %v", num)
	}

	// The order is paid.
	orders := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/orders", nil)
	oList, _ := orders.body["orders"].([]any)
	if len(oList) != 1 {
		t.Fatalf("orders = %d, want 1", len(oList))
	}
	o, _ := oList[0].(map[string]any)
	if o["status"] != "paid" {
		t.Errorf("order status = %v, want paid", o["status"])
	}
	if o["amount_minor"].(float64) != 300 {
		t.Errorf("order amount = %v, want 300", o["amount_minor"])
	}
}

// svcOf returns the env's billing service (the one registerPhase10 wired).
func (e *billingTestEnv) svcOf() *billing.Service {
	return e.srv.newBillingService()
}

func blUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return id
}

// --- webhook endpoint: tamper -> 401; valid -> provision; replay -> no-op -----

func TestBillingWebhookTamperReplay(t *testing.T) {
	env := newBillingTestEnv(t)
	// Configure the webhook secret via the admin settings API.
	env.bl.do("PATCH", "/v1/admin/billing/settings", map[string]any{"webhook_secret": "whsec-test-1"})

	orderID, subID := env.checkout(t)

	body, _ := json.Marshal(map[string]any{
		"event_id": "evt-901", "type": "payment.captured", "order_ref": orderID,
		"amount_minor": 300, "currency": "USD",
	})
	sig := billing.FakeProvider{}.SignPayload("whsec-test-1", body)

	// Tampered signature: rejected BEFORE any state change.
	resp := env.bl.do("POST", "/v1/billing/webhook/fake", map[string]any{"garbage": true})
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("garbage webhook status = %d, want 401", resp.status)
	}
	raw := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if raw.body["provision_state"] != "PENDING" {
		t.Fatalf("state after tamper = %v, want PENDING", raw.body["provision_state"])
	}

	// Valid signature over the exact body.
	req := newSignedRequest(t, "POST", env.bl.base+"/v1/billing/webhook/fake", body, sig)
	hresp, err := env.bl.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(hresp.Body).Decode(&decoded)
	if hresp.StatusCode != http.StatusOK || decoded["replayed"] != false {
		t.Fatalf("valid webhook: %d %v", hresp.StatusCode, decoded)
	}

	raw = env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if raw.body["provision_state"] != "PROVISIONING" {
		t.Fatalf("state after webhook = %v, want PROVISIONING", raw.body["provision_state"])
	}

	// Replay: OK + replayed:true, no second dispatch.
	req2 := newSignedRequest(t, "POST", env.bl.base+"/v1/billing/webhook/fake", body, sig)
	hresp2, err := env.bl.http.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer hresp2.Body.Close()
	var decoded2 map[string]any
	_ = json.NewDecoder(hresp2.Body).Decode(&decoded2)
	if decoded2["replayed"] != true {
		t.Errorf("replay not flagged: %v", decoded2)
	}
	var n int
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE idempotency_key LIKE 'billingprovision-%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("dispatch jobs = %d, want 1 (replay double-provisioned!)", n)
	}
	_ = orderID
}

// newSignedRequest posts a raw body with the fake-gateway signature header.
func newSignedRequest(t *testing.T, method, url string, body []byte, sig string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, newBodyReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EpicPanel", "1")
	req.Header.Set(billing.FakeSignatureHeader, sig)
	return req
}

// --- org scoping + role gates ---------------------------------------------------

func TestBillingOrgScopingAndRoles(t *testing.T) {
	env := newBillingTestEnv(t)
	orderID, subID := env.checkout(t)

	// Second user + org: must not see (or even discover) the first org's data.
	resp := env.app.do("POST", "/v1/organizations", map[string]string{"name": "OtherCo"})
	_ = resp.body["id"]
	c := env.app.NewClient()
	c.do("POST", "/v1/auth/register", map[string]string{
		"email": "other@example.test", "password": "supersecret123", "name": "Other",
	})
	other := &testClient{t: t, base: env.bl.base, http: c.http}

	if r := other.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions", nil); r.status != http.StatusNotFound {
		t.Errorf("cross-tenant list = %d, want 404", r.status)
	}
	if r := other.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil); r.status != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", r.status)
	}
	if r := other.do("POST", "/v1/organizations/"+env.orgID+"/billing/orders/"+orderID+"/pay",
		map[string]any{"method_ref": "tok_visa"}); r.status != http.StatusNotFound {
		t.Errorf("cross-tenant pay = %d, want 404", r.status)
	}

	// billing-role members can read but not purchase: promote the other
	// user into the first org as billing, then try.
	if err := env.srv.Orgs.SetMemberRole(context.Background(), blUUID(t, env.orgID), blUUID(t, mustUser(t, env, "other@example.test")), organizations.RoleBilling); err != nil {
		t.Fatal(err)
	}
	if r := other.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions", nil); r.status != http.StatusOK {
		t.Errorf("billing role read = %d, want 200", r.status)
	}
	if r := other.do("POST", "/v1/organizations/"+env.orgID+"/billing/orders", map[string]any{
		"product_id": env.prodID, "billing_period": "monthly",
	}); r.status != http.StatusForbidden {
		t.Errorf("billing role purchase = %d, want 403", r.status)
	}
}

func mustUser(t *testing.T, env *billingTestEnv, email string) string {
	t.Helper()
	var id string
	if err := env.srv.Pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("user lookup: %v", err)
	}
	return id
}

// --- admin surface: plans/products, invoices, settings ---------------------------

func TestBillingAdminSurface(t *testing.T) {
	env := newBillingTestEnv(t)

	// plans <-> products: link the product to a plan.
	plans := env.bl.do("GET", "/v1/admin/billing/plans", nil)
	planList, _ := plans.body["plans"].([]any)
	if len(planList) == 0 {
		t.Fatal("no plans returned")
	}
	first, _ := planList[0].(map[string]any)
	planID, _ := first["id"].(string)
	updated := env.bl.do("PATCH", "/v1/admin/billing/products/"+env.prodID, map[string]any{
		"plan_id": planID,
	})
	if updated.status != http.StatusOK {
		t.Fatalf("product update: %d %v", updated.status, updated.body)
	}
	if updated.body["plan_id"] == nil || updated.body["plan_id"] == "" {
		t.Errorf("product lost plan link: %v", updated.body)
	}

	// Manual invoice issue (issued = immutable) + void of a fresh one.
	issued := env.bl.do("POST", "/v1/admin/billing/invoices", map[string]any{
		"organization_id": env.orgID, "description": "overage", "amount_minor": 1500,
	})
	if issued.status != http.StatusCreated {
		t.Fatalf("issue: %d %v", issued.status, issued.body)
	}
	invID, _ := issued.body["id"].(string)
	if issued.body["status"] != "open" {
		t.Errorf("issued status = %v, want open", issued.body["status"])
	}
	voided := env.bl.do("POST", "/v1/admin/billing/invoices/"+invID+"/void", nil)
	if voided.status != http.StatusOK {
		t.Fatalf("void: %d %v", voided.status, voided.body)
	}
	// Double void = 409.
	if r := env.bl.do("POST", "/v1/admin/billing/invoices/"+invID+"/void", nil); r.status != http.StatusConflict {
		t.Errorf("double void = %d, want 409", r.status)
	}

	// Settings: grace period config round-trips.
	patched := env.bl.do("PATCH", "/v1/admin/billing/settings", map[string]any{
		"grace_days": 5, "suspend_terminate_days": 9, "invoice_due_days": 3,
		"webhook_secret": "whsec-admin-1",
	})
	if patched.status != http.StatusOK {
		t.Fatalf("settings patch: %d %v", patched.status, patched.body)
	}
	if patched.body["grace_days"].(float64) != 5 || patched.body["suspend_terminate_days"].(float64) != 9 {
		t.Errorf("settings not persisted: %v", patched.body)
	}
	if patched.body["webhook_secret_set"] != true {
		t.Error("webhook secret flag missing")
	}

	// The billing service picks the configured values up.
	svc := env.svcOf()
	if got := svc.SettingInt(context.Background(), "billing.grace_days", 3); got != 5 {
		t.Errorf("service grace days = %d, want 5", got)
	}

	// Admin-only: a non-admin session cannot touch the WHM surface.
	nonAdmin := env.app.NewClient()
	nonAdmin.do("POST", "/v1/auth/register", map[string]string{
		"email": "na@example.test", "password": "supersecret123", "name": "NA",
	})
	na := &testClient{t: t, base: env.bl.base, http: nonAdmin.http}
	if r := na.do("GET", "/v1/admin/billing/plans", nil); r.status != http.StatusForbidden {
		t.Errorf("non-admin plans = %d, want 403", r.status)
	}
	if r := na.do("POST", "/v1/admin/billing/subscriptions/"+uuid.NewString()+"/retry", nil); r.status != http.StatusForbidden {
		t.Errorf("non-admin retry = %d, want 403", r.status)
	}
}

// --- invoice pay recovery + payment method write-only ----------------------------

func TestBillingInvoicePayRecoveryAndPaymentMethod(t *testing.T) {
	env := newBillingTestEnv(t)
	orderID, _ := env.checkout(t)

	// Save a payment method; the token must NEVER come back.
	saved := env.bl.do("PUT", "/v1/organizations/"+env.orgID+"/billing/payment-method", map[string]any{
		"provider": "fake", "token": "tok_visa_saved_99", "brand": "visa", "last4": "4242",
	})
	if saved.status != http.StatusOK {
		t.Fatalf("save method: %d %v", saved.status, saved.body)
	}
	got := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/payment-method", nil)
	if bodyJSON, err := json.Marshal(got.body); err != nil || strings.Contains(string(bodyJSON), "tok_visa_saved_99") {
		t.Fatalf("payment token leaked: %s %v", string(bodyJSON), err)
	}
	if got.body["configured"] != true || got.body["last4"] != "4242" {
		t.Errorf("masked method wrong: %v", got.body)
	}

	// Pay the order with the saved method (no explicit method_ref).
	if r := env.payThisOrder(t, orderID, ""); r.status != http.StatusOK {
		t.Fatalf("pay with stored method: %d %v", r.status, r.body)
	}

	// Declined purchase -> open renewal invoice (grace walk) -> pay it.
	env2 := newBillingTestEnv(t)
	orderID2, sub2 := env2.checkout(t)
	// Pay it to ACTIVE, then force the period into the past so the sweep
	// renews it (only ACTIVE subscriptions renew).
	ctx := context.Background()
	if r := env2.payThisOrder(t, orderID2, "tok_visa"); r.status != http.StatusOK {
		t.Fatalf("pay: %d %v", r.status, r.body)
	}
	// Agent truth: the install job succeeded -> ACTIVE (only ACTIVE
	// subscriptions renew).
	env2.svcOf().ApplyJobOutcome(ctx, billing.JobOutcome{SubscriptionID: blUUID(t, sub2), Success: true})
	if _, err := env2.srv.Pool.Exec(ctx,
		`UPDATE subscriptions SET period_end = now() - interval '1 minute' WHERE id = $1`, sub2); err != nil {
		t.Fatal(err)
	}
	// Store a FAILING method so the renewal charge declines -> grace.
	env2.bl.do("PUT", "/v1/organizations/"+env2.orgID+"/billing/payment-method", map[string]any{
		"provider": "fake", "token": "fail-card",
	})
	svc := env2.svcOf()
	svc.RunRenewals(ctx)
	resp := env2.bl.do("GET", "/v1/organizations/"+env2.orgID+"/billing/subscriptions/"+sub2, nil)
	if resp.body["renewal_status"] != "in_grace" {
		t.Fatalf("renewal status after failed sweep-charge = %v (method was bad?)", resp.body["renewal_status"])
	}
	// Find the open renewal invoice and pay it via the API (recovery:
	// pay -> extend). PayInvoice falls back to the instrument on file —
	// swap it for a good one first.
	env2.bl.do("PUT", "/v1/organizations/"+env2.orgID+"/billing/payment-method", map[string]any{
		"provider": "fake", "token": "tok_visa",
	})
	_ = orderID2
	var invID string
	if err := env2.srv.Pool.QueryRow(ctx,
		`SELECT id FROM invoices WHERE kind = 'renewal' AND status = 'open' LIMIT 1`).Scan(&invID); err != nil {
		t.Fatalf("open renewal invoice: %v", err)
	}
	paid := env2.bl.do("POST", "/v1/organizations/"+env2.orgID+"/billing/invoices/"+invID+"/pay", map[string]any{})
	if paid.status != http.StatusOK {
		t.Fatalf("pay invoice: %d %v", paid.status, paid.body)
	}
	if paid.body["status"] != "paid" {
		t.Errorf("invoice after pay = %v", paid.body["status"])
	}
	resp = env2.bl.do("GET", "/v1/organizations/"+env2.orgID+"/billing/subscriptions/"+sub2, nil)
	if resp.body["provision_state"] != "ACTIVE" {
		t.Errorf("state after invoice pay = %v, want ACTIVE", resp.body["provision_state"])
	}
	var end time.Time
	if err := env2.srv.Pool.QueryRow(ctx,
		`SELECT period_end FROM subscriptions WHERE id = $1`, sub2).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if !end.After(time.Now()) {
		t.Errorf("period not extended after invoice pay: %v", end)
	}
}

// --- suspension rides the EXISTING workload suspend job ---------------------------

func TestBillingSuspendUsesWebsiteLifecycleJob(t *testing.T) {
	env := newBillingTestEnv(t)
	_, subID := env.checkout(t)

	// Attach a website to the subscription (web workload) + activate.
	ctx := context.Background()
	// A server must exist (websites.server_id is NOT NULL).
	var serverID uuid.UUID
	if err := env.srv.Pool.QueryRow(ctx, `SELECT id FROM servers LIMIT 1`).Scan(&serverID); err == nil {
		// already enrolled
	} else {
		if err := env.srv.Pool.QueryRow(ctx,
			`INSERT INTO servers (organization_id, name) VALUES ($1, 'node-bl') RETURNING id`,
			blUUID(t, env.orgID)).Scan(&serverID); err != nil {
			t.Fatalf("seed server: %v", err)
		}
	}
	var wsID uuid.UUID
	if err := env.srv.Pool.QueryRow(ctx, `
		INSERT INTO websites (organization_id, server_id, name, runtime, status, unix_user, created_by)
		VALUES ($1, $2, 'billingsite', 'static', 'ready', 'ep-bill', (SELECT id FROM users LIMIT 1))
		RETURNING id`, blUUID(t, env.orgID), serverID).Scan(&wsID); err != nil {
		t.Fatalf("seed website: %v", err)
	}
	wsIDRef := wsID
	if _, err := env.srv.Pool.Exec(ctx,
		`UPDATE subscriptions SET website_id = $2, workload_kind = 'web', provision_state = 'ACTIVE' WHERE id = $1`,
		subID, wsIDRef); err != nil {
		t.Fatal(err)
	}

	svc := env.svcOf()
	sub, err := svc.Store.GetSubscriptionAny(ctx, blUUID(t, subID))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginSuspend(ctx, sub, "billing test suspend"); err != nil {
		t.Fatalf("begin suspend: %v", err)
	}
	resp := env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if resp.body["provision_state"] != "SUSPENDING" {
		t.Fatalf("state = %v, want SUSPENDING", resp.body["provision_state"])
	}
	// The enqueued job IS the platform suspend_website job (no new channel).
	var jobType string
	if err := env.srv.Pool.QueryRow(ctx,
		`SELECT type::text FROM jobs WHERE website_id = $1 AND type = 'suspend_website' ORDER BY created_at DESC LIMIT 1`,
		wsID).Scan(&jobType); err != nil {
		t.Fatalf("suspend_website job missing: %v", err)
	}
	// The state machine guard: SUSPENDING -> SUSPENDED on job truth.
	svc.ApplyJobOutcome(ctx, billing.JobOutcome{SubscriptionID: blUUID(t, subID), Success: true})
	resp = env.bl.do("GET", "/v1/organizations/"+env.orgID+"/billing/subscriptions/"+subID, nil)
	if resp.body["provision_state"] != "SUSPENDED" {
		t.Errorf("state after suspend truth = %v, want SUSPENDED", resp.body["provision_state"])
	}
}
