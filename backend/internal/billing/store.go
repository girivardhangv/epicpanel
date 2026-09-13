// Store: Order / Payment / Invoice / Subscription persistence. Money is
// integer minor units + currency everywhere. Invoices are immutable once
// issued (store guard + DB trigger from 0030). The guarded subscription
// state update lives here; the ONE Transition function (audit + event +
// guard) wraps it in service.go.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row does not exist (org-scoped lookups
// also return it for cross-tenant ids: cross-tenant access is 404).
var ErrNotFound = errors.New("billing record not found")

// ErrInvoiceImmutable is returned when a mutation would alter a issued
// invoice's money fields.
var ErrInvoiceImmutable = errors.New("invoice is immutable once issued")

// ErrInvoiceState is returned for invalid invoice status transitions
// (e.g. voiding a paid invoice).
var ErrInvoiceState = errors.New("invoice status does not allow this operation")

// Invoice statuses (Phase 2 columns) + kinds.
const (
	InvoiceDraft  = "draft"
	InvoiceOpen   = "open"
	InvoicePaid   = "paid"
	InvoiceVoid   = "void"
	InvoiceRefund = "refunded"

	InvoiceKindPurchase = "purchase"
	InvoiceKindRenewal  = "renewal"
	InvoiceKindManual   = "manual"
)

// Order statuses.
const (
	OrderPending   = "pending"
	OrderPaid      = "paid"
	OrderCancelled = "cancelled"
	OrderFailed    = "failed"
)

// Payment statuses.
const (
	PaymentAuthorized = "authorized"
	PaymentCaptured   = "captured"
	PaymentFailed     = "failed"
	PaymentRefunded   = "refunded"
)

// Product is a purchasable item; plan_id links it to a Phase 9 plan
// (plans <-> products).
type Product struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"` // hosting | service
	Description string          `json:"description"`
	PlanID      *uuid.UUID      `json:"plan_id,omitempty"`
	PlanName    string          `json:"plan_name,omitempty"`
	PlanKind    string          `json:"plan_kind,omitempty"`
	PriceMinor  int64           `json:"price_minor"`
	Currency    string          `json:"currency"`
	Active      bool            `json:"active"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// Order is one purchase attempt (Order -> Payment -> ...).
type Order struct {
	ID             uuid.UUID       `json:"id"`
	OrgID          uuid.UUID       `json:"organization_id"`
	SubscriptionID *uuid.UUID      `json:"subscription_id,omitempty"`
	ProductID      uuid.UUID       `json:"product_id"`
	PlanID         *uuid.UUID      `json:"plan_id,omitempty"`
	Status         string          `json:"status"`
	BillingPeriod  string          `json:"billing_period"`
	AmountMinor    int64           `json:"amount_minor"`
	Currency       string          `json:"currency"`
	Provider       string          `json:"provider"`
	ProviderRef    string          `json:"provider_ref,omitempty"`
	PaidAt         *time.Time      `json:"paid_at,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ProductName    string          `json:"product_name,omitempty"`
}

// Payment is one gateway interaction (capture or recorded failure).
type Payment struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"organization_id"`
	OrderID         *uuid.UUID `json:"order_id,omitempty"`
	InvoiceID       *uuid.UUID `json:"invoice_id,omitempty"`
	SubscriptionID  *uuid.UUID `json:"subscription_id,omitempty"`
	Provider        string     `json:"provider"`
	ProviderRef     string     `json:"provider_ref,omitempty"`
	ProviderEventID string     `json:"provider_event_id,omitempty"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	AmountMinor     int64      `json:"amount_minor"`
	Currency        string     `json:"currency"`
	FailureReason   string     `json:"failure_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Invoice is the Phase 2 invoice row + Phase 10 links.
type Invoice struct {
	ID             uuid.UUID       `json:"id"`
	CustomerID     uuid.UUID       `json:"customer_id"`
	OrgID          uuid.UUID       `json:"organization_id"`
	Number         string          `json:"number"`
	Status         string          `json:"status"`
	Currency       string          `json:"currency"`
	SubtotalMinor  int64           `json:"subtotal_minor"`
	TaxMinor       int64           `json:"tax_minor"`
	TotalMinor     int64           `json:"total_minor"`
	LineItems      json.RawMessage `json:"line_items"`
	Kind           string          `json:"kind"`
	SubscriptionID *uuid.UUID      `json:"subscription_id,omitempty"`
	IssuedAt       *time.Time      `json:"issued_at,omitempty"`
	DueAt          *time.Time      `json:"due_at,omitempty"`
	PaidAt         *time.Time      `json:"paid_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Subscription is the provisioned service with its verbatim state.
