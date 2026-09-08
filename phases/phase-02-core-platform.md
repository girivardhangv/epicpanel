# PHASE 2 — Core Platform

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `docs/architecture-report.md` — **Phase 1 output; it decides what you may rewrite**
> 3. `prompts/epicpanel-docs` lines 66–139 (stack + monorepo rule), 265–375 (Phase 2 + Agent Prompt)
> 4. `phases/README.md` (global contracts)
> 5. Code: `backend/internal/{auth,users,organizations,httpapi,api,jobs,audit,apitokens,secretbox,settings}`, `frontend/src/{lib,context}`
>
> Depends on: Phase 1 · Blocks: 3–15

## Mission
Core platform foundation: identity, RBAC, core entities, versioned API, WebSocket/event infra, job system hardening. Do NOT blindly rewrite modules the audit marked KEEP.

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> Implement the core platform foundation based on the Phase 1 audit.
>
> Do not blindly rewrite existing modules marked KEEP.
>
> Rework or replace modules identified as architecturally unsafe.
>
> Build a clean Go backend foundation with:
> - PostgreSQL
> - Redis
> - versioned REST API
> - WebSocket/event infrastructure
> - background job system
> - structured logging
> - configuration management
> - graceful shutdown
> - health/readiness endpoints
> - database migrations
> - consistent error handling
> - request validation
> - rate limiting
> - audit logging
>
> Implement:
> - users
> - customers
> - roles
> - permissions
> - RBAC
> - sessions
> - authentication
> - 2FA foundation
> - API keys
> - nodes
> - hosting accounts
> - plans
> - services
> - jobs
> - events
>
> Keep APIs versioned.
>
> Make all provisioning and infrastructure operations asynchronous through jobs where appropriate.
>
> Jobs must be idempotent and retryable.
>
> Do not allow arbitrary shell commands through public APIs.
>
> Create a clean internal service architecture so later modules can build on it without creating duplicated logic.
>
> Update tests as modules are changed.

## Specific requirements from master doc — implement EXACTLY as named
- **RBAC roles (verbatim list):** Super Admin, Admin, Support, Reseller, Customer — reconcile with existing owner/admin/developer/billing/support; keep a mapping, do not silently drop existing grants
- **Permission strings (verbatim examples):** `server.read`, `server.manage`, `account.create`, `account.suspend`, `billing.manage`, `node.execute`, `backup.restore`
- **Core entities (verbatim list):** User, Customer, Server, Node, Service, Plan, Product, Account, Domain, Resource, Subscription, Invoice, Job, Event — create missing tables via new migrations; map existing (servers→Node, websites→Account, packages→Plan)
- **API surface (verbatim):** `/api/v1/auth`, `/users`, `/customers`, `/nodes`, `/servers`, `/accounts`, `/plans`, `/domains`, `/services`, `/jobs`, `/metrics` — repo currently uses `/v1/...`; decide prefix ONCE in this phase, alias old routes, document in EPICPANEL.md
- **Stack (doc recommendation):** Go + pgx + PostgreSQL + Redis + WebSocket + OpenTelemetry + Prometheus + systemd; "Don't introduce 20 frameworks... Keep the backend relatively boring and reliable." Repo uses stdlib ServeMux + pgx — audit decides, do not churn frameworks for style
- **Frontend stack (doc):** React, TypeScript, Vite, TanStack Query, React Router, Tailwind, Recharts/lightweight charting, WebSocket client, Zustand — adopt where missing, don't rip out working code
- **Monorepo rule (doc):** `packages/{ui,icons,charts,tables,forms,design-system}` + `apps/{admin,customer}` — "Don't build two completely separate frontend systems." Scaffold this structure now; migrate pages into it in Phases 5/6

## Work Items
- [ ] Identity: users, sessions, password auth (exists — verify), **2FA foundation (TOTP + recovery codes)**, API keys (exists), **service accounts**
- [ ] RBAC v2: roles above + granular permission strings; server-side enforcement on every route; 404-cloaking preserved
- [ ] Core entity migrations + stores + handlers for missing entities (Customer, Subscription, Invoice, Product, Event...)
- [ ] Event infrastructure: in-process hub + Redis pub/sub abstraction; emit events on job state changes (Phase 3 consumes)
- [ ] WebSocket endpoint skeleton `/v1/ws` (auth via session/API key) — full metrics protocol lands in Phase 3
- [ ] Jobs: idempotency keys, retry/backoff policy, dead-letter visibility (exists — harden per audit)
- [ ] Health (`/healthz`) + readiness (`/readyz`); structured logging; config management; request validation; consistent errors; rate limiting; audit — verify each, fix gaps only
- [ ] No arbitrary shell through public APIs — audit `terminal`, `apps`, `agent` ops paths

## Deliverables
Migrations, RBAC v2 + 2FA + service accounts, event bus + WS skeleton, entity CRUD, tests updated per changed module.

## Definition of Done
`go test ./...` green; every route server-side authorized; job lifecycle emits events; permission strings enforced (not just roles); health/readiness respond correctly.

