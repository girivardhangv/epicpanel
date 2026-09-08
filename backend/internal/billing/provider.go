// Payment gateway abstraction (Phase 10). The panel ships with a
// deterministic FakeProvider (tests + demo checkouts) and a ManualProvider
// (offline payments recorded by an operator) — no real gateway is bundled
// and none is hardcoded: gateways plug into the PaymentProvider interface
// and register themselves in the provider registry.
//
// Webhook contract: verify FIRST (signature over the raw body), then parse,
// then emit an event, then transition. Client-side "paid" claims are never
// trusted; the webhook event id is the idempotency key so replays collapse.
package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// ChargeRequest is one authorization attempt against a payment method.
type ChargeRequest struct {
	Amount      Money   `json:"amount"`
	Description string  `json:"description"`
	MethodRef   string  `json:"method_ref"` // gateway token of the payment method
	OrderRef    string  `json:"order_ref"`  // panel-side reference (order id)
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Money is the minor-unit + currency money value.
type Money struct {
	Amount   int64  `json:"amount_minor"`
	Currency string `json:"currency"`
}

// Validate checks the money value (non-negative amount, valid currency).
func (m Money) Validate() error {
	if err := ValidateCurrency(m.Currency); err != nil {
		return err
	}
	return ValidateMinorUnit(m.Amount)
}

func (m Money) String() string { return FormatMinor(m.Amount, m.Currency) }

// Authorization is the result of Authorize.
type Authorization struct {
	Ref      string `json:"ref"` // gateway authorization reference
	Declined bool   `json:"declined"`
	Reason   string `json:"reason,omitempty"`
}

// Capture is the result of Capture.
type Capture struct {
	Ref    string `json:"ref"`
	Status string `json:"status"` // captured
}

// PaymentProvider is the pluggable gateway seam (authorize / capture /
// refund / webhook — the four operations named by the phase contract).
type PaymentProvider interface {
	// Name is the registry key (also the webhook URL suffix).
	Name() string
	// Authorize places a hold for the charge.
	Authorize(ctx context.Context, req ChargeRequest) (Authorization, error)
	// Capture completes a prior authorization (money moves).
	Capture(ctx context.Context, authRef string) (Capture, error)
	// Refund reverses a captured payment (full or partial amount).
	Refund(ctx context.Context, captureRef string, amount Money, reason string) error
	// VerifyWebhook validates the raw webhook request and returns the
	// canonical event. Any error means UNTRUSTED — the caller must reject.
	VerifyWebhook(secret string, header http.Header, body []byte) (WebhookEvent, error)
}

// WebhookEvent is the canonical, verified webhook payload.
type WebhookEvent struct {
	EventID   string `json:"event_id"`            // provider idempotency key
	Type      string `json:"type"`                // payment.captured | payment.failed
	OrderRef  string `json:"order_ref"`           // panel order id (string form)
	Amount    Money  `json:"amount"`
	Reason    string `json:"reason,omitempty"`
	Signature string `json:"-"`
}

// Event types carried by verified webhooks.
const (
	WebhookPaymentCaptured = "payment.captured"
	WebhookPaymentFailed   = "payment.failed"
)

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

var (
	providersMu sync.RWMutex
	providers   = map[string]PaymentProvider{}
)

// RegisterProvider plugs a gateway into the panel. Later registrations for
// the same name replace earlier ones (tests rely on this).
func RegisterProvider(p PaymentProvider) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[p.Name()] = p
}

// ProviderFor resolves a registered gateway by name.
func ProviderFor(name string) (PaymentProvider, error) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("payment provider %q is not registered", name)
	}
	return p, nil
}

// ProviderNames lists registered gateways (stable order).
func ProviderNames() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// FakeProvider — deterministic gateway for tests and demo checkouts.
//
// Determinism rules (no randomness, no wall clock in outcomes):
//   - a charge is declined iff the method reference starts with "fail"
//     (or the amount is non-positive);
//   - references are derived hashes of the request inputs, so the same
//     request always produces the same authorization/capture refs;
//   - webhooks are HMAC-SHA256 over the raw body with the shared secret.
// ---------------------------------------------------------------------------

const fakeDeclinedPrefix = "fail"

// FakeProvider is the deterministic gateway.
type FakeProvider struct{}

// Name implements PaymentProvider.
func (FakeProvider) Name() string { return "fake" }

func fakeRef(prefix, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return prefix + hex.EncodeToString(sum[:])[:24]
}

