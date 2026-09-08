package billing

// DB-backed integration tests for the billing flows (Phase 10 brief):
//   - purchase -> PROVISIONING (dispatch) with audit trail;
//   - webhook replay idempotency (no double provision/invoice/payment);
//   - invoice immutability once issued (store guard + DB trigger);
//   - the full failure walk PENDING/ACTIVE -> grace -> SUSPENDED ->
//     TERMINATED with injected short intervals;
//   - illegal transitions audited (result=failure).
//
// Run with: EPICPANEL_TEST_DATABASE_URL=postgres://epicpanel:epicpanel_dev@
// localhost:5432/epicpanel_test_bl?sslmode=disable go test ./internal/billing/

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/db"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

const testDBURLDefault = "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_bl?sslmode=disable"

// dropList mirrors the api test harness: everything, then re-migrate. The
// billing-owned tables are included so the migration chain is proven
// re-runnable every run.
var dropList = []string{
	"hosting_packages", "billing_payments", "billing_orders", "workload_resource_usage",
	"events", "resources", "invoices", "subscriptions", "services", "products", "customers",
	"recovery_codes", "mfa_challenges", "service_accounts", "api_tokens",
	"applications", "ssh_keys", "system_settings", "cron_jobs", "http_checks", "alerts", "backups",
	"deployments", "domains", "dns_records", "dns_zones", "domain_redirects", "ftp_accounts", "databases",
	"minecraft_world_backups", "minecraft_schedules", "minecraft_instances",
	"bot_schedules", "bot_instances", "runtimes", "jobs", "websites",
	"server_metrics", "server_agent_tokens", "server_registration_tokens", "servers",
	"audit_logs", "sessions", "organization_members", "organizations", "users", "schema_migrations",
}

var dropTypes = []string{"org_role", "job_type", "job_status", "db_engine", "db_status", "domain_kind", "ssl_mode", "ssl_state"}

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("EPICPANEL_TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = testDBURLDefault
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping integration test: no test database (%v)", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range dropList {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
			t.Fatalf("drop table %s: %v", table, err)
		}
	}
	for _, typ := range dropTypes {
		if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS "+typ); err != nil {
			t.Fatalf("drop type %s: %v", typ, err)
		}
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Store{Pool: pool}, pool
}

// fakeProvisioner is a controllable dispatch seam: it records the dispatch
// and enqueues a fake job id that the test marks success/failed.
type fakeProvisioner struct {
	store    *Store
	dispatch []string
	jobID    *uuid.UUID
	failNext bool
}

func (f *fakeProvisioner) provision(ctx context.Context, sub *Subscription, product *Product) error {
	f.dispatch = append(f.dispatch, sub.ID.String()+":"+product.Name)
	if f.failNext {
		// No job recorded: dispatch refused -> FAILED with artifacts.
		return errDispatchRefused
	}
	if f.jobID != nil {
		id := *f.jobID
		return f.store.SetSubscriptionWorkload(ctx, sub.ID, sub.WorkloadKind, nil, nil, nil, &id, sub.ProvisionAttempts+1)
	}
	return nil
}

var errDispatchRefused = &fixedError{"dispatch refused by test"}

type fixedError struct{ s string }

func (e *fixedError) Error() string { return e.s }

// serviceHarness wires a Service over the test DB with controllable engines.
type serviceHarness struct {
	Store      *Store
	Svc        *Service
	Provision  *fakeProvisioner
	Suspended  []uuid.UUID
	Resumed    []uuid.UUID
	Terminated []uuid.UUID
	SuspendOK  bool
	TerminateOK bool
}

func newHarness(t *testing.T, now func() time.Time) *serviceHarness {
	st, pool := testStore(t)
	h := &serviceHarness{Store: st, Provision: &fakeProvisioner{store: st}}
	h.Svc = &Service{
		Store:  st,
		Jobs:   &jobs.Store{Pool: pool},
		Events: &events.Bus{Pool: pool},
		Audit:  &audit.Store{Pool: pool},
		SettingsFn: func(ctx context.Context, key string) (string, error) {
			var v string
			err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v)
			return v, err
		},
		Provision: h.Provision.provision,
		Suspend: func(ctx context.Context, sub *Subscription) error {
			h.Suspended = append(h.Suspended, sub.ID)
			return nil
		},
		Resume: func(ctx context.Context, sub *Subscription) error {
			h.Resumed = append(h.Resumed, sub.ID)
			return nil
		},
		Terminate: func(ctx context.Context, sub *Subscription) error {
			h.Terminated = append(h.Terminated, sub.ID)
			return nil
		},
	}
	if now != nil {
		h.Svc.Now = now
	}
	return h
}