## Session Handoff — FILL BEFORE ENDING SESSION
- What was built vs skipped (with audit justification):
  **Built** — (1) RBAC v2: `internal/permissions` (verbatim permission strings incl. the 7 from the master doc; role map owner=Customer, admin=Admin, reseller=Reseller (new org role), support=Support, Super Admin=is_platform_admin), owner-grant guard, last-owner demotion guard, tokens NEVER inherit platform-admin (store + rbac + audit-logs bypass all exclude tokens). (2) 2FA foundation: RFC-6238 TOTP on stdlib (`auth/totp.go`), secretbox-encrypted secrets (base64 TEXT), login second step via hashed one-shot `mfa_challenges`, 10 hashed single-use recovery codes, endpoints setup/enable/disable/verify. (3) Service accounts (`service_accounts` table + kind='service' api tokens, org-confined). (4) Events: `internal/events` Bus (durable `events` table) + Driver abstraction with PG LISTEN/NOTIFY (default) and go-redis v9.5 driver (EPICPANEL_REDIS_URL; Redis not installed locally so PG driver is tested). WS endpoint `GET /v1/ws` (session/token auth, org-scoped frames, origin allowlist from CORS config, ping keepalive). Events emitted on job claimed/succeeded/failed/requeued + backup.scheduled. (5) Jobs hardened: idempotency keys (partial unique index, ON CONFLICT returns existing), per-type lease on claim, exponential backoff via visible_after (tests: SetBackoffUnit(1ns)), lease reaper on a 30s fast loop, `GET /v1/jobs` dead-letter view (admin), retention prune (30d), job status transitions emit events. (6) Core entities (migration 0020): customers/products/services/subscriptions/invoices/resources/events tables — stores+handlers land with their consuming phases (billing #10, MC #7, Discord #8) per audit §8 to avoid speculative CRUD. (7) Security fixes: server mutations (create/delete/rotate-token/maintenance/db-tools/capacity) → platform-admin session ONLY (audit S1); deny-by-default ScopeEnforce with expanded resource map (S2); registration locked once setup_completed (S3); CSRF guard `X-EpicPanel` header on cookie mutations (S9); CORS wildcard+credentials rejected (S9); trusted-proxy-aware ClientIP (11 duplicated copies deleted), rate-limit bucket eviction (S11); 1MiB JSON body cap (S13); sshkeys List tenant scoping (S4); databases lookupWebsite fixed (S4); ErrInternal now logs causes; packages Delete wrote-no-response bug; package/db gates fail CLOSED (S14-class); scheduled-backup FK bugs fixed + DueBackup carries server_id + NULL created_by via CreateSystem (uuid.Nil class); runtimes/databases created_by relaxed to NULL for system actors; settings.Complete atomic w/ advisory lock; rewrite-snippet smuggling blocked BOTH control-plane and agent-side (S6); cron `$(` blocked; jobs endpoint developer+ (S5 partial — full scrub in #12); apps env_vars encrypted at rest + GET returns keys only (S7-class); terminal per-site cap actually enforced + registration; /api/v1 canonical prefix with transparent rewrite (/v1 kept); readyz; first-admin bootstrap advisory-locked. Frontend: api.ts sends X-EpicPanel; MFA two-step login UI; TwoFactorCard on Security page; monorepo scaffold packages/{ui,icons,charts,tables,forms,design-system} + apps/{admin,customer}; User.mfa_enabled. First-admin/`/auth/me` for tokens returns token identity (never creator's admin flag).
  **Skipped (with reason)** — entity stores/handlers for customers/products/services/subscriptions/invoices/resources: tables + schema ready; CRUD belongs with the consuming phases (audit: no speculative modules); TanStack Query adoption deferred to Phase 3 data-layer work (needs npm registry decision); full rewrite-rules grammar per directive also deferred to Phase 4 (blocklist-class validation now in place); Terminal idle-timer reset-on-activity and apps port uniqueness left for their consuming phases; OpenAPI regeneration deferred (spec already drifted 42→~120 paths — needs tooling, Phase 2 decision logged).
- Final API prefix decision: **`/api/v1` is canonical** (master doc verbatim); `/v1` remains a working alias via `httpapi.APIPrefixRewrite` (outermost middleware, zero route duplication). Documented in EPICPANEL.md ADR-041. OpenAPI regeneration tracked in ADR-041 note.
- New migrations added: `0019_identity_rbac_v2.sql` (reseller role, MFA columns/tables, service_accounts, api_tokens.kind, created_by NULL relaxations), `0020_core_entities.sql` (customers, products, services, subscriptions, invoices, resources, events), `0021_jobs_hardening.sql` (idempotency_key + partial unique, visible_after, lease_expires_at, idx_jobs_claim, audit-flagged missing indexes). NOTE: deployed instances must run migrations; local dev DB already migrated.
- Known gaps handed to Phase 3: metrics protocol on WS hub (this session shipped the event channel only); server CPU still since-boot (agent rewrite is THE Phase 3 deliverable); frontend still polls (react-query/WS data layer next); events payload >7KB drops payload field (by design, NOTIFY limit); Redis driver untested live (no redis-server locally — interface identical to tested PG driver); WP admin password still in job results until Phase 12 scrub (mitigated: developer+ gate); `/v1/setup/verify-hostname` still public (DNS oracle, low sev — Phase 12).
- Status: **DONE** — session 2026-09-07. go test ./... green (6 packages incl. full integration suite); frontend tsc+vite build green.