type Subscription struct {
	ID                uuid.UUID  `json:"id"`
	CustomerID        uuid.UUID  `json:"customer_id"`
	OrgID             uuid.UUID  `json:"organization_id"`
	PlanID            *uuid.UUID `json:"plan_id,omitempty"`
	ProductID         *uuid.UUID `json:"product_id,omitempty"`
	Status            string     `json:"status"` // legacy Phase 2 projection
	ProvisionState    State      `json:"provision_state"`
	BillingPeriod     string     `json:"billing_period"`
	PeriodStart       time.Time  `json:"period_start"`
	PeriodEnd         time.Time  `json:"period_end"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	StateChangedAt    time.Time  `json:"state_changed_at"`
	WorkloadKind      string     `json:"workload_kind"`
	WebsiteID         *uuid.UUID `json:"website_id,omitempty"`
	GraceUntil        *time.Time `json:"grace_until,omitempty"`
	// RenewalAnchor is the period_end whose renewal was last attempted
	// (internal idempotency anchor; not part of the API surface).
	RenewalAnchor     *time.Time `json:"-"`
	LastInvoiceID     *uuid.UUID `json:"last_invoice_id,omitempty"`
	LastJobID         *uuid.UUID `json:"last_job_id,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	ProvisionAttempts int        `json:"provision_attempts"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ProductName       string     `json:"product_name,omitempty"`
	PlanName          string     `json:"plan_name,omitempty"`
}

// Customer is the billing profile of an organization (Phase 2 table).
type Customer struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"organization_id"`
	Currency string    `json:"currency"`
}

// PaymentMethod is the org's payment instrument on file. The token is
// stored secretbox-sealed (TokenEnc) and is NEVER returned by any API.
type PaymentMethod struct {
	Provider string `json:"provider"`
	TokenEnc string `json:"token_enc,omitempty"` // ciphertext at rest
	Brand    string `json:"brand,omitempty"`
	Last4    string `json:"last4,omitempty"`
}

// Store persists billing rows.
type Store struct{ Pool *pgxpool.Pool }

// ---------------------------------------------------------------------------
// customer
// ---------------------------------------------------------------------------

// EnsureCustomer returns the org's billing profile, creating it lazily on
// first purchase (Phase 2 created the table; one row per organization).
func (s *Store) EnsureCustomer(ctx context.Context, orgID uuid.UUID, currency string) (*Customer, error) {
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO customers (organization_id, currency)
		VALUES ($1, $2)
		ON CONFLICT (organization_id) DO NOTHING
	`, orgID, currency); err != nil {
		return nil, err
	}
	var c Customer
	err := s.Pool.QueryRow(ctx, `
		SELECT id, organization_id, currency FROM customers WHERE organization_id = $1
	`, orgID).Scan(&c.ID, &c.OrgID, &c.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetPaymentMethod stores the org's instrument (token sealed at rest).
func (s *Store) SetPaymentMethod(ctx context.Context, orgID uuid.UUID, pm PaymentMethod) error {
	raw, err := json.Marshal(pm)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE customers SET payment_method = $2, updated_at = now() WHERE organization_id = $1`, orgID, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PaymentMethodRaw returns the stored JSON (TokenEnc included — internal
// callers only; API handlers project it to the masked shape).
func (s *Store) PaymentMethodRaw(ctx context.Context, orgID uuid.UUID) (*PaymentMethod, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT payment_method FROM customers WHERE organization_id = $1`, orgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var pm PaymentMethod
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &pm); err != nil {
			return nil, err
		}
	}
	return &pm, nil
}

// ---------------------------------------------------------------------------
// orders
// ---------------------------------------------------------------------------

// OrderInput is a new order.
type OrderInput struct {
	OrgID             uuid.UUID
	ProductID         uuid.UUID
	PlanID            *uuid.UUID
	Period            string
	AmountMinor       int64
	Currency          string
	Provider          string
	Metadata          map[string]any
	SubscriptionState State // initial machine state (PENDING)
	WorkloadKind      string
	// PeriodStart is injected by the service clock (tests inject a fake now).
	PeriodStart time.Time
}

const orderCols = `id, organization_id, subscription_id, product_id, plan_id, status,
	billing_period, amount_minor, currency, provider, provider_ref, paid_at, metadata,
	created_at, updated_at`

