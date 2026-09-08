# Phase 10 Handoff — Billing & Provisioning (P10-BL)

Status: **COMPLETE** (billing core + API + scheduler + migration + WHM/customer UI + tests green; coordinator wiring listed below).

## Session Handoff (verbatim items from the phase file)

### Gateway(s) wired vs interface-only
- **Interface**: `billing.PaymentProvider` (backend/internal/billing/provider.go) exposes the four contract operations — `Authorize`, `Capture`, `Refund`, `VerifyWebhook` — plus a process registry (`RegisterProvider` / `ProviderFor` / `ProviderNames`). Nothing is hardcoded: the gateway is selected per order and by the stored payment instrument; `GET /admin/billing/settings` lists registered providers.
- **Wired (bundled, deterministic)**:
  - `FakeProvider` — deterministic fake for tests/demo: declines iff `method_ref` starts with `fail` or the amount is non-positive; refs are derived hashes (same input → same ref, proven by test); webhooks are HMAC-SHA256 over the raw body with a settings-backed secret (`billing.webhook_secret`, admin write-only). Ships with `SignPayload` so tests/docs can craft valid webhooks without a real gateway.
  - `ManualProvider` — offline payments recorded by an operator (authorize/capture always succeed, attested by the operator); **no webhooks by design** (`VerifyWebhook` errors, and the docs say so).
- **Not wired (honest gaps)**: no real gateway (Stripe/PayPal/...) — the panel has no live payment credentials. A real gateway is one file implementing the interface + one `RegisterProvider` call + one settings secret; zero core changes.

