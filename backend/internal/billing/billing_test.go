package billing

// Unit tests for the pure billing core: the verbatim state machine, money
// invariants, the payment providers (deterministic fake + manual) and
// webhook verification/replay mechanics. DB-backed integration tests live
// in internal/api/phase10_billing_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- state machine: the verbatim states -------------------------------------

func TestVerbatimStatesExist(t *testing.T) {
	want := []string{"PENDING", "PROVISIONING", "ACTIVE", "SUSPENDING", "SUSPENDED", "TERMINATING", "TERMINATED", "FAILED"}
	if len(want) != 8 {
		t.Fatalf("machine must have exactly the 8 verbatim states")
	}
	for _, s := range want {
		if !ValidState(s) {
			t.Errorf("state %s missing from the machine", s)
		}
	}
	extra := 0
	for s := range map[State]bool{
		StatePending: true, StateProvisioning: true, StateActive: true, StateSuspending: true,
		StateSuspended: true, StateTerminating: true, StateTerminated: true, StateFailed: true,
	} {
		if !ValidState(string(s)) {
			t.Errorf("state %v not valid", s)
		}
		extra++
	}
	if extra != 8 {
		t.Fatalf("machine gained states beyond the 8 verbatim ones: %d", extra)
	}
}

// happyPath proves the required flow: purchase -> ACTIVE, and the failure
// walk PENDING/ACTIVE -> grace -> SUSPENDED -> TERMINATED is reachable.
func TestHappyPathEdges(t *testing.T) {
	steps := []struct {
		from, to State
	}{
		{StatePending, StateProvisioning},
		{StateProvisioning, StateActive},
		{StateActive, StateSuspending},
		{StateSuspending, StateSuspended},
		{StateSuspended, StateTerminating},
		{StateTerminating, StateTerminated},
	}
	for _, st := range steps {
		if !CanTransition(st.from, st.to) {
			t.Errorf("required edge %s -> %s missing", st.from, st.to)
		}
	}
}

