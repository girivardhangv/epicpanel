# PHASE 10 — Billing & Provisioning (state machine)

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1003–1043 (Phase 10 spec — diagrams ARE the requirement)
> 3. `phases/README.md` + Phase 2 handoff (Subscription/Invoice/Product entities), Phase 9 handoff (plans), Phase 7/8 handoffs (workload provision APIs)
> 4. Code: `backend/internal/{jobs,organizations}` (only `RoleBilling` exists today — no billing module)
>
> Depends on: Phases 2, 4, 7, 8, 9 · Blocks: 13

## Mission
Order→payment→invoice→provision lifecycle and renewal/failure paths, driven by an explicit provisioning **state machine**. All transitions via idempotent jobs.

## Flows — VERBATIM from master doc
```
Order → Payment → Invoice → Provision → Active
```
```
Renewal → Payment → Extend
```
```
Payment failed → Grace period → Suspend → Terminate
```
```
PENDING → PROVISIONING → ACTIVE → SUSPENDING → SUSPENDED
       → TERMINATING → TERMINATED → FAILED
```
"Provisioning should be a state machine."

## Work Items
- [ ] `internal/billing`: Order, Payment, Invoice, Subscription entities (tables exist from Phase 2 — add business logic); money as integer minor units + currency; invoices immutable once issued
- [ ] Payment gateway abstraction: `PaymentProvider` interface (authorize/capture/refund/webhook) — pluggable, none hardcoded; webhook → event → state transition (never trust client-side "paid")
- [ ] State machine implementation: single transition function, guards per edge, every transition = job (idempotent, retryable) + audit entry + event; FAILED retains artifacts for manual retry
- [ ] Provision dispatch: subscription product type → correct engine (web account / Minecraft instance / Discord bot) with Phase 9 plan limits applied at creation
- [ ] Renewal: scheduler job → invoice → payment attempt → Extend; failure → grace period (configurable days) → Suspend (calls workload suspend ops — must be reversible) → Terminate (irreversible, backup-first per Phase 11 rule)
- [ ] Suspend semantics per workload: web=site offline, minecraft=stop, discord=stop — reuse existing lifecycle jobs, no new side channels
- [ ] WHM: billing settings, plans↔products link, invoice list/issue/void, grace-period config
- [ ] Customer: subscription list, invoices, payment method, renewal status (Phase 5 seam)

## Deliverables
`internal/billing` + migrations + state machine + gateway interface + WHM/customer screens.

## Definition of Done
Purchase→ACTIVE provisions correct workload; gateway webhook replay is idempotent (no double provision/invoice); failed payment walks PENDING→grace→SUSPENDED→TERMINATED on schedule with audit trail; no state reachable that the machine doesn't define.

## Session Handoff — FILL BEFORE ENDING SESSION
- Gateway(s) wired vs interface-only: (fill)
- Grace/suspend/terminate default timings: (fill)
- State machine test coverage notes: (fill)
- Update `phases/README.md` status row for Phase 10 → DONE