func scanOrder(row pgx.Row) (*Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.OrgID, &o.SubscriptionID, &o.ProductID, &o.PlanID, &o.Status,
		&o.BillingPeriod, &o.AmountMinor, &o.Currency, &o.Provider, &o.ProviderRef,
		&o.PaidAt, &o.Metadata, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// CreateOrderWithSubscription inserts the order and its subscription in one
// transaction (the order owns the subscription it will activate).
func (s *Store) CreateOrderWithSubscription(ctx context.Context, in OrderInput) (*Order, *Subscription, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	sub, err := createSubscription(ctx, tx, in)
	if err != nil {
		return nil, nil, err
	}
	meta := []byte("{}")
	if in.Metadata != nil {
		if meta, err = json.Marshal(in.Metadata); err != nil {
			return nil, nil, err
		}
	}
	var o Order
	err = tx.QueryRow(ctx, `
		INSERT INTO billing_orders
			(organization_id, subscription_id, product_id, plan_id, billing_period, currency, amount_minor, provider, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING `+orderCols,
		in.OrgID, sub.ID, in.ProductID, in.PlanID, in.Period, in.Currency, in.AmountMinor, in.Provider, meta,
	).Scan(&o.ID, &o.OrgID, &o.SubscriptionID, &o.ProductID, &o.PlanID, &o.Status,
		&o.BillingPeriod, &o.AmountMinor, &o.Currency, &o.Provider, &o.ProviderRef,
		&o.PaidAt, &o.Metadata, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &o, sub, nil
}

func createSubscription(ctx context.Context, tx pgx.Tx, in OrderInput) (*Subscription, error) {
	// The customer row must exist (EnsureCustomer runs before this in the
	// service flow); resolve it org-scoped.
	var customerID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM customers WHERE organization_id = $1`, in.OrgID).Scan(&customerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("billing customer profile missing; ensure it before ordering")
		}
		return nil, err
	}
	periodStart := in.PeriodStart
	if periodStart.IsZero() {
		periodStart = time.Now().UTC()
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO subscriptions
			(customer_id, plan_id, product_id, status, provision_state, billing_period,
			 period_start, period_end, state_changed_at, workload_kind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),$9)
		RETURNING id, customer_id, plan_id, product_id, status, provision_state,
			billing_period, period_start, period_end, cancel_at_period_end,
			state_changed_at, workload_kind, website_id,
			grace_until, last_renewal_period_end, last_invoice_id, last_job_id,
			last_error, provision_attempts, created_at, updated_at
	`,
		customerID, in.PlanID, in.ProductID, LegacyStatus(in.SubscriptionState),
		string(in.SubscriptionState), in.Period,
		periodStart, AddPeriod(periodStart, in.Period), in.WorkloadKind,
	)
	var sub Subscription
	if err := row.Scan(&sub.ID, &sub.CustomerID, &sub.PlanID, &sub.ProductID, &sub.Status,
		&sub.ProvisionState, &sub.BillingPeriod, &sub.PeriodStart, &sub.PeriodEnd, &sub.CancelAtPeriodEnd,
		&sub.StateChangedAt, &sub.WorkloadKind, &sub.WebsiteID,
		&sub.GraceUntil, &sub.RenewalAnchor, &sub.LastInvoiceID, &sub.LastJobID, &sub.LastError,
		&sub.ProvisionAttempts, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT organization_id FROM customers WHERE id = $1`, sub.CustomerID).Scan(&sub.OrgID); err != nil {
		return nil, err
	}
	return &sub, nil
}

// GetOrder resolves an order org-scoped (cross-tenant = not found).
func (s *Store) GetOrder(ctx context.Context, orgID, orderID uuid.UUID) (*Order, error) {
	return scanOrder(s.Pool.QueryRow(ctx, `SELECT `+orderCols+` FROM billing_orders WHERE id = $1 AND organization_id = $2`, orderID, orgID))
}

// GetOrderAny resolves an order without org scope (webhook path; the webhook
// already proved possession of the gateway secret).
func (s *Store) GetOrderAny(ctx context.Context, orderID uuid.UUID) (*Order, error) {
	return scanOrder(s.Pool.QueryRow(ctx, `SELECT `+orderCols+` FROM billing_orders WHERE id = $1`, orderID))
}

// ListOrdersForOrg lists the org's orders (newest first).
func (s *Store) ListOrdersForOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]Order, error) {
	return s.listOrders(ctx, `SELECT `+orderColsList()+`, COALESCE(p.name,'')
		FROM billing_orders o LEFT JOIN products p ON p.id = o.product_id
		WHERE o.organization_id = $1 ORDER BY o.created_at DESC LIMIT $2`, orgID, limit)
}

type orderJoinScan struct {
	order *Order
}

func orderColsList() string {
	return `o.id, o.organization_id, o.subscription_id, o.product_id, o.plan_id, o.status,
	o.billing_period, o.amount_minor, o.currency, o.provider, o.provider_ref, o.paid_at, o.metadata,
	o.created_at, o.updated_at`
}

func (s *Store) listOrders(ctx context.Context, query string, args ...any) ([]Order, error) {
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		var product string
		if err := rows.Scan(&o.ID, &o.OrgID, &o.SubscriptionID, &o.ProductID, &o.PlanID, &o.Status,
			&o.BillingPeriod, &o.AmountMinor, &o.Currency, &o.Provider, &o.ProviderRef,
			&o.PaidAt, &o.Metadata, &o.CreatedAt, &o.UpdatedAt, &product); err != nil {
			return nil, err
		}
		o.ProductName = product
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkOrderPaid flips the order to paid exactly once (guarded UPDATE).
func (s *Store) MarkOrderPaid(ctx context.Context, orderID uuid.UUID, providerRef string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE billing_orders SET status = 'paid', provider_ref = $2, paid_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'pending'
	`, orderID, providerRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvoiceState // already paid/failed/cancelled
	}
	return nil
}

// CancelOrder voids a pending order (customer withdrew before paying).
func (s *Store) CancelOrder(ctx context.Context, orgID, orderID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE billing_orders SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND organization_id = $2 AND status = 'pending'
	`, orderID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// payments
// ---------------------------------------------------------------------------

// PaymentInput is one payment row to record.
type PaymentInput struct {
	OrgID           uuid.UUID
	OrderID         *uuid.UUID
	InvoiceID       *uuid.UUID
	SubscriptionID  *uuid.UUID
	Provider        string
	ProviderRef     string
	ProviderEventID string
	Kind            string
	Status          string
	Amount          Money
	FailureReason   string
}

// RecordPayment inserts a payment row. The (provider, provider_event_id)
// unique index makes replays no-ops: when replayed is true the row existed
// and NO side effect may run again.
func (s *Store) RecordPayment(ctx context.Context, in PaymentInput) (*Payment, bool, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO billing_payments
			(organization_id, order_id, invoice_id, subscription_id, provider, provider_ref,
			 provider_event_id, kind, status, amount_minor, currency, failure_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (provider, provider_event_id) WHERE provider_event_id <> '' DO NOTHING
		RETURNING id
	`, in.OrgID, in.OrderID, in.InvoiceID, in.SubscriptionID, in.Provider, in.ProviderRef,
		in.ProviderEventID, in.Kind, in.Status, in.Amount.Amount, in.Amount.Currency, in.FailureReason)
	var id uuid.UUID
	err := row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Replay: fetch the existing row for the response.
		p, gerr := s.paymentByEvent(ctx, in.Provider, in.ProviderEventID)
		if gerr != nil {
			return nil, false, gerr
		}
		return p, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	p, err := s.GetPayment(ctx, id)
	return p, false, err
}

