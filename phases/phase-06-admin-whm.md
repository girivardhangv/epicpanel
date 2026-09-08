# PHASE 6 — Admin WHM

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 712–744 (Phase 6 spec)
> 3. `prompts/ui-ref.html` — admin screens: `screen-admin-overview`, servers, services, accounts, domains, packages, backups, logs, settings, tickets
> 4. `phases/README.md` + Phase 5 handoff notes (shared packages available)
> 5. Code: `frontend/src/pages/{Servers,AdminUsers,Packages,Team,Settings,Activity}.tsx`, `backend/internal/{servers,monitoring,jobs,organizations}`
>
> Depends on: Phases 3–5 · Blocks: Phase 13

## Mission
Administration experience. Principle (verbatim): **"I should be able to understand the entire infrastructure in seconds."** Built on the SAME shared packages as Phase 5 — no second component system.

## Feature list — VERBATIM from master doc (Phase 6)
```
Dashboard | Nodes | Servers | Customers | Accounts | Plans | Domains | DNS
IP Addresses | Web Servers | PHP | Databases | Backups | Security
Monitoring | Logs | Jobs | Billing | Support | Settings
```
Include (verbatim): node health · capacity · account distribution · active services · failed jobs · alerts · resource consumption

## Work Items
- [ ] `apps/admin` on shared packages; WHM nav per list above
- [ ] WHM Dashboard: fleet resource consumption, node health grid, capacity per node, account distribution, active services, failed jobs, alerts — all from Phase 3 live stream (LIVE/STALE/OFFLINE everywhere)
- [ ] Nodes/Servers: enroll, heartbeat status, maintenance mode, per-node drill-down (host metrics + services: nginx/apache/OLS/PHP-FPM/MariaDB/Docker)
- [ ] Customers & Accounts: cross-org management; suspend/resume/terminate gated by RBAC (`account.suspend`, `node.execute` from Phase 2)
- [ ] Plans/Packages, Domains, DNS, **IP Addresses** (pool/allocation — check `internal/ports` + audit), Web Servers, PHP, Databases admin views
- [ ] Backups admin view (module lands Phase 11 — build view against contract), Security, Logs (audit feed exists: `Activity`), **Jobs console: list/filter/retry/cancel, dead-letter view**
- [ ] Billing + Support screens: stubs/routes only (Phases 10 and support backlog fill them) — do not fake data
- [ ] Bulk actions + confirmation dialogs + command/search palette + global notifications (shared from Phase 5 kit)

## UI parity rule
Match `prompts/ui-ref.html` admin screens. **No emoji. No unnecessary animations. Proper icon library (lucide).**

## Deliverables
`apps/admin` with all views above; jobs console; node drill-downs; WHM overview on live stream.

## Definition of Done
Admin sees fleet health <5s on load; can filter accounts across nodes; retry a failed job; suspend an account — each server-side authorized + audited; admin and customer apps import the same `packages/*`.

## Session Handoff — FILL BEFORE ENDING SESSION
- Views done vs stubbed (billing/support): (fill)
- IP address management findings: (fill)
- Handed to Phase 13 (alert UI seams): (fill)
- Update `phases/README.md` status row for Phase 6 → DONE
