// Service: the billing business flows on top of the store + providers.
//
//	Purchase:  Order -> Payment -> Invoice -> Provision -> Active
//	Renewal:   Renewal -> Payment -> Extend
//	Failure:   Payment failed -> Grace period -> Suspend -> Terminate
//
// Every state change goes through the ONE Transition function (guard + audit
// + event) and every workload-changing action is an idempotent, retryable
// job. FAILED retains artifacts (row + refs + last_error) for manual retry.
package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// Defaults for the failure walk (configurable via settings keys
// billing.grace_days / billing.suspend_terminate_days / billing.invoice_due_days).
const (
	DefaultGraceDays        = 3
	DefaultSuspendThenDays  = 7
	DefaultInvoiceDueDays   = 7
	DefaultRenewalLeadDays  = 3 // renewal invoiced N days before period end
	DefaultWebhookSecretKey = "billing.webhook_secret"
)

// Service wires the billing flows.
type Service struct {
	Store  *Store
	Jobs   *jobs.Store
	Events *events.Bus
	Audit  *audit.Store

	// Provision dispatches the workload for a paid subscription (by product
	// type: web account or a manual service). Implemented by
	// the API layer (it owns the websites stores + limit engine); the core
	// stays engine-agnostic. It MUST enqueue an idempotent job and record
	// the job id via SetSubscriptionWorkload.
	Provision func(ctx context.Context, sub *Subscription, product *Product) error

	// Suspend turns the workload off using EXISTING lifecycle jobs
	// (web: suspend_website). Reversible: Resume undoes it.
	Suspend func(ctx context.Context, sub *Subscription) error

	// Resume reverses a billing suspension.
	Resume func(ctx context.Context, sub *Subscription) error

	// Terminate is IRREVERSIBLE: backup-first (Phase 11 rule: terminate
	// enqueues the terminate_backup hook when a backup sink is configured)
	// then delete the workload.
	Terminate func(ctx context.Context, sub *Subscription) error

	// SettingsFn resolves a settings string (grace days etc.). Nil = defaults.
	SettingsFn func(ctx context.Context, key string) (string, error)

	// Now is injectable for the grace-walk tests.
	Now func() time.Time

	// WebhookSecretKey is the settings key holding the webhook HMAC secret.
	WebhookSecretKey string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// SettingInt resolves an integer setting with a default.
func (s *Service) SettingInt(ctx context.Context, key string, def int) int {
	if s.SettingsFn == nil {
		return def
	}
	v, err := s.SettingsFn(ctx, key)
	if err != nil || v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
		return def
	}
	return n
}

// GracePeriod returns the configured grace window.
func (s *Service) GracePeriod(ctx context.Context) time.Duration {
	return time.Duration(s.SettingInt(ctx, "billing.grace_days", DefaultGraceDays)) * 24 * time.Hour
}

// SuspendThenTerminate returns how long a SUSPENDED subscription is kept
// before termination.
func (s *Service) SuspendThenTerminate(ctx context.Context) time.Duration {
	return time.Duration(s.SettingInt(ctx, "billing.suspend_terminate_days", DefaultSuspendThenDays)) * 24 * time.Hour
}

// InvoiceDue returns the invoice payment window.
func (s *Service) InvoiceDue(ctx context.Context) time.Duration {
	return time.Duration(s.SettingInt(ctx, "billing.invoice_due_days", DefaultInvoiceDueDays)) * 24 * time.Hour
}

// ---------------------------------------------------------------------------
// audit + events
// ---------------------------------------------------------------------------

// auditSystem records a system-actor audit entry (machine-driven moves).
func (s *Service) auditSystem(ctx context.Context, orgID *uuid.UUID, action string, resourceID string, ok bool, meta map[string]any) {
	if s.Audit == nil {
		return
	}
	entry := audit.Entry{
		OrganizationID: orgID,
		ActorType:      audit.ActorSystem,
		Action:         action,
		ResourceType:   "subscription",
		ResourceID:     resourceID,
		Metadata:       meta,
	}
	if !ok {
		entry.Result = audit.ResultFailure
	}
	s.Audit.RecordBestEffort(ctx, entry)
}