// seedProduct inserts a purchasable product.
func (h *serviceHarness) seedProduct(t *testing.T, name, typ string, priceMinor int64, planID *uuid.UUID) *Product {
	t.Helper()
	// hosting_packages row for the plan link (plans<->products).
	var pid *uuid.UUID
	if planID == nil {
		var id uuid.UUID
		if err := h.Store.Pool.QueryRow(context.Background(), `
			INSERT INTO hosting_packages (name, kind, max_websites, price_monthly_cents)
			VALUES ($1, 'web', 5, 500) ON CONFLICT (name) DO UPDATE SET price_monthly_cents = 500
			RETURNING id`, "plan-"+name).Scan(&id); err == nil {
			pid = &id
		}
	} else {
		pid = planID
	}
	p, err := h.Store.CreateProduct(context.Background(), ProductInput{
		Name: name, Type: typ, Description: "test product " + name,
		PlanID: pid, PriceMinor: priceMinor, Currency: "USD", Active: true,
	})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return p
}

// seedOrg inserts an organization + owner user.
func (h *serviceHarness) seedOrg(t *testing.T, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var uid uuid.UUID
	if err := h.Store.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name)
		VALUES ($1, 'x', $2) RETURNING id
	`, name+"@example.test", name).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	orgs := &organizations.Store{Pool: h.Store.Pool}
	org, err := orgs.Create(ctx, name, strings.ToLower(name)+"-"+uuid.NewString()[:8], uid)
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return org.ID
}

func (h *serviceHarness) purchase(t *testing.T, orgID uuid.UUID, product *Product, period, methodRef, eventID string) (*Order, *Subscription, bool, error) {
	t.Helper()
	svc := h.Svc
	svc.Provision = h.Provision.provision
	return svc.PayAndActivate(context.Background(), CheckoutInput{
		OrgID: orgID, ProductID: product.ID, Period: period, Provider: "fake",
	}, methodRef, eventID)
}

func (h *serviceHarness) subscriptionState(t *testing.T, subID uuid.UUID) *Subscription {
	t.Helper()
	sub, err := h.Store.GetSubscriptionAny(context.Background(), subID)
	if err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	return sub
}

func (h *serviceHarness) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

// --- purchase -> PROVISIONING (+ ACTIVE via job outcome) ----------------------

func TestPurchaseProvisionsAndActivates(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "purchaseco")
	product := h.seedProduct(t, "Web Starter", "hosting", 500, nil)

	order, sub, replayed, err := h.purchase(t, org, product, "monthly", "tok_visa", "evt-purchase-1")
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if replayed {
		t.Fatal("first purchase reported replayed")
	}
	if order.Status != OrderPaid {
		t.Errorf("order status = %s, want paid", order.Status)
	}
	if sub.ProvisionState != StateProvisioning {
		t.Fatalf("subscription state = %s, want PROVISIONING", sub.ProvisionState)
	}
	if len(h.Provision.dispatch) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(h.Provision.dispatch))
	}
	if sub.WorkloadKind != string(KindWeb) {
		t.Errorf("workload kind = %s, want web", sub.WorkloadKind)
	}

	// Invoice issued + paid, and it is now immutable.
	invoices, err := h.Store.ListInvoicesForOrg(context.Background(), org, 10)
	if err != nil || len(invoices) != 1 {
		t.Fatalf("invoices = %v (err %v), want 1", invoices, err)
	}
	inv := invoices[0]
	if inv.Status != InvoicePaid || inv.IssuedAt == nil || inv.TotalMinor != 500 {
		t.Errorf("invoice wrong: status=%s total=%d err=%v", inv.Status, inv.TotalMinor, err)
	}
	if !strings.HasPrefix(inv.Number, "INV-") {
		t.Errorf("invoice number = %s, want INV-…", inv.Number)
	}

	// Money = integer minor units + currency on every store.
	if order.AmountMinor != 500 || order.Currency != "USD" {
		t.Errorf("order money = %d %s, want 500 USD", order.AmountMinor, order.Currency)
	}

	// The dispatch is audited + the state transitions are on the bus.
	if n := h.auditCount(t, "billing.subscription_provisioning"); n != 1 {
		t.Errorf("provisioning audit rows = %d, want 1", n)
	}
	var evN int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events WHERE type = 'billing.subscription.provisioning'`).Scan(&evN); err != nil {
		t.Fatal(err)
	}
	if evN != 1 {
		t.Errorf("provisioning events = %d, want 1", evN)
	}

	// Job outcome (agent truth) -> ACTIVE.
	h.Svc.ApplyJobOutcome(context.Background(), JobOutcome{
		SubscriptionID: sub.ID, JobType: jobs.TypeProvisionWebsite, Success: true,
	})
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateActive {
		t.Errorf("state after job success = %s, want ACTIVE", sub.ProvisionState)
	}
	if sub.Status != "active" {
		t.Errorf("legacy status = %s, want active", sub.Status)
	}
}