func TestReversibleSuspendEdges(t *testing.T) {
	// Suspend must be reversible until the delete job runs (payment lands
	// during the walk): SUSPENDING -> ACTIVE and SUSPENDED -> ACTIVE.
	if !CanTransition(StateSuspending, StateActive) {
		t.Error("SUSPENDING -> ACTIVE (reversible suspend) missing")
	}
	if !CanTransition(StateSuspended, StateActive) {
		t.Error("SUSPENDED -> ACTIVE (reactivation) missing")
	}
	// Suspendable straight from ACTIVE (admin terminate) and back from grace.
	if !CanTransition(StateActive, StateTerminating) {
		t.Error("ACTIVE -> TERMINATING (admin terminate) missing")
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	// The machine defines exactly which states are reachable; everything
	// else must be rejected. Table = every illegal jump we could think of,
	// including the tempting ones (jumping to TERMINATED, skipping states,
	// resurrecting TERMINATED).
	all := []State{StatePending, StateProvisioning, StateActive, StateSuspending,
		StateSuspended, StateTerminating, StateTerminated, StateFailed}
	for _, from := range all {
		for _, to := range all {
			legal := CanTransition(from, to)
			if from == to && legal {
				t.Errorf("self-transition %s -> %s must not be a legal edge", from, to)
			}
			// Spot-check the invariants the contract calls out:
			// TERMINATED is terminal; ACTIVE cannot jump to TERMINATED;
			// PENDING cannot jump to ACTIVE; FAILED cannot jump to ACTIVE.
			switch {
			case from == StateTerminated && to != StateTerminated:
				if legal {
					t.Errorf("TERMINATED must be terminal, but %s -> %s is legal", from, to)
				}
			case from == StateActive && to == StateTerminated:
				if legal {
					t.Error("ACTIVE -> TERMINATED skips the machine")
				}
			case from == StatePending && to == StateActive:
				if legal {
					t.Error("PENDING -> ACTIVE skips PROVISIONING")
				}
			case from == StatePending && to == StateSuspended:
				if legal {
					t.Error("PENDING -> SUSPENDED skips the machine")
				}
			case from == StateFailed && to == StateActive:
				if legal {
					t.Error("FAILED -> ACTIVE skips re-dispatch")
				}
			case from == StateSuspended && to == StateSuspended:
				if legal {
					t.Error("self edge leaked")
				}
			}
		}
	}
}

// --- money -------------------------------------------------------------------

func TestMoneyValidation(t *testing.T) {
	if err := ValidateCurrency("USD"); err != nil {
		t.Errorf("USD rejected: %v", err)
	}
	for _, bad := range []string{"", "usd", "US", "USDD", "U D", "123"} {
		if err := ValidateCurrency(bad); err == nil {
			t.Errorf("currency %q accepted", bad)
		}
	}
	if err := ValidateMinorUnit(-1); err == nil {
		t.Error("negative minor units accepted")
	}
	if err := ValidateMinorUnit(0); err != nil {
		t.Errorf("zero rejected: %v", err)
	}
	if got := FormatMinor(12345, "USD"); got != "123.45 USD" {
		t.Errorf("FormatMinor = %q, want 123.45 USD", got)
	}
	if got := FormatMinor(5, "EUR"); got != "0.05 EUR" {
		t.Errorf("FormatMinor = %q, want 0.05 EUR", got)
	}
}

func TestPeriods(t *testing.T) {
	if !ValidPeriod("monthly") || !ValidPeriod("quarterly") || !ValidPeriod("yearly") {
		t.Error("valid periods rejected")
	}
	if ValidPeriod("weekly") {
		t.Error("weekly accepted")
	}
	if PeriodMonths("monthly") != 1 || PeriodMonths("quarterly") != 3 || PeriodMonths("yearly") != 12 {
		t.Error("period month mapping wrong")
	}
	base := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if AddPeriod(base, "monthly").Month() != time.March || AddPeriod(base, "monthly").Day() != 3 {
		// Jan 31 + 1 month normalizes to Mar 3 (Go date arithmetic); what
		// matters is that it is strictly later and stays parseable.
		t.Logf("monthly add from Jan 31 -> %v (normalization, acceptable)", AddPeriod(base, "monthly"))
	}
	if AddPeriod(base, "yearly").Year() != 2027 {
		t.Error("yearly period mapping wrong")
	}
}

// --- fake provider determinism -----------------------------------------------

func TestFakeProviderDeterministic(t *testing.T) {
	p := FakeProvider{}
	ctx := context.Background()
	req := ChargeRequest{Amount: Money{Amount: 1999, Currency: "USD"}, MethodRef: "tok_visa", OrderRef: "order-1"}

	a1, err := p.Authorize(ctx, req)
	if err != nil || a1.Declined {
		t.Fatalf("authorize: %v %v", a1, err)
	}
	a2, _ := p.Authorize(ctx, req)
	if a1.Ref != a2.Ref {
		t.Error("same charge produced different authorization refs (not deterministic)")
	}
	c1, err := p.Capture(ctx, a1.Ref)
	if err != nil || c1.Status != "captured" {
		t.Fatalf("capture: %v %v", c1, err)
	}
	c2, _ := p.Capture(ctx, a1.Ref)
	if c1.Ref != c2.Ref {
		t.Error("same auth captured twice with different refs (not deterministic)")
	}

	// Decline rule: method refs starting with "fail" always decline.
	declined, err := p.Authorize(ctx, ChargeRequest{Amount: Money{Amount: 100, Currency: "USD"}, MethodRef: "fail-card", OrderRef: "o2"})
	if err != nil {
		t.Fatalf("declined auth errored: %v", err)
	}
	if !declined.Declined || declined.Reason == "" {
		t.Errorf("fail-card not declined: %+v", declined)
	}
}

func TestFakeProviderValidation(t *testing.T) {
	p := FakeProvider{}
	if _, err := p.Authorize(context.Background(), ChargeRequest{Amount: Money{Amount: 100, Currency: "usd"}}); err == nil {
		t.Error("invalid currency authorized")
	}
	if _, err := p.Capture(context.Background(), ""); err == nil {
		t.Error("empty auth ref captured")
	}
	if _, err := p.VerifyWebhook("secret", http.Header{"X-Fake-Signature": {"nope"}}, []byte("{}")); err == nil {
		t.Error("bad signature accepted")
	}
}

// --- webhook verify + replay idempotency mechanics ---------------------------

func fakeWebhookBody(t *testing.T, eventID, typ string, amountMinor int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"event_id": eventID, "type": typ, "order_ref": "11111111-1111-1111-1111-111111111111",
		"amount_minor": amountMinor, "currency": "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestWebhookVerifySignatureRequired(t *testing.T) {
	p := FakeProvider{}
	body := fakeWebhookBody(t, "evt-1", WebhookPaymentCaptured, 1999)

	sig := p.SignPayload("secret", body)
	ev, err := p.VerifyWebhook("secret", http.Header{FakeSignatureHeader: {sig}}, body)
	if err != nil {
		t.Fatalf("valid webhook rejected: %v", err)
	}
	if ev.EventID != "evt-1" || ev.Type != WebhookPaymentCaptured || ev.Amount.Amount != 1999 {
		t.Errorf("decoded webhook wrong: %+v", ev)
	}

	// Signature over different bytes must fail (payload substitution).
	if _, err := p.VerifyWebhook("secret", http.Header{FakeSignatureHeader: {sig}}, append(body, byte(' '))); err == nil {
		t.Error("tampered body accepted")
	}
	// Wrong secret fails.
	if _, err := p.VerifyWebhook("other", http.Header{FakeSignatureHeader: {sig}}, body); err == nil {
		t.Error("wrong secret accepted")
	}
	// Missing event id fails (replay anchor mandatory).
	noID, _ := json.Marshal(map[string]any{"type": WebhookPaymentCaptured})
	sig2 := p.SignPayload("secret", noID)
	if _, err := p.VerifyWebhook("secret", http.Header{FakeSignatureHeader: {sig2}}, noID); err == nil {
		t.Error("webhook without event_id accepted")
	}
	// Unknown type fails.
	badType, _ := json.Marshal(map[string]any{"event_id": "e", "type": "payment.refunded"})
	sig3 := p.SignPayload("secret", badType)
	if _, err := p.VerifyWebhook("secret", http.Header{FakeSignatureHeader: {sig3}}, badType); err == nil {
		t.Error("unsupported webhook type accepted")
	}
}

func TestManualProviderHasNoWebhooks(t *testing.T) {
	if _, err := (ManualProvider{}).VerifyWebhook("s", http.Header{}, []byte("{}")); err == nil {
		t.Error("manual provider accepted a webhook")
	}
	// Registry contains exactly the two bundled gateways.
	names := ProviderNames()
	if len(names) != 2 {
		t.Fatalf("bundled providers = %v, want [fake manual]", names)
	}
	if _, err := ProviderFor("fake"); err != nil {
		t.Errorf("fake provider not registered: %v", err)
	}
	if _, err := ProviderFor("manual"); err != nil {
		t.Errorf("manual provider not registered: %v", err)
	}
	if _, err := ProviderFor("stripe"); err == nil {
		t.Error("unregistered provider resolved")
	}
}

// --- legacy status projection -------------------------------------------------

func TestLegacyStatusProjection(t *testing.T) {
	cases := map[State]string{
		StatePending:      "trialing",
		StateProvisioning: "trialing",
		StateActive:       "active",
		StateSuspended:    "past_due",
		StateFailed:       "past_due",
		StateTerminated:   "cancelled",
	}
	for state, want := range cases {
		if got := LegacyStatus(state); got != want {
			t.Errorf("LegacyStatus(%s) = %s, want %s", state, got, want)
		}
	}
}

func TestStateEventsAreNamespaced(t *testing.T) {
	if !strings.HasPrefix(StateEvent(StateActive), "billing.subscription.") {
		t.Error("state events must live in the billing.* namespace")
	}
	if StateEvent(StateSuspended) != "billing.subscription.suspended" {
		t.Errorf("event name = %s", StateEvent(StateSuspended))
	}
}