// auditActor records a user-actor audit entry (API-driven moves).
func (s *Service) auditActor(ctx context.Context, orgID *uuid.UUID, actorID *uuid.UUID, action, resourceID string, meta map[string]any) {
	if s.Audit == nil {
		return
	}
	var actor *uuid.UUID
	if actorID != nil && *actorID != uuid.Nil {
		actor = actorID
	}
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actor,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "subscription",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

func (s *Service) publish(ctx context.Context, orgID uuid.UUID, eventType string, subID uuid.UUID, payload map[string]any) {
	if s.Events == nil {
		return
	}
	var org *uuid.UUID
	if orgID != uuid.Nil {
		o := orgID
		org = &o
	}
	s.Events.Publish(ctx, events.Event{
		Type:         eventType,
		Organization: org,
		ActorType:    "system",
		ResourceType: "subscription",
		ResourceID:   subID.String(),
		Payload:      payload,
	})
}

// ---------------------------------------------------------------------------
// THE transition function
// ---------------------------------------------------------------------------

// TransitionResult reports what Transition did.
type TransitionResult struct {
	// Changed is true when the state actually moved; false means the
	// request was an idempotent replay of an already-taken edge.
	Changed bool
	Sub     *Subscription
}

// Transition is the single state-machine entry point. It:
//  1. validates the edge (illegal -> audited failure + TransitionError);
//  2. applies the guarded store move (replay-safe: the same from->to
//     arriving twice reports Changed=false, not an error);
//  3. writes an audit entry (success or failure);
//  4. publishes the billing.subscription.<state> event.
func (s *Service) Transition(ctx context.Context, sub *Subscription, to State, actor *uuid.UUID, reason string) (TransitionResult, error) {
	from := sub.ProvisionState
	if from == to {
		// Idempotent replay of the same state: report, don't error, don't
		// double-audit.
		return TransitionResult{Changed: false, Sub: sub}, nil
	}
	if !CanTransition(from, to) {
		err := &TransitionError{From: from, To: to, Reason: reason}
		s.auditSystem(ctx, &sub.OrgID, StateAuditAction(to), sub.ID.String(), false, map[string]any{
			"from": string(from), "to": string(to), "reason": reason, "error": err.Error(),
		})
		return TransitionResult{}, err
	}

	opts := TransitionOpts{LastError: sub.LastError}
	if reason != "" {
		opts.LastError = reason
	}
	if to == StateActive || to == StateTerminated {
		opts.GraceClear = true
	}
	updated, err := s.Store.UpdateSubscriptionState(ctx, sub.ID, from, to, opts)
	if err != nil {
		return TransitionResult{}, err
	}
	s.auditSystem(ctx, &updated.OrgID, StateAuditAction(to), sub.ID.String(), true, map[string]any{
		"from": string(from), "to": string(to), "reason": reason,
	})
	s.publish(ctx, updated.OrgID, StateEvent(to), sub.ID, map[string]any{
		"from": string(from), "to": string(to), "workload_kind": updated.WorkloadKind,
	})
	return TransitionResult{Changed: true, Sub: updated}, nil
}

// ---------------------------------------------------------------------------
// Purchase flow: Order -> Payment -> Invoice -> Provision -> Active
// ---------------------------------------------------------------------------

// CheckoutInput creates an order (pending) for a product.
type CheckoutInput struct {
	OrgID     uuid.UUID
	ActorID   uuid.UUID
	ProductID uuid.UUID
	Period    string
	Provider  string
}

// Checkout validates the product + period and creates the pending order +
// PENDING subscription. The price is always re-resolved from the product at
// order time (client-claimed amounts are never trusted).
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (*Order, *Subscription, error) {
	if !ValidPeriod(in.Period) {
		return nil, nil, fmt.Errorf("billing_period must be monthly, quarterly or yearly")
	}
	if in.Provider == "" {
		in.Provider = "manual"
	}
	if _, err := ProviderFor(in.Provider); err != nil {
		return nil, nil, err
	}
	product, err := s.Store.GetProduct(ctx, in.ProductID)
	if err != nil {
		return nil, nil, err
	}
	if !product.Active {
		return nil, nil, fmt.Errorf("product %q is not purchasable", product.Name)
	}
	total := product.PriceMinor * int64(PeriodMonths(in.Period))
	if err := ValidateMinorUnit(total); err != nil {
		return nil, nil, err
	}
	if _, err := s.Store.EnsureCustomer(ctx, in.OrgID, product.Currency); err != nil {
		return nil, nil, err
	}
	var planID *uuid.UUID
	if product.PlanID != nil {
		p := *product.PlanID
		planID = &p
	}
	order, sub, err := s.Store.CreateOrderWithSubscription(ctx, OrderInput{
		OrgID: in.OrgID, ProductID: product.ID, PlanID: planID,
		Period: in.Period, AmountMinor: total, Currency: product.Currency,
		Provider: in.Provider, SubscriptionState: StatePending,
		WorkloadKind: string(WorkloadKindForProductType(product.Type)),
		PeriodStart:  s.now(),
		Metadata:     map[string]any{"product_type": product.Type},
	})
	if err != nil {
		return nil, nil, err
	}
	s.auditActor(ctx, &in.OrgID, &in.ActorID, "billing.order_created", order.ID.String(), map[string]any{
		"product": product.Name, "period": in.Period, "amount": FormatMinor(total, product.Currency),
	})
	s.publish(ctx, in.OrgID, "billing.order.created", sub.ID, map[string]any{
		"order_id": order.ID.String(), "amount_minor": total, "currency": product.Currency,
	})
	return order, sub, nil
}

// PayAndActivate is the purchase core: authorize -> capture -> invoice ->
// provision dispatch -> ACTIVE. providerEventID is the gateway's idempotency
// anchor: passing the same event twice replays the whole flow harmlessly
// (payment row deduped, order already paid, no double invoice/provision).
func (s *Service) PayAndActivate(ctx context.Context, in CheckoutInput, methodRef string, providerEventID string) (*Order, *Subscription, bool, error) {
	order, sub, err := s.Checkout(ctx, in)
	if err != nil {
		return nil, nil, false, err
	}
	replayed, err := s.CaptureForOrder(ctx, order, sub, methodRef, providerEventID, InvoiceKindPurchase)
	if err != nil {
		// Payment failed: order cancelled; the PENDING subscription stays
		// for audit but can never provision (no paid order).
		_ = s.Store.CancelOrder(ctx, in.OrgID, order.ID)
		return order, sub, false, err
	}
	// Return the FRESH rows: the guarded moves above rewrote their state.
	freshOrder, err := s.Store.GetOrderAny(ctx, order.ID)
	if err != nil {
		return nil, nil, replayed, err
	}
	freshSub, err := s.Store.GetSubscriptionAny(ctx, sub.ID)
	if err != nil {
		return freshOrder, nil, replayed, err
	}
	return freshOrder, freshSub, replayed, nil
}

// CaptureForOrder runs authorize+capture for an existing order, issues the
// invoice, flips the order to paid and dispatches provisioning.
// Returns replay=true when providerEventID was already processed.
func (s *Service) CaptureForOrder(ctx context.Context, order *Order, sub *Subscription, methodRef, providerEventID, kind string) (bool, error) {
	provider, err := ProviderFor(order.Provider)
	if err != nil {
		return false, err
	}
	// 1. Idempotent payment row (replay anchor).
	_, replayed, err := s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: order.OrgID, OrderID: &order.ID, SubscriptionID: &sub.ID,
		Provider: order.Provider, ProviderEventID: providerEventID, Kind: kind,
		Status: PaymentAuthorized, Amount: Money{Amount: order.AmountMinor, Currency: order.Currency},
	})
	if err != nil {
		return false, err
	}
	if replayed {
		// The webhook is a duplicate: everything downstream already ran.
		return true, nil
	}

	// 2. Authorize.
	authz, err := provider.Authorize(ctx, ChargeRequest{
		Amount:    Money{Amount: order.AmountMinor, Currency: order.Currency},
		MethodRef: methodRef, OrderRef: order.ID.String(),
		Description: "EpicPanel " + order.BillingPeriod + " order",
	})
	if err != nil {
		_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
			OrgID: order.OrgID, OrderID: &order.ID, SubscriptionID: &sub.ID,
			Provider: order.Provider, ProviderEventID: providerEventID + ":autherr", Kind: kind,
			Status: PaymentFailed, Amount: Money{Amount: order.AmountMinor, Currency: order.Currency},
			FailureReason: err.Error(),
		})
		return false, err
	}
	if authz.Declined {
		_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
			OrgID: order.OrgID, OrderID: &order.ID, SubscriptionID: &sub.ID,
			Provider: order.Provider, ProviderEventID: providerEventID + ":declined", Kind: kind,
			Status: PaymentFailed, Amount: Money{Amount: order.AmountMinor, Currency: order.Currency},
			FailureReason: authz.Reason,
		})
		return false, fmt.Errorf("payment declined: %s", authz.Reason)
	}

	// 3. Capture.
	cap, err := provider.Capture(ctx, authz.Ref)
	if err != nil {
		return false, err
	}

	// 4. Invoice (issued == immutable money fields).
	cust, err := s.Store.EnsureCustomer(ctx, order.OrgID, order.Currency)
	if err != nil {
		return false, err
	}
	inv, err := s.Store.IssueInvoice(ctx, InvoiceInput{
		CustomerID: cust.ID, OrgID: order.OrgID, SubscriptionID: &sub.ID,
		Kind: kind, Currency: order.Currency, SubtotalMinor: order.AmountMinor,
		DueIn: s.InvoiceDue(ctx),
		LineItems: []LineItem{{
			Description: fmt.Sprintf("%s (%s, %s)", orderProductName(order), order.BillingPeriod, order.Currency),
			Quantity:    1, UnitMinor: order.AmountMinor, TotalMinor: order.AmountMinor,
		}},
	})
	if err != nil {
		return false, err
	}
	_ = s.Store.MarkInvoicePaid(ctx, inv.ID)
	_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: order.OrgID, OrderID: &order.ID, InvoiceID: &inv.ID, SubscriptionID: &sub.ID,
		Provider: order.Provider, ProviderRef: cap.Ref, ProviderEventID: providerEventID + ":captured",
		Kind: kind, Status: PaymentCaptured,
		Amount: Money{Amount: order.AmountMinor, Currency: order.Currency},
	})

	// 5. Order paid (guarded once-only flip).
	if err := s.Store.MarkOrderPaid(ctx, order.ID, cap.Ref); err != nil {
		return false, err
	}

	// 6. Provision dispatch -> the job chain drives PROVISIONING -> ACTIVE.
	if err := s.dispatchProvision(ctx, sub, order); err != nil {
		// Money captured but dispatch refused: FAILED retains artifacts.
		// Reload first: dispatch already moved the state to PROVISIONING.
		if fresh, ferr := s.Store.GetSubscriptionAny(ctx, sub.ID); ferr == nil {
			sub = fresh
		}
		if _, terr := s.Transition(ctx, sub, StateFailed, nil, "provision dispatch: "+err.Error()); terr != nil {
			slog.Error("billing: failed-state transition errored", "sub", sub.ID, "err", terr)
		}
		return false, err
	}
	return false, nil
}