// --- replay idempotency -------------------------------------------------------

func TestWebhookReplayIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "replayco")
	product := h.seedProduct(t, "Bot Basic", "discord", 300, nil)

	// First webhook: full flow runs.
	_, _, replayed, err := h.purchase(t, org, product, "monthly", "tok_ok", "evt-replay-1")
	if err != nil || replayed {
		t.Fatalf("first purchase: replayed=%v err=%v", replayed, err)
	}

	// Second purchase with the SAME provider event id: everything collapses.
	_, _, replayed, err = h.purchase(t, org, product, "monthly", "tok_ok", "evt-replay-1")
	if err != nil {
		t.Fatalf("replay errored: %v", err)
	}
	if !replayed {
		t.Fatal("duplicate event id was not detected as replay")
	}

	// No double provision, no double invoice, no double payment.
	if got := len(h.Provision.dispatch); got != 1 {
		t.Errorf("provision dispatches = %d, want 1 (double provision!)", got)
	}
	var n int
	ctx := context.Background()
	if err := h.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("invoices = %d, want 1 (double invoice!)", n)
	}
	if err := h.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_payments WHERE provider_event_id = 'evt-replay-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("payments for event = %d, want 1 (double capture!)", n)
	}
}

// --- declined payment ---------------------------------------------------------

func TestDeclinedPurchaseStaysPendingForRetry(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "declineco")
	product := h.seedProduct(t, "Web Pro", "hosting", 2500, nil)

	_, sub, _, err := h.purchase(t, org, product, "monthly", "fail-card", "evt-decline-1")
	if err == nil {
		t.Fatal("declined purchase did not error")
	}
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StatePending {
		t.Errorf("state after decline = %s, want PENDING (retryable)", sub.ProvisionState)
	}
	if got := len(h.Provision.dispatch); got != 0 {
		t.Errorf("declined purchase dispatched provisioning %d times", got)
	}
	// No invoice may exist for a declined purchase.
	var n int
	if err := h.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM invoices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("invoices after decline = %d, want 0", n)
	}
}

// --- dispatch refusal -> FAILED with retained artifacts ------------------------

func TestDispatchRefusalFailsWithArtifacts(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "refuseco")
	product := h.seedProduct(t, "MC 4GB", "minecraft", 2000, nil)

	h.Provision.failNext = true
	_, sub, _, err := h.purchase(t, org, product, "monthly", "tok_ok", "evt-refuse-1")
	if err == nil {
		t.Fatal("dispatch refusal did not error")
	}
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateFailed {
		t.Fatalf("state = %s, want FAILED", sub.ProvisionState)
	}
	if sub.LastError == "" {
		t.Error("FAILED subscription lost last_error (artifacts not retained)")
	}
	// FAILED -> PROVISIONING (manual retry) is legal and re-dispatches.
	h.Provision.failNext = false
	if err := h.Svc.ManualRetry(context.Background(), sub, nil); err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateProvisioning {
		t.Errorf("state after retry = %s, want PROVISIONING", sub.ProvisionState)
	}
	if got := len(h.Provision.dispatch); got != 2 {
		t.Errorf("dispatch after retry = %d, want 2 (refused attempt + manual retry)", got)
	}
}

// --- invoice immutability (store guard + DB trigger) ---------------------------

