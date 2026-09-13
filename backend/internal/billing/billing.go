// Package billing implements Phase 10: the Order -> Payment -> Invoice ->
// Provision -> Active lifecycle, the verbatim provisioning state machine
// (PENDING -> PROVISIONING -> ACTIVE -> SUSPENDING -> SUSPENDED ->
// TERMINATING -> TERMINATED -> FAILED), the pluggable payment-gateway
// abstraction and the renewal/grace/suspend/terminate failure walk.
//
// Invariants enforced here (and proven by tests):
//   - money is ALWAYS integer minor units + a currency code, never floats;
//   - invoices are immutable once issued (DB trigger + store guards);
//   - exactly ONE transition function guards every state edge; illegal
//     transitions are rejected AND audited;
//   - every state-changing workload action rides an idempotent, retryable
//     job (API -> Job -> Queue -> Node Agent -> Event -> UI — never
//     HTTP -> SSH -> wait -> return);
//   - gateway webhooks are verified, then turned into events, then into
//     transitions — client-side "paid" claims are never trusted; replays
//     are idempotent (no double provision, no double invoice).
package billing

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Money — integer minor units + currency. There is no float money in the
// billing path; every amount crosses the API/store boundary as minor units.
// ---------------------------------------------------------------------------

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidateCurrency accepts exactly three uppercase letters (ISO-4217 style).
func ValidateCurrency(c string) error {
	if !currencyRe.MatchString(c) {
		return fmt.Errorf("currency must be a 3-letter uppercase code, got %q", c)
	}
	return nil
}

// ValidateMinorUnit rejects negative amounts (money enters as order totals,
// line totals or payment captures — none may be negative; refunds are a
// payment status, not a negative row).
func ValidateMinorUnit(amount int64) error {
	if amount < 0 {
		return fmt.Errorf("amount must be >= 0 minor units, got %d", amount)
	}
	return nil
}

// FormatMinor renders minor units as a decimal string with the currency
// ("12.34 USD"). Display-only helper — never used for arithmetic.
func FormatMinor(amount int64, currency string) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, amount/100, amount%100, currency)
}

// BillingPeriod is the renewal cadence of a subscription.
type BillingPeriod string

const (
	PeriodMonthly   BillingPeriod = "monthly"
	PeriodQuarterly BillingPeriod = "quarterly"
	PeriodYearly    BillingPeriod = "yearly"
)

// ValidPeriod reports whether p is one of the three supported cadences.
func ValidPeriod(p string) bool {
	switch BillingPeriod(p) {
	case PeriodMonthly, PeriodQuarterly, PeriodYearly:
		return true
	}
	return false
}

// PeriodMonths maps a billing period to its month multiple (monthly price x
// months = order total). Unknown periods are rejected by the store.
func PeriodMonths(p string) int {
	switch BillingPeriod(p) {
	case PeriodQuarterly:
		return 3
	case PeriodYearly:
		return 12
	default:
		return 1
	}
}

// AddPeriod advances t by one billing period.
func AddPeriod(t time.Time, p string) time.Time {
	switch BillingPeriod(p) {
	case PeriodQuarterly:
		return t.AddDate(0, 3, 0)
	case PeriodYearly:
		return t.AddDate(1, 0, 0)
	default:
		return t.AddDate(0, 1, 0)
	}
}

// ---------------------------------------------------------------------------
// State machine — the verbatim master-doc states. Stored exactly as written
// (uppercase) in subscriptions.provision_state.
//
//	PENDING -> PROVISIONING -> ACTIVE -> SUSPENDING -> SUSPENDED
//	        -> TERMINATING -> TERMINATED -> FAILED
// ---------------------------------------------------------------------------

// State is one provisioning state of a subscription.
type State string

const (
	StatePending      State = "PENDING"
	StateProvisioning State = "PROVISIONING"
	StateActive       State = "ACTIVE"
	StateSuspending   State = "SUSPENDING"
	StateSuspended    State = "SUSPENDED"
	StateTerminating  State = "TERMINATING"
	StateTerminated   State = "TERMINATED"
	StateFailed       State = "FAILED"
)