func orderProductName(o *Order) string {
	if o.ProductName != "" {
		return o.ProductName
	}
	return "subscription"
}

// dispatchProvision flips PENDING -> PROVISIONING and hands off to the
// workload engine (implemented by the API layer). The engine enqueues its
// idempotent job; ACTIVE arrives only on agent/job truth.
func (s *Service) dispatchProvision(ctx context.Context, sub *Subscription, order *Order) error {
	if _, err := s.Transition(ctx, sub, StateProvisioning, nil, "payment captured (order "+order.ID.String()+")"); err != nil {
		return err
	}
	// Reload with product + workload refs resolved.
	fresh, err := s.Store.GetSubscriptionAny(ctx, sub.ID)
	if err != nil {
		return err
	}
	product, err := s.Store.GetProduct(ctx, uuidOrNil(fresh.ProductID))
	if err != nil {
		return err
	}
	if s.Provision == nil {
		return errors.New("no provision engine wired")
	}
	if err := s.Provision(ctx, fresh, product); err != nil {
		return err
	}
	s.publish(ctx, fresh.OrgID, "billing.provision.dispatched", fresh.ID, map[string]any{
		"workload_kind": fresh.WorkloadKind, "product": product.Name,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Webhook path: verify -> event -> transition (never trust client "paid")
// ---------------------------------------------------------------------------

// HandleWebhook verifies the webhook against the registered provider,
// records it idempotently and walks the purchase flow. Unverified requests
// are rejected BEFORE any parse/use.
func (s *Service) HandleWebhook(ctx context.Context, providerName, secret string, header http.Header, body []byte) (*WebhookEvent, bool, error) {
	provider, err := ProviderFor(providerName)
	if err != nil {
		return nil, false, err
	}
	ev, err := provider.VerifyWebhook(secret, header, body)
	if err != nil {
		return nil, false, fmt.Errorf("webhook verification failed: %w", err)
	}
	// Every verified event lands on the bus (webhook -> event -> transition,
	// as the contract requires).
	s.publish(ctx, uuid.Nil, "billing.webhook."+ev.Type, uuid.Nil, map[string]any{
		"event_id": ev.EventID, "order_ref": ev.OrderRef, "provider": providerName,
	})

	orderID, err := uuid.Parse(ev.OrderRef)
	if err != nil {
		return &ev, false, fmt.Errorf("webhook order_ref is not an order id")
	}
	order, err := s.Store.GetOrderAny(ctx, orderID)
	if err != nil {
		return &ev, false, err
	}
	if ev.Type == WebhookPaymentFailed {
		// Record the failure; the order stays pending (customer may retry).
		_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
			OrgID: order.OrgID, OrderID: &order.ID, Provider: providerName,
			ProviderEventID: ev.EventID, Kind: InvoiceKindPurchase, Status: PaymentFailed,
			Amount: ev.Amount, FailureReason: ev.Reason,
		})
		_ = s.Store.CancelOrder(ctx, order.OrgID, order.ID)
		return &ev, false, nil
	}
	if order.Status != OrderPending {
		// Already paid: replay. No double invoice, no double provision.
		return &ev, true, nil
	}
	if order.SubscriptionID == nil {
		return &ev, false, errors.New("order has no subscription")
	}
	sub, err := s.Store.GetSubscriptionAny(ctx, *order.SubscriptionID)
	if err != nil {
		return &ev, false, err
	}
	replayed, err := s.CaptureForOrder(ctx, order, sub, "webhook:"+providerName, ev.EventID, InvoiceKindPurchase)
	return &ev, replayed, err
}

// WebhookSecret resolves the shared webhook secret from settings (set by the
// admin settings API); empty disables the webhook endpoint (403).
func (s *Service) WebhookSecret(ctx context.Context) string {
	key := s.WebhookSecretKey
	if key == "" {
		key = DefaultWebhookSecretKey
	}
	if s.SettingsFn == nil {
		return ""
	}
	v, err := s.SettingsFn(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

// ---------------------------------------------------------------------------
// Job-outcome application (control plane convergence)
// ---------------------------------------------------------------------------

// JobOutcome is the finished job the convergence loop applies to a
// subscription in a transitional state.
type JobOutcome struct {
	SubscriptionID uuid.UUID
	JobType        jobs.Type
	Success        bool
	Error          string
}

// ApplyJobOutcome advances subscriptions out of PROVISIONING / SUSPENDING /
// TERMINATING based on finished workload jobs. Agent truth is the only
// authority: an accepted request never equals success.
func (s *Service) ApplyJobOutcome(ctx context.Context, out JobOutcome) {
	sub, err := s.Store.GetSubscriptionAny(ctx, out.SubscriptionID)
	if err != nil {
		return
	}
	switch sub.ProvisionState {
	case StateProvisioning:
		if out.Success {
			if _, terr := s.Transition(ctx, sub, StateActive, nil, "provision job succeeded"); terr != nil {
				slog.Warn("billing: provision->active failed", "sub", sub.ID, "err", terr)
			}
		} else if out.Terminal() {
			// FAILED retains artifacts for manual retry (row + refs kept).
			if _, terr := s.Transition(ctx, sub, StateFailed, nil, "provision job failed: "+out.Error); terr != nil {
				slog.Warn("billing: provision->failed failed", "sub", sub.ID, "err", terr)
			}
		}
	case StateSuspending:
		if out.Success {
			if _, terr := s.Transition(ctx, sub, StateSuspended, nil, "suspend job succeeded"); terr != nil {
				slog.Warn("billing: suspending->suspended failed", "sub", sub.ID, "err", terr)
			}
		} else if out.Terminal() {
			if _, terr := s.Transition(ctx, sub, StateFailed, nil, "suspend job failed: "+out.Error); terr != nil {
				slog.Warn("billing: suspending->failed failed", "sub", sub.ID, "err", terr)
			}
		}
	case StateTerminating:
		if out.Success {
			if _, terr := s.Transition(ctx, sub, StateTerminated, nil, "terminate job succeeded"); terr != nil {
				slog.Warn("billing: terminating->terminated failed", "sub", sub.ID, "err", terr)
			}
		} else if out.Terminal() {
			if _, terr := s.Transition(ctx, sub, StateFailed, nil, "terminate job failed: "+out.Error); terr != nil {
				slog.Warn("billing: terminating->failed failed", "sub", sub.ID, "err", terr)
			}
		}
	}
}

// Terminal reports whether the job outcome is final (no retries left) — the
// jobs store leaves retryable failures pending, so only failed rows converge.
func (o JobOutcome) Terminal() bool { return !o.Success }

// ---------------------------------------------------------------------------
// Renewal walk: Renewal -> Payment -> Extend
// Failure walk: grace -> suspend -> terminate
// ---------------------------------------------------------------------------

// RunRenewals invoices + charges every due ACTIVE subscription and extends
// the period on success. Called by the scheduler loop (idempotent: the
// invoice exists check in DueForRenewal prevents double invoicing).
func (s *Service) RunRenewals(ctx context.Context) {
	due, err := s.Store.DueForRenewal(ctx, s.now(), 100)
	if err != nil {
		slog.Warn("billing: renewal scan failed", "err", err)
		return
	}
	for i := range due {
		if err := s.renewOne(ctx, &due[i]); err != nil {
			slog.Warn("billing: renewal failed", "sub", due[i].ID, "err", err)
		}
	}
	// Cancelled-at-period-end subscriptions just terminate (no invoice).
	cancelled, err := s.Store.CancelledDueForTerminate(ctx, s.now(), 100)
	if err != nil {
		slog.Warn("billing: cancel-terminate scan failed", "err", err)
		return
	}
	for i := range cancelled {
		if err := s.BeginTerminate(ctx, &cancelled[i], "cancelled at period end"); err != nil {
			slog.Warn("billing: cancel-terminate failed", "sub", cancelled[i].ID, "err", err)
		}
	}
	// Grace expired -> SUSPENDING (workload stop via existing lifecycle jobs).
	expired, err := s.Store.InGraceExpired(ctx, s.now(), 100)
	if err != nil {
		slog.Warn("billing: grace scan failed", "err", err)
		return
	}
	for i := range expired {
		if err := s.BeginSuspend(ctx, &expired[i], "grace period elapsed with unpaid renewal"); err != nil {
			slog.Warn("billing: grace-suspend failed", "sub", expired[i].ID, "err", err)
		}
	}
	// SUSPENDED too long -> TERMINATING (backup-first, irreversible).
	terminated, err := s.Store.SuspendedDueForTerminate(ctx, s.now(), s.SuspendThenTerminate(ctx), 100)
	if err != nil {
		slog.Warn("billing: suspend-terminate scan failed", "err", err)
		return
	}
	for i := range terminated {
		if err := s.BeginTerminate(ctx, &terminated[i], "suspended beyond the terminate window"); err != nil {
			slog.Warn("billing: suspend-terminate failed", "sub", terminated[i].ID, "err", err)
		}
	}
}

// renewOne invoices one due subscription and attempts the charge against
// the stored payment method. On success the period extends; on failure the
// grace clock starts (grace_until = now + grace period).
func (s *Service) renewOne(ctx context.Context, sub *Subscription) error {
	if sub.ProductID == nil {
		return errors.New("subscription has no product")
	}
	product, err := s.Store.GetProduct(ctx, uuidOrNil(sub.ProductID))
	if err != nil {
		return err
	}
	if !product.Active {
		// Product retired: invoice at the last known price.
		product.PriceMinor = 0
	}
	amount := product.PriceMinor * int64(PeriodMonths(sub.BillingPeriod))
	cust, err := s.Store.EnsureCustomer(ctx, sub.OrgID, product.Currency)
	if err != nil {
		return err
	}
	inv, err := s.Store.IssueInvoice(ctx, InvoiceInput{
		CustomerID: cust.ID, OrgID: sub.OrgID, SubscriptionID: &sub.ID,
		Kind: InvoiceKindRenewal, Currency: product.Currency, SubtotalMinor: amount,
		DueIn: s.InvoiceDue(ctx),
		LineItems: []LineItem{{
			Description: fmt.Sprintf("%s renewal (%s)", product.Name, sub.BillingPeriod),
			Quantity:    1, UnitMinor: amount, TotalMinor: amount,
		}},
	})
	if err != nil {
		return err
	}
	// Pin the renewal anchor BEFORE charging: one renewal attempt per
	// period regardless of the outcome (a failed charge starts grace; the
	// sweep must not re-invoice every pass).
	if err := s.Store.SetSubscriptionRenewalAnchor(ctx, sub.ID, sub.PeriodEnd); err != nil {
		return err
	}
	s.publish(ctx, sub.OrgID, "billing.renewal.invoiced", sub.ID, map[string]any{
		"invoice_id": inv.ID.String(), "amount_minor": amount, "currency": product.Currency,
	})

	// Charge the method on file (if any). No method = immediate grace.
	method, err := s.Store.PaymentMethodRaw(ctx, sub.OrgID)
	if err != nil && err != ErrNotFound {
		return err
	}
	var methodRef string
	if method != nil && method.TokenEnc != "" {
		methodRef = s.storedMethodToken(ctx, sub.OrgID)
		if methodRef == "" {
			return errors.New("payment method unreadable")
		}
	}
	providerName := "manual"
	if method != nil && method.Provider != "" {
		providerName = method.Provider
	}
	if methodRef == "" {
		// Nothing to charge: start grace.
		return s.StartGrace(ctx, sub, "no payment method on file for renewal invoice "+inv.Number)
	}
	provider, err := ProviderFor(providerName)
	if err != nil {
		return err
	}
	// Idempotency anchor: one renewal attempt per subscription per period.
	eventID := "renewal:" + sub.ID.String() + ":" + sub.PeriodEnd.UTC().Format("2006-01-02")
	_, replayed, err := s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: sub.OrgID, InvoiceID: &inv.ID, SubscriptionID: &sub.ID,
		Provider: providerName, ProviderEventID: eventID, Kind: InvoiceKindRenewal,
		Status: PaymentAuthorized, Amount: Money{Amount: amount, Currency: product.Currency},
	})
	if err != nil {
		return err
	}
	if replayed {
		return nil // renewal attempt already settled for this period
	}
	authz, err := provider.Authorize(ctx, ChargeRequest{
		Amount: Money{Amount: amount, Currency: product.Currency}, MethodRef: methodRef,
		OrderRef: inv.ID.String(), Description: "EpicPanel renewal " + inv.Number,
	})
	if err != nil || authz.Declined {
		reason := authz.Reason
		if err != nil {
			reason = err.Error()
		}
		_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
			OrgID: sub.OrgID, InvoiceID: &inv.ID, SubscriptionID: &sub.ID,
			Provider: providerName, ProviderEventID: eventID + ":failed", Kind: InvoiceKindRenewal,
			Status: PaymentFailed, Amount: Money{Amount: amount, Currency: product.Currency},
			FailureReason: reason,
		})
		return s.StartGrace(ctx, sub, "renewal payment failed: "+reason)
	}
	cap, err := provider.Capture(ctx, authz.Ref)
	if err != nil {
		return s.StartGrace(ctx, sub, "renewal capture failed: "+err.Error())
	}
	if err := s.Store.MarkInvoicePaid(ctx, inv.ID); err != nil {
		return err
	}
	_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: sub.OrgID, InvoiceID: &inv.ID, SubscriptionID: &sub.ID,
		Provider: providerName, ProviderRef: cap.Ref, ProviderEventID: eventID + ":captured",
		Kind: InvoiceKindRenewal, Status: PaymentCaptured,
		Amount: Money{Amount: amount, Currency: product.Currency},
	})
	// Extend: Renewal -> Payment -> Extend. period_start = old period_end.
	updated, err := s.Store.ExtendPeriod(ctx, sub.ID)
	if err != nil {
		return err
	}
	// Resume a suspended workload if renewal landed during the suspend path.
	if updated.ProvisionState == StateSuspended || updated.ProvisionState == StateSuspending {
		if err := s.ResumeBilling(ctx, updated, "renewal paid during suspension"); err != nil {
			slog.Warn("billing: resume after renewal failed", "sub", sub.ID, "err", err)
		}
	}
	s.auditSystem(ctx, &sub.OrgID, "billing.renewal_paid", sub.ID.String(), true, map[string]any{
		"invoice": inv.Number, "amount": FormatMinor(amount, product.Currency),
	})
	s.publish(ctx, sub.OrgID, "billing.renewal.paid", sub.ID, map[string]any{
		"invoice_id": inv.ID.String(), "period_end": updated.PeriodEnd,
	})
	return nil
}