func TestInvoiceImmutableOnceIssued(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "immutableco")
	cust, err := h.Store.EnsureCustomer(context.Background(), org, "USD")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := h.Store.IssueInvoice(context.Background(), InvoiceInput{
		CustomerID: cust.ID, OrgID: org, Kind: InvoiceKindManual,
		Currency: "USD", SubtotalMinor: 900,
		LineItems: []LineItem{{Description: "overage", Quantity: 1, UnitMinor: 900, TotalMinor: 900}},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// The DB trigger must reject money-field updates on issued invoices.
	ctx := context.Background()
	if _, err := h.Store.Pool.Exec(ctx,
		`UPDATE invoices SET total_cents = 1 WHERE id = $1`, inv.ID); err == nil {
		t.Fatal("DB accepted a total change on an issued invoice (immutability broken)")
	} else if !strings.Contains(err.Error(), "immutable") {
		t.Errorf("unexpected trigger error: %v", err)
	}
	if _, err := h.Store.Pool.Exec(ctx,
		`UPDATE invoices SET line_items = '[{"description":"tampered","quantity":1,"unit_minor":1,"total_minor":1}]'::jsonb WHERE id = $1`, inv.ID); err == nil {
		t.Fatal("DB accepted a line_items change on an issued invoice")
	}

	// Status transitions stay legal and never touch money.
	if err := h.Store.MarkInvoicePaid(ctx, inv.ID); err != nil {
		t.Fatalf("pay: %v", err)
	}
	// Paid invoices cannot be voided (refund path instead).
	if err := h.Store.VoidInvoice(ctx, inv.ID); err != ErrInvoiceState {
		t.Errorf("voiding a paid invoice = %v, want ErrInvoiceState", err)
	}

	// A draft/unpaid invoice can be voided exactly once.
	inv2, err := h.Store.IssueInvoice(ctx, InvoiceInput{
		CustomerID: cust.ID, OrgID: org, Kind: InvoiceKindManual,
		Currency: "USD", SubtotalMinor: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.VoidInvoice(ctx, inv2.ID); err != nil {
		t.Fatalf("void open invoice: %v", err)
	}
	if err := h.Store.VoidInvoice(ctx, inv2.ID); err != ErrInvoiceState {
		t.Errorf("double void = %v, want ErrInvoiceState", err)
	}
}

// --- illegal transitions are audited ------------------------------------------

func TestIllegalTransitionRejectedAndAudited(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "illegalco")
	product := h.seedProduct(t, "Web One", "hosting", 100, nil)

	_, sub, _, err := h.purchase(t, org, product, "monthly", "tok_ok", "evt-illegal-1")
	if err != nil {
		t.Fatal(err)
	}
	// PENDING -> ACTIVE would skip PROVISIONING: illegal (the sub is now
	// PROVISIONING; jump to SUSPENDED instead — also illegal).
	if _, err := h.Svc.Transition(context.Background(), sub, StateSuspended, nil, "attempted skip"); err == nil {
		t.Fatal("illegal transition accepted")
	}
	var successes int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = 'billing.subscription_suspended' AND result = 'success'`).Scan(&successes); err != nil {
		t.Fatal(err)
	}
	if successes != 0 {
		t.Errorf("illegal transition wrote a SUCCESS audit row (%d)", successes)
	}
	// The failure IS audited (result=failure).
	var failures int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = 'billing.subscription_suspended' AND result = 'failure'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Errorf("failure audit rows = %d, want 1", failures)
	}
	// And the state did not move.
	if got := h.subscriptionState(t, sub.ID).ProvisionState; got != StateProvisioning {
		t.Errorf("state after illegal attempt = %s, want PROVISIONING", got)
	}
	// Idempotent replay of the CURRENT state is not an error.
	res, err := h.Svc.Transition(context.Background(), sub, StateProvisioning, nil, "replay")
	if err != nil {
		t.Fatalf("idempotent replay errored: %v", err)
	}
	if res.Changed {
		t.Error("same-state replay reported Changed")
	}
}

// --- the grace walk (injected short intervals) ---------------------------------
// Payment failed -> Grace period -> Suspend -> Terminate, verbatim from the
// master doc, with the scheduler intervals injected (seconds, not days).

func TestGraceWalkSuspendTerminate(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, func() time.Time { return now })
	org := h.seedOrg(t, "graceco")
	product := h.seedProduct(t, "Bot Pro", "discord", 700, nil)

	// Grace = 3 days default; inject 1 day + terminate window 1 day.
	ctx := context.Background()
	for k, v := range map[string]string{
		"billing.grace_days":            "1",
		"billing.suspend_terminate_days": "1",
	} {
		if _, err := h.Store.Pool.Exec(ctx, `
			INSERT INTO system_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = $2`, k, v); err != nil {
			t.Fatal(err)
		}
	}

	_, sub, _, err := h.purchase(t, org, product, "monthly", "tok_ok", "evt-grace-1")
	if err != nil {
		t.Fatal(err)
	}
	// Agent truth: provision job succeeded -> ACTIVE.
	h.Svc.ApplyJobOutcome(ctx, JobOutcome{SubscriptionID: sub.ID, Success: true})
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateActive {
		t.Fatalf("pre-walk state = %s, want ACTIVE", sub.ProvisionState)
	}

	// Move past period_end with NO payment method on file -> renewal fails
	// -> grace.
	now = now.AddDate(0, 1, 0)
	h.Svc.RunRenewals(ctx)
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateActive {
		t.Fatalf("state during grace = %s, want ACTIVE (workload keeps running)", sub.ProvisionState)
	}
	if sub.GraceUntil == nil {
		t.Fatal("grace not started (grace_until nil)")
	}
	if !sub.GraceUntil.After(now) {
		t.Errorf("grace_until %v not after now %v", sub.GraceUntil, now)
	}
	// A renewal invoice exists and is OPEN (unpaid).
	var openInvoices int
	if err := h.Store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE kind = 'renewal' AND status = 'open'`).Scan(&openInvoices); err != nil {
		t.Fatal(err)
	}
	if openInvoices != 1 {
		t.Errorf("open renewal invoices = %d, want 1", openInvoices)
	}

	// Grace elapsed -> SUSPENDING (the suspend engine was invoked).
	now = sub.GraceUntil.Add(time.Minute)
	h.Svc.RunRenewals(ctx)
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateSuspending {
		t.Fatalf("state after grace = %s, want SUSPENDING", sub.ProvisionState)
	}
	if got := len(h.Suspended); got != 1 {
		t.Fatalf("suspend engine calls = %d, want 1", got)
	}
	// Suspend job truth arrives -> SUSPENDED.
	h.Svc.ApplyJobOutcome(ctx, JobOutcome{SubscriptionID: sub.ID, Success: true})
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateSuspended {
		t.Fatalf("state after suspend job = %s, want SUSPENDED", sub.ProvisionState)
	}

	// Payment lands during suspension -> reversible resume (SUSPENDED ->
	// ACTIVE + resume engine).
	if err := h.Store.SetPaymentMethod(ctx, org, PaymentMethod{Provider: "fake", TokenEnc: base64Of("tok_ok"), Brand: "visa", Last4: "4242"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Svc.ResumeBilling(ctx, sub, "reactivated after payment"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateActive {
		t.Fatalf("state after resume = %s, want ACTIVE", sub.ProvisionState)
	}
	if got := len(h.Resumed); got != 1 {
		t.Errorf("resume engine calls = %d, want 1", got)
	}

	// Recovery path: the customer pays the OPEN renewal invoice (pay ->
	// extend -> resume). The stored method is good now.
	h.Svc.Store.SetPaymentMethod(ctx, org, PaymentMethod{Provider: "fake", TokenEnc: base64Of("tok_ok"), Brand: "visa", Last4: "4242"})
	var openInvID uuid.UUID
	if err := h.Store.Pool.QueryRow(ctx,
		`SELECT id FROM invoices WHERE kind = 'renewal' AND status = 'open' ORDER BY created_at DESC LIMIT 1`).Scan(&openInvID); err != nil {
		t.Fatalf("open renewal invoice: %v", err)
	}
	paidInv, err := h.Svc.PayInvoice(ctx, org, openInvID, "", "", nil)
	if err != nil {
		t.Fatalf("pay renewal invoice: %v", err)
	}
	if paidInv.Status != InvoicePaid {
		t.Errorf("paid invoice status = %s, want paid", paidInv.Status)
	}
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateActive {
		t.Fatalf("state after paying renewal = %s, want ACTIVE", sub.ProvisionState)
	}
	if !sub.PeriodEnd.After(now) {
		t.Errorf("period not extended: end=%v now=%v", sub.PeriodEnd, now)
	}

	// Now the terminate path: bad method again -> grace -> suspend ->
	// past the terminate window -> TERMINATING (backup-first) -> TERMINATED.
	if err := h.Store.SetPaymentMethod(ctx, org, PaymentMethod{Provider: "fake", TokenEnc: base64Of("fail-card")}); err != nil {
		t.Fatal(err)
	}
	now = sub.PeriodEnd.Add(time.Minute)
	h.Svc.RunRenewals(ctx) // renewal fails -> grace
	sub = h.subscriptionState(t, sub.ID)
	if sub.GraceUntil == nil {
		t.Fatal("second grace not started")
	}
	now = sub.GraceUntil.Add(time.Minute)
	h.Svc.RunRenewals(ctx) // grace expired -> SUSPENDING
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateSuspending {
		t.Fatalf("second suspend state = %s, want SUSPENDING", sub.ProvisionState)
	}
	h.Svc.ApplyJobOutcome(ctx, JobOutcome{SubscriptionID: sub.ID, Success: true}) // stop job truth
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateSuspended {
		t.Fatalf("second suspended = %s", sub.ProvisionState)
	}
	now = sub.StateChangedAt.Add(25 * time.Hour) // terminate window = 1 day
	h.Svc.RunRenewals(ctx)                        // -> TERMINATING
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateTerminating {
		t.Fatalf("state after terminate window = %s, want TERMINATING", sub.ProvisionState)
	}
	if got := len(h.Terminated); got != 1 {
		t.Fatalf("terminate engine calls = %d, want 1 (backup-first enqueue)", got)
	}
	h.Svc.ApplyJobOutcome(ctx, JobOutcome{SubscriptionID: sub.ID, Success: true}) // delete job truth
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateTerminated {
		t.Fatalf("final state = %s, want TERMINATED", sub.ProvisionState)
	}
	// TERMINATED is terminal: any further move is illegal.
	if _, err := h.Svc.Transition(ctx, sub, StateActive, nil, "resurrection attempt"); err == nil {
		t.Fatal("TERMINATED was resurrected")
	}
	// The whole walk left an audit trail.
	for _, action := range []string{
		"billing.grace_started", "billing.subscription_suspending",
		"billing.subscription_suspended", "billing.subscription_terminating",
		"billing.subscription_terminated",
	} {
		if n := h.auditCount(t, action); n < 1 {
			t.Errorf("audit trail missing %s", action)
		}
	}
}

// base64Of seals s into the TokenEnc storage shape (base64 secretbox
// ciphertext) so renewal charges can decrypt the instrument.
func base64Of(s string) string {
	enc, err := secretbox.Encrypt(s)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(enc)
}

// --- renewal extension on success ----------------------------------------------

func TestRenewalExtendsPeriod(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, func() time.Time { return now })
	org := h.seedOrg(t, "renewco")
	product := h.seedProduct(t, "Web Renew", "hosting", 500, nil)

	_, sub, _, err := h.purchase(t, org, product, "quarterly", "tok_ok", "evt-renew-1")
	if err != nil {
		t.Fatal(err)
	}
	h.Svc.ApplyJobOutcome(context.Background(), JobOutcome{SubscriptionID: sub.ID, Success: true})
	sub = h.subscriptionState(t, sub.ID)
	origEnd := sub.PeriodEnd

	// Store the payment method so renewal charges succeed.
	if err := h.Store.SetPaymentMethod(context.Background(), org,
		PaymentMethod{Provider: "fake", TokenEnc: base64Of("tok_ok")}); err != nil {
		t.Fatal(err)
	}
	now = origEnd.Add(time.Minute)
	h.Svc.RunRenewals(context.Background())
	sub = h.subscriptionState(t, sub.ID)
	if !sub.PeriodEnd.After(origEnd.AddDate(0, 0, 80)) {
		t.Errorf("quarterly renewal did not extend by 3 months: end=%v orig=%v", sub.PeriodEnd, origEnd)
	}
	if sub.ProvisionState != StateActive {
		t.Errorf("state after renewal = %s, want ACTIVE", sub.ProvisionState)
	}
	// Renewal invoice is paid.
	var paid int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE kind = 'renewal' AND status = 'paid'`).Scan(&paid); err != nil {
		t.Fatal(err)
	}
	if paid != 1 {
		t.Errorf("paid renewal invoices = %d, want 1", paid)
	}
	// Double sweep is a no-op (already renewed for this period).
	h.Svc.RunRenewals(context.Background())
	var totalRenewals int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE kind = 'renewal'`).Scan(&totalRenewals); err != nil {
		t.Fatal(err)
	}
	if totalRenewals != 1 {
		t.Errorf("renewal invoices after double sweep = %d, want 1 (not idempotent)", totalRenewals)
	}
}

// --- cancelled-at-period-end walks straight to terminate ------------------------

func TestCancelAtPeriodEndTerminates(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, func() time.Time { return now })
	org := h.seedOrg(t, "cancelco")
	product := h.seedProduct(t, "Bot Cancel", "discord", 300, nil)

	_, sub, _, err := h.purchase(t, org, product, "monthly", "tok_ok", "evt-cancel-1")
	if err != nil {
		t.Fatal(err)
	}
	h.Svc.ApplyJobOutcome(context.Background(), JobOutcome{SubscriptionID: sub.ID, Success: true})
	if err := h.Store.SetSubscriptionCancelFlag(context.Background(), org, sub.ID, true); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 1, 0)
	h.Svc.RunRenewals(context.Background())
	sub = h.subscriptionState(t, sub.ID)
	if sub.ProvisionState != StateTerminating {
		t.Fatalf("state after cancelled period = %s, want TERMINATING", sub.ProvisionState)
	}
	h.Svc.ApplyJobOutcome(context.Background(), JobOutcome{SubscriptionID: sub.ID, Success: true})
	if got := h.subscriptionState(t, sub.ID).ProvisionState; got != StateTerminated {
		t.Errorf("final state = %s, want TERMINATED", got)
	}
	// No renewal invoice was created for the cancelled subscription.
	var n int
	if err := h.Store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE kind = 'renewal'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("cancelled subscription invoiced for renewal (%d rows)", n)
	}
}