func (s *Store) paymentByEvent(ctx context.Context, provider, eventID string) (*Payment, error) {
	return scanPayment(s.Pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM billing_payments
		WHERE provider = $1 AND provider_event_id = $2`, provider, eventID))
}

const paymentCols = `id, organization_id, order_id, invoice_id, subscription_id, provider,
	provider_ref, provider_event_id, kind, status, amount_minor, currency, failure_reason, created_at`

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.OrgID, &p.OrderID, &p.InvoiceID, &p.SubscriptionID, &p.Provider,
		&p.ProviderRef, &p.ProviderEventID, &p.Kind, &p.Status, &p.AmountMinor, &p.Currency,
		&p.FailureReason, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPayment resolves one payment row.
func (s *Store) GetPayment(ctx context.Context, id uuid.UUID) (*Payment, error) {
	return scanPayment(s.Pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM billing_payments WHERE id = $1`, id))
}

// ListPaymentsForOrg lists the org's payments (newest first).
func (s *Store) ListPaymentsForOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]Payment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+paymentCols+` FROM billing_payments
		WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// invoices
// ---------------------------------------------------------------------------

// InvoiceInput is a new invoice.
type InvoiceInput struct {
	CustomerID     uuid.UUID
	OrgID          uuid.UUID
	SubscriptionID *uuid.UUID
	Kind           string
	Currency       string
	SubtotalMinor  int64
	TaxMinor       int64
	LineItems      []LineItem
	DueIn          time.Duration
}

// LineItem is one invoice line.
type LineItem struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
	UnitMinor   int64  `json:"unit_minor"`
	TotalMinor  int64  `json:"total_minor"`
}

const invoiceCols = `i.id, i.customer_id, c.organization_id, i.number, i.status, i.currency,
	i.subtotal_cents, i.tax_cents, i.total_cents, i.line_items, i.kind, i.subscription_id,
	i.issued_at, i.due_at, i.paid_at, i.created_at`

func scanInvoice(row pgx.Row) (*Invoice, error) {
	var inv Invoice
	err := row.Scan(&inv.ID, &inv.CustomerID, &inv.OrgID, &inv.Number, &inv.Status, &inv.Currency,
		&inv.SubtotalMinor, &inv.TaxMinor, &inv.TotalMinor, &inv.LineItems, &inv.Kind,
		&inv.SubscriptionID, &inv.IssuedAt, &inv.DueAt, &inv.PaidAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// invoiceRawCols is the RETURNING list for INSERT (no aliases).
const invoiceRawCols = "id, customer_id, number, status, currency, subtotal_cents, tax_cents, total_cents, line_items, kind, subscription_id, issued_at, due_at, paid_at, created_at"

// IssueInvoice creates + issues an invoice (status open, issued_at now).
// Once issued the money fields are IMMUTABLE (store guard + DB trigger).
func (s *Store) IssueInvoice(ctx context.Context, in InvoiceInput) (*Invoice, error) {
	if err := ValidateCurrency(in.Currency); err != nil {
		return nil, err
	}
	if err := ValidateMinorUnit(in.SubtotalMinor); err != nil {
		return nil, err
	}
	items := in.LineItems
	if items == nil {
		items = []LineItem{}
	}
	lines, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	total := in.SubtotalMinor + in.TaxMinor
	var dueAt any
	if in.DueIn > 0 {
		dueAt = time.Now().UTC().Add(in.DueIn)
	}
	var inv Invoice
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO invoices
			(customer_id, number, status, currency, subtotal_cents, tax_cents, total_cents,
			 line_items, kind, subscription_id, issued_at, due_at)
		VALUES ($1, $2, 'open', $3, $4, $5, $6, $7, $8, $9, now(), $10)
		RETURNING `+invoiceRawCols,
		in.CustomerID, s.nextInvoiceNumber(ctx), in.Currency, in.SubtotalMinor, in.TaxMinor, total,
		lines, in.Kind, in.SubscriptionID, dueAt,
	).Scan(&inv.ID, &inv.CustomerID, &inv.Number, &inv.Status, &inv.Currency,
		&inv.SubtotalMinor, &inv.TaxMinor, &inv.TotalMinor, &inv.LineItems, &inv.Kind,
		&inv.SubscriptionID, &inv.IssuedAt, &inv.DueAt, &inv.PaidAt, &inv.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Resolve the org id (the INSERT cannot see the join alias).
	var orgID uuid.UUID
	if err := s.Pool.QueryRow(ctx, "SELECT organization_id FROM customers WHERE id = $1", inv.CustomerID).Scan(&orgID); err != nil {
		return nil, err
	}
	inv.OrgID = orgID
	return &inv, nil
}