// ValidState reports whether s is one of the eight machine states.
func ValidState(s string) bool {
	switch State(s) {
	case StatePending, StateProvisioning, StateActive, StateSuspending,
		StateSuspended, StateTerminating, StateTerminated, StateFailed:
		return true
	}
	return false
}

// WorkloadKind selects the provisioning engine for a subscription. It maps
// 1:1 from the Phase 2 product types (hosting->web, minecraft, discord,
// service).
type WorkloadKind string

const (
	KindWeb       WorkloadKind = "web"
	KindMinecraft WorkloadKind = "minecraft"
	KindDiscord   WorkloadKind = "discord"
	KindService   WorkloadKind = "service" // no node workload; admin fulfils manually
)

// WorkloadKindForProductType maps a Phase 2 product type to the workload
// kind its subscriptions provision.
func WorkloadKindForProductType(productType string) WorkloadKind {
	switch productType {
	case "hosting":
		return KindWeb
	case "minecraft":
		return KindMinecraft
	case "discord":
		return KindDiscord
	default:
		return KindService
	}
}

// edges is the COMPLETE legal-edge set. Every other from->to pair is illegal
// and rejected (single Transition function) + audited.
//
// Rationale per state:
//   - PENDING: paid -> PROVISIONING; dispatch refused -> FAILED (artifacts
//     retained); admin voids the purchase -> TERMINATED (nothing to delete).
//   - PROVISIONING: agent job success -> ACTIVE; terminal failure -> FAILED.
//   - ACTIVE: renewal walk -> SUSPENDING; admin/customer terminate -> TERMINATING.
//   - SUSPENDING: workload stopped -> SUSPENDED; payment lands in the window
//     (reversible suspend) -> ACTIVE; suspend job dead-letters -> FAILED.
//   - SUSPENDED: reactivation payment -> ACTIVE; terminate window elapsed ->
//     TERMINATING.
//   - TERMINATING: delete job success -> TERMINATED (irreversible); failure
//     -> FAILED (artifacts retained for manual retry).
//   - TERMINATED: terminal — no exits.
//   - FAILED: manual retry re-enters the interrupted action (PROVISIONING /
//     SUSPENDING / TERMINATING) or admin abandons -> TERMINATED.
var edges = map[State][]State{
	StatePending:      {StateProvisioning, StateFailed, StateTerminated},
	StateProvisioning: {StateActive, StateFailed},
	StateActive:       {StateSuspending, StateTerminating},
	StateSuspending:   {StateSuspended, StateActive, StateFailed},
	StateSuspended:    {StateActive, StateTerminating},
	StateTerminating:  {StateTerminated, StateFailed},
	StateTerminated:   {},
	StateFailed:       {StateProvisioning, StateSuspending, StateTerminating, StateTerminated},
}

// CanTransition reports whether from->to is a legal edge of the machine.
func CanTransition(from, to State) bool {
	if from == to {
		return false // same-state is handled as an idempotent no-op by Transition
	}
	for _, s := range edges[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionError is the illegal-edge error. It is audited with
// result=failure at the point of rejection.
type TransitionError struct {
	From, To State
	Reason   string
}

func (e *TransitionError) Error() string {
	msg := "illegal subscription state transition " + string(e.From) + " -> " + string(e.To)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// LegacyStatus projects a machine state onto the Phase 2 `status` column
// ('trialing','active','past_due','cancelled') so pre-billing readers keep
// working. The machine itself lives in provision_state.
func LegacyStatus(s State) string {
	switch s {
	case StatePending, StateProvisioning:
		return "trialing"
	case StateActive:
		return "active"
	case StateSuspending, StateSuspended, StateFailed:
		return "past_due"
	case StateTerminating, StateTerminated:
		return "cancelled"
	default:
		return "trialing"
	}
}

// StateEvent returns the bus event type for reaching state s ("billing.").
func StateEvent(s State) string {
	return "billing.subscription." + strings.ToLower(string(s))
}

// StateAuditAction returns the audit action for reaching state s.
func StateAuditAction(s State) string {
	return "billing.subscription_" + strings.ToLower(string(s))
}