// --- webhook -> verify -> event -> transition (untrusted caller path) ----------

func TestHandleWebhookVerifiesBeforeTransition(t *testing.T) {
	h := newHarness(t, nil)
	org := h.seedOrg(t, "hookco")
	product := h.seedProduct(t, "Web Hook", "hosting", 400, nil)

	// Order first (checkout API path).
	order, sub, err := h.Svc.Checkout(context.Background(), CheckoutInput{
		OrgID: org, ProductID: product.ID, Period: "monthly", Provider: "fake",
	})
	if err != nil {
		t.Fatal(err)
	}

	secret := "whsec-test"
	body, _ := json.Marshal(map[string]any{
		"event_id": "evt-hook-1", "type": WebhookPaymentCaptured,
		"order_ref": order.ID.String(), "amount_minor": 400, "currency": "USD",
	})
	sig := FakeProvider{}.SignPayload(secret, body)

	// Tampered signature must be rejected BEFORE any state change.
	_, _, err = h.Svc.HandleWebhook(context.Background(), "fake", secret,
		http.Header{FakeSignatureHeader: {"deadbeef"}}, body)
	if err == nil {
		t.Fatal("tampered webhook accepted")
	}
	if got := h.subscriptionState(t, sub.ID).ProvisionState; got != StatePending {
		t.Fatalf("state after tampered webhook = %s, want PENDING", got)
	}

	// Valid signature: verify -> event -> transition.
	_, replayed, err := h.Svc.HandleWebhook(context.Background(), "fake", secret,
		http.Header{FakeSignatureHeader: {sig}}, body)
	if err != nil {
		t.Fatalf("valid webhook: %v", err)
	}
	if replayed {
		t.Fatal("first webhook reported replayed")
	}
	if got := h.subscriptionState(t, sub.ID).ProvisionState; got != StateProvisioning {
		t.Fatalf("state after webhook = %s, want PROVISIONING", got)
	}

	// Replay: no double side effects.
	_, replayed, err = h.Svc.HandleWebhook(context.Background(), "fake", secret,
		http.Header{FakeSignatureHeader: {sig}}, body)
	if err != nil {
		t.Fatalf("replay errored: %v", err)
	}
	if !replayed {
		t.Error("webhook replay not detected")
	}
	if got := len(h.Provision.dispatch); got != 1 {
		t.Errorf("dispatches after replay = %d, want 1", got)
	}
	// Unknown provider is refused.
	if _, _, err := h.Svc.HandleWebhook(context.Background(), "notagateway", secret,
		http.Header{}, body); err == nil {
		t.Error("unknown provider accepted")
	}
}