// nextInvoiceNumber mints INV-<year>-<seq> from the DB sequence.
func (s *Store) nextInvoiceNumber(ctx context.Context) string {
	var seq int64
	_ = s.Pool.QueryRow(ctx, `SELECT nextval('billing_invoice_number_seq')`).Scan(&seq)
	return fmt.Sprintf("INV-%d-%06d", time.Now().UTC().Year(), seq)
}

// GetInvoice resolves an invoice org-scoped.
func (s *Store) GetInvoice(ctx context.Context, orgID, invoiceID uuid.UUID) (*Invoice, error) {
	return scanInvoice(s.Pool.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i
		JOIN customers c ON c.id = i.customer_id
		WHERE i.id = $1 AND c.organization_id = $2`, invoiceID, orgID))
}

// GetInvoiceAny resolves an invoice without org scope (admin/webhook paths).
func (s *Store) GetInvoiceAny(ctx context.Context, invoiceID uuid.UUID) (*Invoice, error) {
	return scanInvoice(s.Pool.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i
		JOIN customers c ON c.id = i.customer_id WHERE i.id = $1`, invoiceID))
}

// ListInvoicesForOrg lists the org's invoices.
func (s *Store) ListInvoicesForOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]Invoice, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+invoiceCols+` FROM invoices i
		JOIN customers c ON c.id = i.customer_id
		WHERE c.organization_id = $1 ORDER BY i.created_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// ListInvoicesAdmin lists invoices across orgs (optional org + status filter).
func (s *Store) ListInvoicesAdmin(ctx context.Context, orgID *uuid.UUID, status string, limit int) ([]Invoice, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+invoiceCols+` FROM invoices i
		JOIN customers c ON c.id = i.customer_id
		WHERE ($1::uuid IS NULL OR c.organization_id = $1)
		  AND ($2 = '' OR i.status = $2)
		ORDER BY i.created_at DESC LIMIT $3`, orgID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// MarkInvoicePaid marks an OPEN invoice paid (status transition only — the
// money fields stay untouched: invoices are immutable once issued).
func (s *Store) MarkInvoicePaid(ctx context.Context, invoiceID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE invoices SET status = 'paid', paid_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'open'
	`, invoiceID)
	if err != nil {
		if isImmutableViolation(err) {
			return ErrInvoiceImmutable
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvoiceState
	}
	return nil
}

// VoidInvoice voids an OPEN invoice (issued, unpaid). Paid invoices are not
// voidable (they are refundable, out of scope for this panel build).
func (s *Store) VoidInvoice(ctx context.Context, invoiceID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE invoices SET status = 'void', updated_at = now()
		WHERE id = $1 AND status = 'open'
	`, invoiceID)
	if err != nil {
		if isImmutableViolation(err) {
			return ErrInvoiceImmutable
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvoiceState
	}
	return nil
}

// isImmutableViolation maps the 0030 trigger error to ErrInvoiceImmutable.
func isImmutableViolation(err error) bool {
	return err != nil && containsString(err.Error(), "invoices are immutable once issued")
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// subscriptions
// ---------------------------------------------------------------------------

// subRawCols is the RETURNING list for INSERT/UPDATE (no join aliases;
// names are resolved by a follow-up scoped read).
const subRawCols = "id, customer_id, plan_id, product_id, status, provision_state, billing_period, period_start, period_end, cancel_at_period_end, state_changed_at, workload_kind, website_id, grace_until, last_renewal_period_end, last_invoice_id, last_job_id, last_error, provision_attempts, created_at, updated_at"

const subCols = `s.id, s.customer_id, c.organization_id, s.plan_id, s.product_id, s.status,
	s.provision_state, s.billing_period, s.period_start, s.period_end, s.cancel_at_period_end,
	s.state_changed_at, s.workload_kind, s.website_id,
	s.grace_until, s.last_renewal_period_end, s.last_invoice_id, s.last_job_id, s.last_error,
	s.provision_attempts, s.created_at, s.updated_at, COALESCE(p.name,''), COALESCE(hp.name,'')`

const subFrom = ` FROM subscriptions s
	JOIN customers c ON c.id = s.customer_id
	LEFT JOIN products p ON p.id = s.product_id
	LEFT JOIN hosting_packages hp ON hp.id = s.plan_id`

func scanSubscription(row pgx.Row) (*Subscription, error) {
	var sub Subscription
	err := row.Scan(&sub.ID, &sub.CustomerID, &sub.OrgID, &sub.PlanID, &sub.ProductID, &sub.Status,
		&sub.ProvisionState, &sub.BillingPeriod, &sub.PeriodStart, &sub.PeriodEnd, &sub.CancelAtPeriodEnd,
		&sub.StateChangedAt, &sub.WorkloadKind, &sub.WebsiteID,
		&sub.GraceUntil, &sub.RenewalAnchor, &sub.LastInvoiceID, &sub.LastJobID, &sub.LastError,
		&sub.ProvisionAttempts, &sub.CreatedAt, &sub.UpdatedAt, &sub.ProductName, &sub.PlanName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// scanSubscriptionRaw scans the no-alias RETURNING list (22 cols).
func scanSubscriptionRaw(row pgx.Row) (*Subscription, error) {
	var sub Subscription
	err := row.Scan(&sub.ID, &sub.CustomerID, &sub.PlanID, &sub.ProductID, &sub.Status,
		&sub.ProvisionState, &sub.BillingPeriod, &sub.PeriodStart, &sub.PeriodEnd, &sub.CancelAtPeriodEnd,
		&sub.StateChangedAt, &sub.WorkloadKind, &sub.WebsiteID,
		&sub.GraceUntil, &sub.RenewalAnchor, &sub.LastInvoiceID, &sub.LastJobID, &sub.LastError,
		&sub.ProvisionAttempts, &sub.CreatedAt, &sub.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// GetSubscription resolves a subscription org-scoped.
func (s *Store) GetSubscription(ctx context.Context, orgID, subID uuid.UUID) (*Subscription, error) {
	return scanSubscription(s.Pool.QueryRow(ctx, `SELECT `+subCols+subFrom+` WHERE s.id = $1 AND c.organization_id = $2`, subID, orgID))
}

// GetSubscriptionAny resolves a subscription without org scope (system paths).
func (s *Store) GetSubscriptionAny(ctx context.Context, subID uuid.UUID) (*Subscription, error) {
	return scanSubscription(s.Pool.QueryRow(ctx, `SELECT `+subCols+subFrom+` WHERE s.id = $1`, subID))
}

// ListSubscriptionsForOrg lists the org's subscriptions.
func (s *Store) ListSubscriptionsForOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]Subscription, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE c.organization_id = $1 ORDER BY s.created_at DESC LIMIT $2`, orgID, limit)
}