// Authorize implements PaymentProvider.
func (FakeProvider) Authorize(_ context.Context, req ChargeRequest) (Authorization, error) {
	if err := req.Amount.Validate(); err != nil {
		return Authorization{}, err
	}
	if req.Amount.Amount <= 0 {
		return Authorization{Declined: true, Reason: "non-positive charge amount"}, nil
	}
	if strings.HasPrefix(strings.ToLower(req.MethodRef), fakeDeclinedPrefix) {
		return Authorization{Declined: true, Reason: "card declined (fake gateway: method_ref starts with 'fail')"}, nil
	}
	seed := req.MethodRef + "|" + req.OrderRef + "|" + fmt.Sprint(req.Amount.Amount) + "|" + req.Amount.Currency
	return Authorization{Ref: fakeRef("fakeauth_", seed)}, nil
}

// Capture implements PaymentProvider.
func (FakeProvider) Capture(_ context.Context, authRef string) (Capture, error) {
	if authRef == "" {
		return Capture{}, errors.New("empty authorization ref")
	}
	return Capture{Ref: fakeRef("fakecap_", authRef), Status: "captured"}, nil
}

// Refund implements PaymentProvider.
func (FakeProvider) Refund(_ context.Context, captureRef string, amount Money, _ string) error {
	if captureRef == "" {
		return errors.New("empty capture ref")
	}
	return amount.Validate()
}

// FakeWebhookHeaders are the headers the fake gateway sets on webhooks.
const (
	FakeSignatureHeader = "X-Fake-Signature"
	FakeEventIDHeader   = "X-Fake-Event-Id"
)

// SignPayload computes the webhook signature the fake gateway sends
// (HMAC-SHA256 hex over the raw body). Tests and the docs use this to craft
// valid webhooks without a real gateway.
func (FakeProvider) SignPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook implements PaymentProvider: constant-time HMAC comparison,
// then parse. Event id is mandatory (replay idempotency).
func (FakeProvider) VerifyWebhook(secret string, header http.Header, body []byte) (WebhookEvent, error) {
	got := header.Get(FakeSignatureHeader)
	if got == "" {
		return WebhookEvent{}, errors.New("missing " + FakeSignatureHeader + " header")
	}
	want := FakeProvider{}.SignPayload(secret, body)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return WebhookEvent{}, errors.New("webhook signature mismatch")
	}
	var raw struct {
		EventID    string         `json:"event_id"`
		Type       string         `json:"type"`
		OrderRef   string         `json:"order_ref"`
		AmountMinor int64         `json:"amount_minor"`
		Currency   string         `json:"currency"`
		Reason     string         `json:"reason"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("webhook body: %w", err)
	}
	if raw.EventID == "" {
		return WebhookEvent{}, errors.New("webhook event_id is required")
	}
	if raw.Type != WebhookPaymentCaptured && raw.Type != WebhookPaymentFailed {
		return WebhookEvent{}, fmt.Errorf("unsupported webhook type %q", raw.Type)
	}
	return WebhookEvent{
		EventID:  raw.EventID,
		Type:     raw.Type,
		OrderRef: raw.OrderRef,
		Amount:   Money{Amount: raw.AmountMinor, Currency: raw.Currency},
		Reason:   raw.Reason,
	}, nil
}

// ---------------------------------------------------------------------------
// ManualProvider — offline payments (bank transfer, cash) recorded by an
// operator. Authorize/Capture always succeed (the operator attests the money
// arrived); there are no webhooks.
// ---------------------------------------------------------------------------

// ManualProvider records offline payments.
type ManualProvider struct{}

// Name implements PaymentProvider.
func (ManualProvider) Name() string { return "manual" }

// Authorize implements PaymentProvider.
func (ManualProvider) Authorize(_ context.Context, req ChargeRequest) (Authorization, error) {
	if err := req.Amount.Validate(); err != nil {
		return Authorization{}, err
	}
	ref := req.MethodRef
	if ref == "" {
		ref = "offline"
	}
	return Authorization{Ref: "manualauth_" + fakeRef("", ref+req.OrderRef)[1:]}, nil
}

// Capture implements PaymentProvider.
func (ManualProvider) Capture(_ context.Context, authRef string) (Capture, error) {
	if authRef == "" {
		return Capture{}, errors.New("empty authorization ref")
	}
	return Capture{Ref: "manualcap_" + strings.TrimPrefix(authRef, "manualauth_"), Status: "captured"}, nil
}

// Refund implements PaymentProvider.
func (ManualProvider) Refund(_ context.Context, captureRef string, amount Money, _ string) error {
	if captureRef == "" {
		return errors.New("empty capture ref")
	}
	return amount.Validate()
}

// VerifyWebhook implements PaymentProvider — the manual gateway has none.
func (ManualProvider) VerifyWebhook(string, http.Header, []byte) (WebhookEvent, error) {
	return WebhookEvent{}, errors.New("the manual provider has no webhooks; record offline payments from the admin invoice view")
}

func init() {
	RegisterProvider(FakeProvider{})
	RegisterProvider(ManualProvider{})
}