// PayInvoice settles an OPEN invoice (customer checkout or admin-recorded
// offline payment). For renewal invoices this is the RECOVERY path: the
// period extends and a suspended workload resumes (payment during the
// failure walk). Money fields stay immutable — status flips only.
func (s *Service) PayInvoice(ctx context.Context, orgID, invoiceID uuid.UUID, methodRef, providerName string, actor *uuid.UUID) (*Invoice, error) {
	inv, err := s.Store.GetInvoice(ctx, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	if inv.Status != InvoiceOpen {
		return nil, ErrInvoiceState
	}
	if providerName == "" {
		// Use the instrument on file when the caller does not name one.
		if pm, perr := s.Store.PaymentMethodRaw(ctx, orgID); perr == nil && pm != nil && pm.Provider != "" {
			providerName = pm.Provider
		} else {
			providerName = "manual"
		}
	}
	provider, err := ProviderFor(providerName)
	if err != nil {
		return nil, err
	}
	if methodRef == "" {
		methodRef = s.storedMethodToken(ctx, orgID)
	}
	eventID := "invoice:" + inv.ID.String()
	_, replayed, err := s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: orgID, InvoiceID: &inv.ID, SubscriptionID: inv.SubscriptionID,
		Provider: providerName, ProviderEventID: eventID, Kind: inv.Kind,
		Status: PaymentAuthorized, Amount: Money{Amount: inv.TotalMinor, Currency: inv.Currency},
	})
	if err != nil {
		return nil, err
	}
	if replayed {
		return inv, nil // already settled
	}
	authz, err := provider.Authorize(ctx, ChargeRequest{
		Amount: Money{Amount: inv.TotalMinor, Currency: inv.Currency}, MethodRef: methodRef,
		OrderRef: inv.ID.String(), Description: "EpicPanel invoice " + inv.Number,
	})
	if err != nil || authz.Declined {
		reason := authz.Reason
		if err != nil {
			reason = err.Error()
		}
		_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
			OrgID: orgID, InvoiceID: &inv.ID, SubscriptionID: inv.SubscriptionID,
			Provider: providerName, ProviderEventID: eventID + ":failed", Kind: inv.Kind,
			Status: PaymentFailed, Amount: Money{Amount: inv.TotalMinor, Currency: inv.Currency},
			FailureReason: reason,
		})
		return nil, fmt.Errorf("payment declined: %s", reason)
	}
	cap, err := provider.Capture(ctx, authz.Ref)
	if err != nil {
		return nil, err
	}
	if err := s.Store.MarkInvoicePaid(ctx, inv.ID); err != nil {
		return nil, err
	}
	_, _, _ = s.Store.RecordPayment(ctx, PaymentInput{
		OrgID: orgID, InvoiceID: &inv.ID, SubscriptionID: inv.SubscriptionID,
		Provider: providerName, ProviderRef: cap.Ref, ProviderEventID: eventID + ":captured",
		Kind: inv.Kind, Status: PaymentCaptured,
		Amount: Money{Amount: inv.TotalMinor, Currency: inv.Currency},
	})
	if fresh, ferr := s.Store.GetInvoiceAny(ctx, inv.ID); ferr == nil {
		inv = fresh
	}
	s.auditSystem(ctx, &orgID, "billing.invoice_paid", inv.ID.String(), true, map[string]any{
		"number": inv.Number, "kind": inv.Kind,
	})
	s.publish(ctx, orgID, "billing.invoice.paid", uuidOrNil(inv.SubscriptionID), map[string]any{
		"invoice_id": inv.ID.String(), "kind": inv.Kind,
	})

	// Renewal recovery: extend the period + resume the workload.
	if inv.Kind == InvoiceKindRenewal && inv.SubscriptionID != nil {
		sub, serr := s.Store.GetSubscriptionAny(ctx, *inv.SubscriptionID)
		if serr == nil {
			updated, xerr := s.Store.ExtendPeriod(ctx, sub.ID)
			if xerr != nil {
				return inv, xerr
			}
			if updated.ProvisionState == StateSuspended || updated.ProvisionState == StateSuspending {
				if rerr := s.ResumeBilling(ctx, updated, "renewal invoice "+inv.Number+" paid"); rerr != nil {
					slog.Warn("billing: resume after invoice pay failed", "sub", sub.ID, "err", rerr)
				}
			}
		}
	}
	return inv, nil
}