### Grace/suspend/terminate default timings
- **Grace**: `billing.grace_days` setting, **default 3 days** (contract default). On a failed renewal the subscription stays ACTIVE (workload keeps running) with `grace_until = now + grace`; the open renewal invoice is payable at any time (customer "Pay now" or admin manual path) — paying it extends the period and resumes a suspended workload (recovery path, `PayInvoice`).
- **Suspend**: at grace expiry the sweep transitions ACTIVE → SUSPENDING and enqueues the **existing** workload suspend jobs only — web = `suspend_website`, minecraft = `mc_stop`, discord = `bot_stop` (reversible; resume = `resume_website` / `bot_start` / `mc_start`). No new side channels.
- **Terminate**: `billing.suspend_terminate_days` setting, **default 7 days** after entering SUSPENDED. Then SUSPENDING→SUSPENDED→TERMINATING: **backup-first** — for websites a `create_backup` job is enqueued before the `delete_website` job (FIFO claim order enforces the sequence; Phase 11's dedicated `terminate_backup` hook job replaces this seam when it lands), for Minecraft `mc_backup_world` runs before `mc_delete`, for Discord bots there is **no backup job type yet** (P11 delivers it) so deletion proceeds directly — noted as an honest gap. TERMINATED is terminal; FAILED retains the row + workload refs + `last_error` for admin `retry`.

### State machine test coverage notes
- The 8 verbatim states are constants stored **exactly as written** (uppercase) in `subscriptions.provision_state` (`PENDING, PROVISIONING, ACTIVE, SUSPENDING, SUSPENDED, TERMINATING, TERMINATED, FAILED`); the Phase 2 `status` column is kept in sync as a legacy projection (`trialing/active/past_due/cancelled`).
- **ONE transition function** (`Service.Transition`): validates the edge against the `edges` map, applies a guarded optimistic `UPDATE … WHERE provision_state = $from` (replay-safe: same target reached concurrently = idempotent success), writes an audit entry and publishes a `billing.subscription.<state>` event on every move. Self-transitions are idempotent no-ops; all other unlisted edges are rejected **and audited with result=failure**.
- Legal-edge map: PENDING→{PROVISIONING,FAILED,TERMINATED}; PROVISIONING→{ACTIVE,FAILED}; ACTIVE→{SUSPENDING,TERMINATING}; SUSPENDING→{SUSPENDED,ACTIVE,FAILED}; SUSPENDED→{ACTIVE,TERMINATING}; TERMINATING→{TERMINATED,FAILED}; TERMINATED→{} (irreversible); FAILED→{PROVISIONING,SUSPENDING,TERMINATING,TERMINATED} (manual retry re-enters the interrupted action).
- Coverage: `TestIllegalTransitionsRejected` walks the full 8×8 state matrix against the invariants; `TestHappyPathEdges` + `TestReversibleSuspendEdges` prove the contract flows; `TestIllegalTransitionRejectedAndAudited` proves rejection + failure audit + no state move + idempotent replay; `TestGraceWalkSuspendTerminate` walks purchase→ACTIVE→grace→SUSPENDING→SUSPENDED→resume→renew→grace→suspend→TERMINATING→TERMINATED with **injected intervals** (1-day grace / 1-day terminate via settings, injected clock) and asserts the full audit trail; `TestCancelAtPeriodEndTerminates` covers the cancel path. Illegal moves surface as 409 through the admin retry/terminate endpoints.
- Replay idempotency: `TestWebhookReplayIsIdempotent` (duplicate provider event id → 1 payment, 1 invoice, 1 dispatch) and `TestHandleWebhookVerifiesBeforeTransition` (tampered signature rejected before any parse/use; replay flagged `replayed:true` with zero side effects) at service level, plus `TestBillingWebhookTamperReplay` through the HTTP endpoint.
- Invoice immutability: DB trigger (0030) blocks money-field UPDATEs once `issued_at` is set — proven in `TestInvoiceImmutableOnceIssued` (raw SQL tamper attempt fails with the trigger error; status transitions pay/void remain legal; paid invoices are not voidable). Store-level guards map the violation to `ErrInvoiceImmutable`.

### phases/README.md status row
Not edited (wave contract forbids edits during the parallel wave). Coordinator should set:
`| 10 | Billing + Provisioning | phase-10-billing-provisioning.md | DONE | Verbatim 8-state machine (single guarded transition fn + audit + events, replay-safe), orders/payments tables + Phase-2 entity extensions (0030), PaymentProvider interface w/ deterministic Fake + Manual gateways (none hardcoded), signature-verified webhook → event → transition w/ event-id replay idempotency, provision dispatch by product type → web/Minecraft/Discord engines using existing job types + Phase 9 plan gates/limits, renewal sweep → invoice → charge → extend, failure walk grace(3d default)→suspend(existing lifecycle jobs)→terminate(backup-first, irreversible), invoice immutability at DB-trigger level, WHM plans↔products/invoices/settings + customer subscriptions/invoices/payment-method UI, ADR-051/052 in handoff. Tests: 22 billing + 5 API integration suites on epicpanel_test_bl; full backend `-p 1` 17/17 pkgs ok. |`

## What shipped (files)

Backend core (`backend/internal/billing/`):
- `billing.go` — package contract; money = integer minor units + `[A-Z]{3}` currency (no floats anywhere); billing periods (monthly/quarterly/yearly → month multiples); **verbatim state machine**: State constants, the complete legal-edge map, `CanTransition`, `TransitionError`, legacy-status projection, event/audit action naming (`billing.*`).
- `store.go` — pgx store for Orders/Payments/Invoices/Subscriptions/Customers/Products/Plans: org-scoped reads (cross-tenant = not found), `CreateOrderWithSubscription` (one tx), `RecordPayment` with the `(provider, provider_event_id)` unique partial index (replay anchor), `IssueInvoice` (number from `billing_invoice_number_seq`, `INV-<year>-<seq>`), guarded `UpdateSubscriptionState` (THE state write), `ExtendPeriod`, renewal-anchor idempotency (`last_renewal_period_end` — period-boundary based, wall-clock-free), `DueForRenewal` / `InGraceExpired` / `SuspendedDueForTerminate` / `ListInState` scans, product CRUD + `ListPlans` (Phase 9 matrix + product counts).
- `provider.go` — `PaymentProvider` interface (authorize/capture/refund/webhook), provider registry, deterministic `FakeProvider` (+HMAC webhook verify) and `ManualProvider`.
- `service.go` — the business flows: `Checkout` (order + PENDING subscription, price re-resolved server-side), `CaptureForOrder` (authorize→capture→immutable invoice→order paid→dispatch; every step replay-guarded), `PayAndActivate`, `HandleWebhook` (**verify first**, then event on the bus, then transition), `Transition` (single guarded move + audit + event), `ApplyJobOutcome` (agent/job truth drives PROVISIONING→ACTIVE, SUSPENDING→SUSPENDED, TERMINATING→TERMINATED; terminal failures → FAILED with retained artifacts), `RunRenewals` (invoice→charge→extend; failure→grace; cancelled→terminate; grace expiry→suspend; suspended expiry→terminate), `PayInvoice` (recovery: pay→extend→resume), `BeginSuspend`/`ResumeBilling`/`BeginTerminate`, `ManualRetry`.
- `billing_test.go` (12 pure unit tests), `integration_test.go` (10 DB tests on `epicpanel_test_bl`).

API + scheduler (`backend/internal/api/`):
- `phase10_billing.go` — `registerPhase10(s, mux)` (coordinator adds one call line):
  - Customer (org-scoped via `ResolveOrg`; reads RoleBilling+, mutations RoleAdmin+): `GET/PATCH` overview, subscriptions list/get/cancel, products list, `POST orders` (checkout), `POST orders/{id}/pay`, invoices list/get/`pay`, payment-method get/put (**token stored secretbox-sealed; only brand/last4 ever returned; never in logs/audit**).
  - Admin (platform-admin session only, API tokens rejected — RBAC v2): plans list (Phase 9 matrix), products CRUD with **plan↔product link**, invoices list/issue/void, settings get/patch (grace days, suspend→terminate days, invoice due days, webhook secret write-only), subscriptions fleet view + `retry` (FAILED→provision/suspend/terminate) + `terminate` (admin force).
  - Webhook (public route, no session): `POST /v1/billing/webhook/{provider}` — refuses traffic when no secret is configured; unverified/malformed → 401 + audited rejection.
  - Provision dispatch by product type: `hosting`→`provisionWebAccount` (websites.Store + `provision_website` job), `minecraft`→`provisionMinecraft` (Phase 9 kind/port gates, port allocation, RCON sealed, `mc_install`), `discord`→`provisionDiscordBot` (Phase 9 kind/count gates, `bot_install` per the Phase 8 handoff). **All provision jobs carry the idempotency key `billingprovision-<subID>`**; suspend/resume/delete use `billingsuspend-`/`billingresume-`/`billingdelete-` + backup `billingtermbackup-`.
  - `applyProductPlan`: a purchase grants the linked plan — assigns `organizations.package_id` and re-enqueues `enforce_limits` for affected sites (Phase 9 convergence), which is what makes the Phase 7/8 kind/count gates pass after purchase.
- `scheduler_billing.go` — `startBillingLoops()` (once per process; `EPICPANEL_BILLING_LOOPS=1` disable knob, `EPICPANEL_DISABLE_BILLING_LOOPS` guard): boot pass + hourly renewal/failure walk (scheduler.go cadence) + 30s convergence loop (`billingConverge` advances transitional states from finished jobs — agent/job truth only; `billingStalledRecovery` re-dispatches lost provision jobs; `billingReconcileWorkloadTruth` terminates billing for out-of-band workload deletions).

Migration: `backend/migrations/0030_billing.sql` — `billing_orders`, `billing_payments` (+ replay-unique index), subscriptions extensions (`provision_state` with the verbatim CHECK, `state_changed_at`, `workload_kind`, `website_id`/`bot_id`/`instance_id` refs, `grace_until`, `last_renewal_period_end`, `last_invoice_id`, `last_job_id`, `last_error`, `provision_attempts`), products↔plans link + currency, invoices `subscription_id`/`kind` + **immutability trigger** + number sequence, customers `payment_method` JSONB, additive mc job-type enum values (idempotent `DO` blocks; re-runnable end-to-end).

Frontend:
- Customer (`frontend/apps/customer/`): `pages/billing/Billing.tsx` (subscriptions + state/renewal-status cards, product price list, order/checkout modal, payment-method write-only editor, cancel-at-period-end), `pages/billing/Invoices.tsx` (invoice history, pay-now for open invoices), `routes.billing.tsx` (`/billing`, `/billing/invoices`).
- Admin WHM (`frontend/apps/admin/`): `pages/billing/AdminBilling.tsx` (plans matrix + products + plan↔product linking), `pages/billing/AdminInvoices.tsx` (all-org invoice list/filter/issue/void), `pages/billing/AdminBillingSettings.tsx` (grace/suspend/due config, webhook secret, gateway registry), `routes.billing.tsx` (`/billing`, `/billing/invoices`, `/billing/settings`).

## Coordinator wiring (one-time, between waves)
1. `backend/internal/api/server.go` (coordinator-owned) — inside `Handler()`: `registerPhase10(s, mux)`.
2. Route mounting: customer `routes.billing.tsx` inside the apps/customer guarded shell + a "Billing" nav item; admin `routes.billing.tsx` inside the apps/admin shell + WHM nav items (Billing, Invoices, Settings).
3. `phases/README.md` — set the Phase 10 status row (text above).
4. `EPICPANEL.md` — append ADR-051/ADR-052 (below).
5. When Phase 11 lands: replace the website pre-terminate `create_backup` enqueue with the `terminate_backup` hook job and add the Discord-bot terminate backup job type — the seam is `billingTerminate` in `phase10_billing.go`.
6. Optional: add billing paths to `internal/api/openapi.json` (coordinator-owned; not touched).

## ADRs for the coordinator to record
```
ADR-051
Decision: Billing state machine lives in ONE guarded transition function
          (edge map + optimistic `WHERE provision_state=$from` update +
          audit + `billing.*` event). State names are stored verbatim
          (PENDING…FAILED, uppercase) in subscriptions.provision_state; the
          Phase 2 `status` column becomes a compatibility projection.
          TERMINATED is terminal; FAILED retains artifacts for admin retry.
Reason:   Master doc: "Provisioning should be a state machine"; no state
          may be reachable that the machine does not define. Optimistic
          guards make replays idempotent and races non-destructive.
Status:   Accepted

ADR-052
Decision: Payment gateways are a registry behind PaymentProvider
          (authorize/capture/refund/webhook). Webhooks verify the HMAC
          signature over the RAW body BEFORE parsing, then publish the
          event, then transition; the provider event id is the idempotency
          anchor (unique partial index) so replays cannot double-charge,
          double-invoice or double-provision. Renewal attempts anchor on
          period boundaries (last_renewal_period_end), not wall clocks.
Reason:   "Never trust client-side paid" + webhook replay idempotency are
          the phase's hard requirements; anchoring on provider event ids
          and period boundaries keeps the machine deterministic under
          retries, injected clocks and concurrent sweeps.
Status:   Accepted
```

## Deviations (with reasons)
1. **` PayAndActivate` creates the subscription inside Checkout** (order + PENDING subscription in one tx) rather than after payment: the master flow (Order→Payment→Invoice→Provision→Active) needs the subscription to exist as the machine's subject before money moves; a declined payment leaves the order cancelled and the PENDING subscription as the audit artifact.
2. **Product→plan grant on purchase**: provisioning applies the purchased plan to the organization (`packages.Assign` + `enforce_limits` re-convergence) because the Phase 7/8 create gates check the ORG's plan kind/count. This is the intended meaning of the WHM "plans↔products link" (purchases grant the linked plan).
3. **Discord-bot terminate backup gap**: no bot backup job type exists until Phase 11 merges; termination for bots proceeds without a backup (web + minecraft are backup-first). Flagged above for the Phase 11 seam.
4. **`service` product types are manual-fulfilment**: provisioning refuses them with a clear error (no node workload); an operator fulfils and the subscription is activated via the admin surface. Honest stub, documented in the API response.
5. **Minecraft enum values in 0030**: my migration adds `mc_install/mc_stop/mc_start/mc_delete/mc_backup_world` idempotently so the billing chain is self-sufficient even if applied before Phase 7's 0028; both migrations use `ADD VALUE IF NOT EXISTS`, so order is irrelevant.
6. **Secrets never enter billing records**: bot env vars / RCON secrets are NOT provisioned from orders; customers set them through the Phase 7/8 write-only endpoints. The payment-instrument token is sealed with secretbox and never returned by any API (tested).

## Definition of Done — mapping
- *Purchase→ACTIVE provisions correct workload*: proven end-to-end — `TestBillingPurchaseFlowToActive` (HTTP: checkout→pay→PROVISIONING→`bot_install` job with `billingprovision-` key→job truth→ACTIVE→paid invoice `INV-…`→paid order), `TestPurchaseProvisionsAndActivates`, and `TestBillingSuspendUsesWebsiteLifecycleJob` (web dispatch uses the platform `suspend_website` job, no new side channel).
- *Gateway webhook replay is idempotent (no double provision/invoice)*: `TestWebhookReplayIsIdempotent` + `TestHandleWebhookVerifiesBeforeTransition` + `TestBillingWebhookTamperReplay` (HTTP 401 on tamper, `replayed:true` on replay, single dispatch job).
- *Failed payment walks PENDING→grace→SUSPENDED→TERMINATED on schedule with audit trail*: `TestGraceWalkSuspendTerminate` (injected 1-day intervals; asserts every state, the suspend/resume/terminate engine calls, the recovery path and the audit rows) + `TestRenewalExtendsPeriod` (quarterly extend + double-sweep idempotency) + `TestCancelAtPeriodEndTerminates`.
- *No state reachable that the machine doesn't define*: single Transition function + 8×8 edge-matrix test + audited rejection; illegal admin moves surface as 409.
- *Money = integer minor units + currency*: enforced at validation, store and API (`amount_minor`/`currency` everywhere; `TestMoneyValidation`; no float money in the billing path).
- *Invoices immutable once issued*: DB trigger + store guards, proven by direct SQL tamper attempts.

## Test evidence
- `cd backend && go build ./... && go vet ./...` — clean.
- `EPICPANEL_TEST_DATABASE_URL=…epicpanel_test_bl go test ./internal/billing/ -count=1 -v` — **22/22 PASS**.
- `EPICPANEL_TEST_DATABASE_URL=…epicpanel_test_bl go test ./internal/api/ -count=1 -run TestBilling -v` — **5/5 PASS** (purchase flow, webhook tamper/replay, org scoping + roles, admin surface, invoice-pay recovery + payment-method write-only, web suspend dispatch).
- Full backend with the shared harness: `EPICPANEL_TEST_DATABASE_URL=…epicpanel_test_bl go test ./... -p 1 -count=1` — **17/17 packages ok, 0 FAIL** (packages share one DB; `-p 1` is the repo convention).
- Frontend: `npx tsc --noEmit -p tsconfig.json` — clean; `npx vite build --config apps/customer/vite.config.ts` and `--config apps/admin/vite.config.ts` — both green.
- Migration 0030 proven re-runnable: the billing-package harness drops the full schema (incl. billing tables) and re-applies the entire chain on every test run.
- Integration DB: `epicpanel_test_bl` (wave-assigned).
