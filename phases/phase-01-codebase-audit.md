# PHASE 1 — Codebase Audit & Architecture Recovery

> **NEW AGENT SESSION — START HERE.** Read in this order before touching anything:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 2–65 (Core Architecture + agent responsibilities) and lines 142–264 (Phase 1 + its Agent Prompt)
> 3. `phases/README.md` (global contracts, execution order, phase statuses)
> 4. `EPICPANEL.md` + `tillnow.md` — existing architecture claims; **verify against code, do not trust blindly**
>
> Depends on: nothing · Blocks: every later phase · Do NOT change functionality in this phase.

## Mission
Complete architecture & codebase audit of epicpanel-2 (Go control plane + Go node agent + React frontend). Classify every module, find the root cause of stale/incorrect metrics under load, and produce the report that Phase 2 builds on.

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> You are working on an existing hosting control panel built with Go and React/TypeScript.
>
> DO NOT immediately rewrite the project.
>
> First perform a complete architecture and codebase audit.
>
> Analyze the entire repository and understand how the existing system currently works.
>
> Inspect:
> - Go backend
> - React frontend
> - database schema and migrations
> - authentication
> - authorization/RBAC
> - APIs
> - WebSockets
> - background jobs
> - metrics
> - node/server management
> - provisioning
> - Docker integration
> - filesystem management
> - web-server integration
> - PHP-FPM
> - DNS
> - SSL
> - backups
> - Minecraft functionality
> - Discord/bot functionality
> - billing
> - logging
> - configuration
> - deployment
> - tests
>
> For every existing module classify it as:
> KEEP / REFACTOR / REWRITE / REMOVE / MISSING
>
> Do not make large changes yet.
>
> Identify architectural problems, security problems, performance problems, race conditions, stale-data problems, bad abstractions, duplicated logic, inefficient database queries, blocking operations, and modules that will prevent the system from scaling.
>
> Pay particular attention to the current resource monitoring implementation.
>
> Determine why CPU/RAM/disk/network usage can become delayed or inaccurate under high load.
>
> Create an architecture report containing:
> 1. Current architecture
> 2. Existing modules
> 3. Dependency relationships
> 4. KEEP modules
> 5. REFACTOR modules
> 6. REWRITE modules
> 7. REMOVE modules
> 8. Missing modules
> 9. Critical technical debt
> 10. Security risks
> 11. Performance risks
> 12. Recommended target architecture
> 13. Recommended development order
>
> Do not rewrite good existing code simply for stylistic reasons.
>
> Preserve working functionality where the architecture is sound.
>
> The goal is to recover the existing architecture first and create a safe plan for incremental rework.

## Problem hunt list (verbatim from doc lines 179–193)
duplicated logic · dead code · insecure code · race conditions · bad abstractions · database problems · API inconsistencies · memory leaks · blocking operations · inefficient queries · incorrect metrics · stale polling · frontend over-fetching · backend bottlenecks

## Repo starting point (pre-audit snapshot, for orientation only)
- `backend/cmd/{api,agent,agent-shell}`, `backend/internal/*` (~27 modules: auth, users, organizations, audit, httpapi, api, jobs, settings, apitokens, secretbox, config, db, migrations, servers, agent, monitoring, isolation, ports, terminal, sshkeys, websites, runtimes, databases, domains, deployments, backups, crons, apps, packages), 126 Go files, migrations 0001–0018
- `frontend/src/pages/*` (22 pages), `lib/api.ts` (`/v1/...` fetch client), Tailwind, lucide-react, xterm.js
- Known from docs: agent = poll loop + heartbeat + typed jobs; jobs table with `FOR UPDATE SKIP LOCKED`, retry ≤3; RBAC roles owner/admin/developer/billing/support
- Expected MISSING vs master plan (verify): Minecraft, Discord bots, billing/invoices/subscriptions, WebSocket event stream, 2FA, service accounts, Reseller/Super-Admin roles, unified Resource/limit enforcement

## Work Items
- [ ] Inspect everything in the verbatim list above; trace real call paths (not just file names)
- [ ] Classify every module KEEP/REFACTOR/REWRITE/REMOVE/MISSING with one-line justification
- [ ] **Metrics deep-dive**: trace agent poll → `internal/servers/metrics.go` → DB → frontend polling; explain exactly why CPU/RAM/disk/network go stale/inaccurate under high load (queue delay? slow queries? poll interval? blocking collection? over-fetch?)
- [ ] Security pass: authz coverage per route, secret handling, terminal/shell exposure, token storage
- [ ] Perf pass: N+1s, missing indexes, sync work in request path, polling loops in frontend
- [ ] Write `docs/architecture-report.md` with all 13 numbered sections (verbatim structure above)

## Deliverables
- `docs/architecture-report.md` (13 sections)
- Module classification table
- Metrics root-cause section (feeds Phase 3 directly)

## Definition of Done
Report exists with all 13 sections; every module classified; zero production code changed.

## Session Handoff — FILL BEFORE ENDING SESSION
- Report path: `docs/architecture-report.md` (all 13 sections + ★ metrics deep-dive + classification table)
- Top 5 critical findings:
  1. **Heartbeat starvation = metrics root cause**: agent's single goroutine drains the job queue synchronously after each heartbeat tick (`cmd/agent/main.go:107-126`); long jobs (10-min installs, backups) block heartbeats → server flips offline and metrics freeze exactly under load.
  2. **CPU% is a since-boot lifetime average** (single cumulative `/proc/stat` sample, `internal/agent/client.go:194-213`); control plane never refines it; network metrics never collected at all; no LIVE/STALE/age anywhere (contract #4 unmet).
  3. **Cross-org server fleet takeover**: server routes check org membership but store ignores org (`servers/store.go:246-274`, `handler.go:198-211`) — any org admin can delete/re-enroll any server; plus API tokens inherit platform-admin and ScopeEnforce fails open (`tokenauth.go:113-127`, `rbac.go:28-32`).
  4. **No job lease/reaper**: agent death mid-job leaves jobs `running` forever and every entity state machine (website/runtime/backup/deploy/SSL) stuck — desired-state reconciliation is half-implemented.
  5. **Scheduled backups are dead on arrival** (two FK bugs: `uuid.Nil` created_by + website UUID passed as server_id, `api/scheduler.go:90,104`) — and docs claim "live-verified": systemic doc-vs-code divergence (also: runtimes detect-adoption, WP DB creator).
- KEEP/REFACTOR/REWRITE/REMOVE/MISSING counts: **16 KEEP / 15 REFACTOR / 2 REWRITE / 0 REMOVE modules (12 dead artifacts) / 16 MISSING**
- Metrics root cause in one sentence: The agent sends heartbeats only between synchronously-drained job batches, so any busy queue head-of-line-blocks metrics collection while the one CPU figure it does send is a since-boot average and network is never sampled — and nothing in the stack labels stale values as stale.
- Deviations from as-written items: none; all 13 report sections present. Zero production code changed (DoD met).
- For Phase 2 (decide once): tenancy model (recommend platform-wide fleet + admin-only server routes), `/v1` vs `/api/v1`, deny-by-default scope map, shared authz middleware package.
- Status: **DONE** — session 2026-09-07.