// storedMethodToken decrypts the org's saved instrument (internal).
func (s *Service) storedMethodToken(ctx context.Context, orgID uuid.UUID) string {
	pm, err := s.Store.PaymentMethodRaw(ctx, orgID)
	if err != nil || pm == nil || pm.TokenEnc == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(pm.TokenEnc)
	if err != nil {
		return ""
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return ""
	}
	return plain
}

// StartGrace puts a subscription into grace (ACTIVE + grace_until set).
// The workload keeps running during grace.
func (s *Service) StartGrace(ctx context.Context, sub *Subscription, reason string) error {
	until := s.now().Add(s.GracePeriod(ctx))
	if err := s.Store.SetSubscriptionGrace(ctx, sub.ID, &until); err != nil {
		return err
	}
	s.auditSystem(ctx, &sub.OrgID, "billing.grace_started", sub.ID.String(), true, map[string]any{
		"reason": reason, "grace_until": until,
	})
	s.publish(ctx, sub.OrgID, "billing.grace.started", sub.ID, map[string]any{
		"reason": reason, "grace_until": until,
	})
	return nil
}

// BeginSuspend moves ACTIVE -> SUSPENDING and enqueues the workload suspend
// through the EXISTING lifecycle jobs (web: suspend_website). Reversible until SUSPENDED.
func (s *Service) BeginSuspend(ctx context.Context, sub *Subscription, reason string) error {
	if _, err := s.Transition(ctx, sub, StateSuspending, nil, reason); err != nil {
		return err
	}
	if s.Suspend == nil {
		return errors.New("no suspend engine wired")
	}
	if err := s.Suspend(ctx, sub); err != nil {
		return err
	}
	return nil
}