// ListSubscriptionsAdmin lists subscriptions across orgs (optional state filter).
func (s *Store) ListSubscriptionsAdmin(ctx context.Context, state string, limit int) ([]Subscription, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE ($1 = '' OR s.provision_state::text = $1) ORDER BY s.created_at DESC LIMIT $2`, state, limit)
}

func (s *Store) listSubscriptions(ctx context.Context, query string, args ...any) ([]Subscription, error) {
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// TransitionOpts carries the optional side columns of a transition.
type TransitionOpts struct {
	SetGraceUntil *time.Time // nil = leave; non-nil = set (use &time.Time{} to clear? no: GraceClear)
	GraceClear    bool
	LastError     string
	// RequireFrom pins the optimistic guard (the machine edge source state).
	RequireFrom State
}

// UpdateSubscriptionState applies ONE guarded state move. The WHERE clause
// carries the optimistic guard: two racing transitions cannot both win; the
// loser reloads and either observes the same target (idempotent) or reports
// the illegal edge. This is the ONLY place provision_state is written.
func (s *Store) UpdateSubscriptionState(ctx context.Context, subID uuid.UUID, from, to State, opts TransitionOpts) (*Subscription, error) {
	args := []any{subID, string(from), string(to)}
	graceExpr := `grace_until` // leave unchanged by default
	if opts.GraceClear {
		graceExpr = fmt.Sprintf(`$%d`, len(args)+1)
		args = append(args, nil)
	} else if opts.SetGraceUntil != nil {
		graceExpr = fmt.Sprintf(`$%d`, len(args)+1)
		args = append(args, *opts.SetGraceUntil)
	}
	legacyExpr := fmt.Sprintf(`$%d`, len(args)+1)
	args = append(args, LegacyStatus(to))
	errExpr := fmt.Sprintf(`$%d`, len(args)+1)
	args = append(args, opts.LastError)

	sub, err := scanSubscriptionRaw(s.Pool.QueryRow(ctx, `
		UPDATE subscriptions SET
			provision_state = $3,
			status = `+legacyExpr+`,
			state_changed_at = now(),
			grace_until = `+graceExpr+`,
			last_error = `+errExpr+`,
			updated_at = now()
		WHERE id = $1 AND provision_state = $2
		RETURNING `+subRawCols,
		args...,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the race or the edge was already taken: reload to
		// disambiguate idempotent-replay from a genuinely illegal move.
		cur, gerr := s.GetSubscriptionAny(ctx, subID)
		if gerr != nil {
			return nil, ErrNotFound
		}
		if cur.ProvisionState == to {
			return cur, nil // someone else already moved it — idempotent success
		}
		return nil, &TransitionError{From: cur.ProvisionState, To: to, Reason: "concurrent update lost"}
	}
	if err != nil {
		return nil, err
	}
	// Reload with the joined projections (product/plan names).
	full, ferr := s.GetSubscriptionAny(ctx, subID)
	if ferr != nil {
		return sub, nil // the guarded move succeeded; names are cosmetic
	}
	return full, nil
}

// SetSubscriptionWorkload records the provisioned workload ref + the job
// that carries the state-changing work (convergence reads last_job_id).
func (s *Store) SetSubscriptionWorkload(ctx context.Context, subID uuid.UUID, kind string, websiteID, jobID *uuid.UUID, attempts int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE subscriptions SET workload_kind = $2, website_id = $3,
			last_job_id = $4, provision_attempts = $5, updated_at = now()
		WHERE id = $1
	`, subID, kind, websiteID, jobID, attempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscriptionJob records the latest state-changing job (suspend/
// terminate steps update it as the walk advances).
func (s *Store) SetSubscriptionJob(ctx context.Context, subID, jobID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE subscriptions SET last_job_id = $2, updated_at = now() WHERE id = $1`, subID, jobID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ExtendPeriod advances the billing window by one period (period_start
// becomes the previous period_end — no time is ever lost).
func (s *Store) ExtendPeriod(ctx context.Context, subID uuid.UUID) (*Subscription, error) {
	var sub Subscription
	err := s.Pool.QueryRow(ctx, `
		UPDATE subscriptions SET
			period_start = period_end,
			period_end = period_end + (CASE billing_period
				WHEN 'quarterly' THEN interval '3 months'
				WHEN 'yearly' THEN interval '1 year'
				ELSE interval '1 month' END),
			grace_until = NULL,
			updated_at = now()
		WHERE id = $1
		RETURNING customer_id, plan_id, product_id, status, provision_state,
			billing_period, period_start, period_end, cancel_at_period_end,
			state_changed_at, workload_kind, website_id,
			grace_until, last_renewal_period_end, last_invoice_id, last_job_id,
			last_error, provision_attempts, created_at, updated_at`, subID).Scan(&sub.CustomerID, &sub.PlanID, &sub.ProductID, &sub.Status,
		&sub.ProvisionState, &sub.BillingPeriod, &sub.PeriodStart, &sub.PeriodEnd, &sub.CancelAtPeriodEnd,
		&sub.StateChangedAt, &sub.WorkloadKind, &sub.WebsiteID,
		&sub.GraceUntil, &sub.RenewalAnchor, &sub.LastInvoiceID, &sub.LastJobID, &sub.LastError,
		&sub.ProvisionAttempts, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sub.ID = subID
	if err := s.Pool.QueryRow(ctx, `SELECT organization_id FROM customers WHERE id = $1`, sub.CustomerID).Scan(&sub.OrgID); err != nil {
		return nil, err
	}
	return &sub, nil
}

// SetSubscriptionGrace pins/clears the grace deadline (failed renewal).
func (s *Store) SetSubscriptionGrace(ctx context.Context, subID uuid.UUID, until *time.Time) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE subscriptions SET grace_until = $2, updated_at = now() WHERE id = $1`, subID, until)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscriptionRenewalAnchor pins the period_end whose renewal was
// invoiced/attempted (idempotency: one renewal per period, wall-clock-free).
func (s *Store) SetSubscriptionRenewalAnchor(ctx context.Context, subID uuid.UUID, periodEnd time.Time) error {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE subscriptions SET last_renewal_period_end = $2, updated_at = now() WHERE id = $1`,
		subID, periodEnd)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscriptionCancelFlag sets cancel_at_period_end.
func (s *Store) SetSubscriptionCancelFlag(ctx context.Context, orgID, subID uuid.UUID, cancel bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE subscriptions s SET cancel_at_period_end = $3, updated_at = now()
		FROM customers c
		WHERE s.customer_id = c.id AND s.id = $1 AND c.organization_id = $2
	`, subID, orgID, cancel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DueForRenewal returns ACTIVE subscriptions whose period ended and whose
// renewal has not been invoiced yet (renewal -> payment -> extend).
func (s *Store) DueForRenewal(ctx context.Context, now time.Time, limit int) ([]Subscription, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.provision_state = 'ACTIVE' AND s.cancel_at_period_end = FALSE
		  AND s.period_end <= $1
		  AND (s.last_renewal_period_end IS NULL OR s.last_renewal_period_end < s.period_end)
		ORDER BY s.period_end ASC LIMIT $2`, now, limit)
}

// CancelledDueForTerminate returns ACTIVE subscriptions that were cancelled
// at period end and whose period has now ended (walk to TERMINATING).
func (s *Store) CancelledDueForTerminate(ctx context.Context, now time.Time, limit int) ([]Subscription, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.provision_state = 'ACTIVE' AND s.cancel_at_period_end = TRUE
		  AND s.period_end <= $1
		ORDER BY s.period_end ASC LIMIT $2`, now, limit)
}

// InGraceExpired returns ACTIVE subscriptions whose grace window elapsed
// (grace -> suspend).
func (s *Store) InGraceExpired(ctx context.Context, now time.Time, limit int) ([]Subscription, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.provision_state = 'ACTIVE' AND s.grace_until IS NOT NULL AND s.grace_until <= $1
		ORDER BY s.grace_until ASC LIMIT $2`, now, limit)
}

// SuspendedDueForTerminate returns SUSPENDED subscriptions past the
// terminate-after window (suspend -> terminate).
func (s *Store) SuspendedDueForTerminate(ctx context.Context, now time.Time, after time.Duration, limit int) ([]Subscription, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.provision_state = 'SUSPENDED' AND s.state_changed_at <= $1
		ORDER BY s.state_changed_at ASC LIMIT $2`, now.Add(-after), limit)
}

// ListInState returns subscriptions currently in one machine state (the
// convergence loop reads their last_job_id).
func (s *Store) ListInState(ctx context.Context, state State, limit int) ([]Subscription, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listSubscriptions(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.provision_state = $1 ORDER BY s.state_changed_at ASC LIMIT $2`, string(state), limit)
}

// ---------------------------------------------------------------------------
// products (plans <-> products)
// ---------------------------------------------------------------------------

const productCols = `p.id, p.name, p.type, p.description, p.plan_id, COALESCE(hp.name,''),
	COALESCE(hp.kind,''), p.price_cents, p.currency, p.active, p.config`

func scanProduct(row pgx.Row) (*Product, error) {
	var pr Product
	err := row.Scan(&pr.ID, &pr.Name, &pr.Type, &pr.Description, &pr.PlanID, &pr.PlanName,
		&pr.PlanKind, &pr.PriceMinor, &pr.Currency, &pr.Active, &pr.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetProduct resolves one product.
func (s *Store) GetProduct(ctx context.Context, productID uuid.UUID) (*Product, error) {
	return scanProduct(s.Pool.QueryRow(ctx, `SELECT `+productCols+`
		FROM products p LEFT JOIN hosting_packages hp ON hp.id = p.plan_id
		WHERE p.id = $1`, productID))
}

// ListProducts lists products (activeOnly for the customer price list).
func (s *Store) ListProducts(ctx context.Context, activeOnly bool) ([]Product, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+productCols+`
		FROM products p LEFT JOIN hosting_packages hp ON hp.id = p.plan_id
		WHERE (NOT $1 OR p.active) ORDER BY p.name ASC`, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		pr, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pr)
	}
	return out, rows.Err()
}

// ProductInput creates/updates a product (admin).
type ProductInput struct {
	Name        string
	Type        string
	Description string
	PlanID      *uuid.UUID
	PriceMinor  int64
	Currency    string
	Active      bool
	Config      map[string]any
}

// CreateProduct inserts a product.
func (s *Store) CreateProduct(ctx context.Context, in ProductInput) (*Product, error) {
	cfg, err := json.Marshal(orEmptyMap(in.Config))
	if err != nil {
		return nil, err
	}
	var id uuid.UUID
	if err := s.Pool.QueryRow(ctx, `
		INSERT INTO products (name, type, description, plan_id, price_cents, currency, active, config)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id`,
		in.Name, in.Type, in.Description, in.PlanID, in.PriceMinor, in.Currency, in.Active, cfg).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetProduct(ctx, id)
}

// UpdateProduct updates price/plan/description/active (name+type are the
// identity pair and stay fixed).
func (s *Store) UpdateProduct(ctx context.Context, productID uuid.UUID, in ProductInput) (*Product, error) {
	cfg, err := json.Marshal(orEmptyMap(in.Config))
	if err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE products SET description = $2, plan_id = $3, price_cents = $4,
			currency = $5, active = $6, config = $7, updated_at = now()
		WHERE id = $1`,
		productID, in.Description, in.PlanID, in.PriceMinor, in.Currency, in.Active, cfg); err != nil {
		return nil, err
	}
	return s.GetProduct(ctx, productID)
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// PlanRow is the hosting_packages projection billing needs (Phase 9 matrix
// summary for the WHM plans view).
type PlanRow struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	PriceMinor     int64     `json:"price_minor"`
	MaxWebsites    int       `json:"max_websites"`
	MaxDatabases   int       `json:"max_databases"`
	MaxDiskMB      int64     `json:"max_disk_mb"`
	MemoryLimitMB  int64     `json:"memory_limit_mb"`
	CPUCores       float64   `json:"cpu_cores"`
	MaxBandwidthMB int64     `json:"max_bandwidth_mb"`
	MaxPorts       int       `json:"max_ports"`
	MaxBackups     int       `json:"max_backups"`
	Default        bool      `json:"is_default"`
	ProductCount   int       `json:"product_count"`
}

// ListPlans returns the plan matrix + how many products link to each plan.
func (s *Store) ListPlans(ctx context.Context) ([]PlanRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT hp.id, hp.name, hp.kind, COALESCE(hp.price_monthly_cents,0),
			hp.max_websites, hp.max_databases, hp.max_disk_mb, hp.memory_limit_mb,
			hp.cpu_cores, hp.max_bandwidth_mb, hp.max_ports, hp.max_backups, hp.is_default,
			(SELECT count(*) FROM products p WHERE p.plan_id = hp.id)
		FROM hosting_packages hp ORDER BY hp.kind, hp.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanRow
	for rows.Next() {
		var p PlanRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.PriceMinor, &p.MaxWebsites, &p.MaxDatabases,
			&p.MaxDiskMB, &p.MemoryLimitMB, &p.CPUCores, &p.MaxBandwidthMB, &p.MaxPorts,
			&p.MaxBackups, &p.Default, &p.ProductCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