// ResumeBilling brings a suspended/suspending subscription back (payment
// landed during the failure walk). SUSPENDED -> ACTIVE, then the workload
// resume job.
func (s *Service) ResumeBilling(ctx context.Context, sub *Subscription, reason string) error {
	from := sub.ProvisionState
	if _, err := s.Transition(ctx, sub, StateActive, nil, reason); err != nil {
		return err
	}
	if from == StateSuspended && s.Resume != nil {
		fresh, err := s.Store.GetSubscriptionAny(ctx, sub.ID)
		if err != nil {
			return err
		}
		return s.Resume(ctx, fresh)
	}
	return nil
}

// BeginTerminate moves to TERMINATING and runs the IRREVERSIBLE delete
// (backup-first per the Phase 11 rule). Artifacts retained on failure.
func (s *Service) BeginTerminate(ctx context.Context, sub *Subscription, reason string) error {
	if _, err := s.Transition(ctx, sub, StateTerminating, nil, reason); err != nil {
		return err
	}
	if s.Terminate == nil {
		return errors.New("no terminate engine wired")
	}
	if err := s.Terminate(ctx, sub); err != nil {
		return err
	}
	return nil
}

// ManualRetry re-drives a FAILED subscription (admin action). The retained
// artifacts (workload refs + product) decide which action resumes.
func (s *Service) ManualRetry(ctx context.Context, sub *Subscription, actor *uuid.UUID) error {
	from := sub.ProvisionState
	switch from {
	case StateFailed:
		if sub.LastError != "" && containsString(sub.LastError, "terminate job failed") {
			return s.BeginTerminate(ctx, sub, "manual retry of terminate")
		}
		if sub.LastError != "" && containsString(sub.LastError, "suspend job failed") {
			return s.BeginSuspend(ctx, sub, "manual retry of suspend")
		}
		// Default: re-attempt provisioning.
		product, err := s.Store.GetProduct(ctx, uuidOrNil(sub.ProductID))
		if err != nil {
			return err
		}
		if _, err := s.Transition(ctx, sub, StateProvisioning, actor, "manual retry"); err != nil {
			return err
		}
		fresh, err := s.Store.GetSubscriptionAny(ctx, sub.ID)
		if err != nil {
			return err
		}
		if s.Provision == nil {
			return errors.New("no provision engine wired")
		}
		return s.Provision(ctx, fresh, product)
	default:
		return &TransitionError{From: from, To: StateProvisioning, Reason: "manual retry is only available for FAILED"}
	}
}

// ---------------------------------------------------------------------------
// webhook secret helper (settings-backed HMAC shared secret)
// ---------------------------------------------------------------------------

// SignWebhookBody is the HMAC the fake gateway uses; exposed so the admin
// settings screen can validate a configured secret end-to-end.
func SignWebhookBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// uuidOrNil dereferences an optional uuid pointer (nil -> uuid.Nil).
func uuidOrNil(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}
