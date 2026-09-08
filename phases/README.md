# EpicPanel — Phase Plan Index (session-per-phase workflow)

> **How to use:** each phase runs in its OWN agent session. The phase file is self-contained:
> it lists what to read at session start, embeds the master-doc Agent Prompt **verbatim**,
> extracts every specifically-mentioned requirement, and ends with a **Session Handoff**
> section the agent MUST fill before ending the session (that's how the next session inherits context).
>
> Sources: `prompts/epicpanel-docs` (master plan, 15 phases) · `prompts/ui-ref.html` ("StratusHost" WHM/cPanel prototype — UI reference) · `EPICPANEL.md`/`tillnow.md` (existing architecture) · `docs/architecture-report.md` (Phase 1 output)

## Recommended execution order (master doc "Development Order")
`1 → 2 → 3 → 4 → 5 → 6 → 9 → 7 → 8 → 10 → 11 → 12 → 13 → 14 → 15`
(Resource/Limit Engine #9 runs BEFORE Minecraft #7 / Discord #8 so both workloads consume one engine. Phase 3 is elevated: the stale-metrics incident is architectural, not cosmetic.)

## Phase files & status (update the row when a session ends)

| # | Phase | File | Status | Handoff summary |
|---|-------|------|--------|-----------------|
| 1 | Codebase Audit | `phase-01-codebase-audit.md` | DONE | Report at `docs/architecture-report.md`. 16 KEEP / 15 REFACTOR / 2 REWRITE / 0 REMOVE (12 dead artifacts) / 16 MISSING. Metrics root cause: agent single-goroutine loop head-of-line-blocks heartbeats behind synchronous jobs + CPU is a since-boot average + network never sampled + no LIVE/STALE labeling. Critical: cross-org server takeover, token admin-inheritance + fail-open scopes, public register, no job reaper, scheduled-backup FK bugs (docs' "live-verified" claim is false). |
| 2 | Core Platform | `phase-02-core-platform.md` | DONE | RBAC v2 (permission strings + reseller role + token hardening), 2FA (TOTP+recovery codes), service accounts, event bus (PG LISTEN/NOTIFY + optional Redis) + WS `/v1/ws`, jobs hardened (lease/reaper/backoff/idempotency + dead-letter view), core-entity tables (0019–0021), audit-critical security fixes (server mutations admin-only, deny-by-default scopes, registration lockdown, CSRF, CORS, trusted proxies, body cap, sshkeys/databases/lookupWebsite, rewrite smuggling, apps env encryption). API prefix: `/api/v1` canonical, `/v1` aliased. Entity CRUD handlers deferred to consuming phases (billing/MC/Discord). |
| 3 | Node Agent + Real-Time Metrics ⚑ | `phase-03-node-agent-realtime-metrics.md` | DONE | agentproto v1 stream (`GET /v1/agent/stream`, session-scoped seq + ring-buffer resume replay, ingest dedup/gap counters), agent loops decoupled (stream / jobs / legacy-heartbeat-when-stream-down), delta CPU + network + disk-IO + swap + inodes + TCP + services + per-site cgroups + containers + app envelopes (MC/Discord reserved fields), self-throttling collector (degraded flag, never silent), in-memory LiveStore as sole live source with LIVE(≤15s)/STALE(≤120s)/OFFLINE + age + ONLINE/STALE/OFFLINE node registry, WS push via `/v1/ws` (no DB in live path), async batched history writer + 24h raw retention + 5-min rollup 30d (migration 0022), frontend WS client + useMetrics/useFreshness + FreshnessBadge, all metric polling removed; monitoring history connection leak fixed. Protocol: `docs/protocol-metrics.md` (ADR-046). |
| 4 | Web Hosting Engine | `phase-04-web-hosting-engine.md` | DONE | Common WebServerProvider abstraction (nginx / nginx+apache / nginx+OLS) over one config-safety pipeline (atomic tmp+rename → 120s-capped validate → restore-on-fail; `atomic.go`), per-provider render matrix incl. redirects + 503 suspended stubs, dead proxy_ops deleted, dbadmin writers deduped. NEW: FTP/SFTP module (0023; $6$-hash-only payloads, vsftpd virtual users + sshd internal-sftp, prefix-scoped merge), DNS zones/records CRUD + RFC1035 publish op (0024, named-checkzone + BIND include merge), domain redirects in DesiredPayload (0025), suspend/resume lifecycle jobs with agent vhost backups `.epicpanel-suspend-bak` (0026), `internal/limits` single-seam stub for Phase 9 wired at buildDesiredPayload, SSL issued/failed/renewal events on the bus. Fanout sinks + scope map + frontend UI (aliases, redirects, FTP one-time passwords, DNS zone page, suspend/resume, SSL expiry chips). Tests: provider rollback proofs, lifecycle round-trips, API integration suite `phase4_test.go`; all 12 packages green (`-p 1`). ADR-047. |
| 5 | Customer cPanel | `phase-05-customer-cpanel.md` | DONE | Monorepo split complete: `packages/{core,ui,icons,charts,tables,forms,design-system}` (7 pkgs via `@epicpanel/*`, workspaces + tsconfig paths + vite aliases; legacy root app kept green via re-export shims → ZERO duplicated component code) + `apps/customer` on :5174. 14 screens: WS-live dashboard cards w/ FreshnessBadge (verbatim age formats), websites, domains, DNS zone CRUD+publish, file manager, FTP (one-time passwords), databases + pma-sso, PHP selector, crons, backups, metrics history (live strip WS-only, separated), SSL expiry countdown, security (2FA + API keys), account. Email DEFERRED (no backend module — flagged to owner). Terminal & SSH keys removed from customer nav AND server-side authz raised dev→admin (`terminal/terminal.go`, `sshkeys/handler.go` List/Create) — authz > UI hiding. UX kit (AppSidebar/DataTable/ConfirmDialog/Toaster/AreaChart/PageTitle) built for Phase 6 reuse. Verify: `tsc -b` clean; root 804 kB + customer 382 kB builds green. ADR-048. |
| 6 | Admin WHM | `phase-06-admin-whm.md` | TODO | — |
| 9 | Resource & Limit Engine (run 7th) | `phase-09-resource-limit-engine.md` | DONE | `internal/resources` unified engine (Resource type + data-driven Plan→limits matrix; `GetLimits`/`GetUsage`/`Enforce`; adding a plan = data only, proven by test). Agent enforcement: cgroups v2 per-site slices (`cpu.max`/`memory.max`+`memory.high`@90%/`pids.max`, sync timer recaptures respawned workers), disk XFS project + ext4 user quotas (detect-then-apply, else honestly `accounted` w/ reason), nftables per-account RX+TX counters + monthly high-water, cgroup `io.weight`, FPM `pm.max_children` bound to plan RAM/32 MB (Phase 4 `internal/limits` stub → engine shim), count limits = create-time fail-closed gates + agent reconciliation guard. Anti-drift by construction: ONE shared cgroup reader serves both display collectors and enforcement verification (e2e drift test). Over-limit: kernel-capped → `none`; disk/bandwidth → idempotent `suspend_website` + `limits.breach` event (Phase 10 hook); `throttle` re-asserts. Migration 0027: +9 matrix columns, 7 verbatim plans, `workload_resource_usage`, `enforce_limits` job; legacy pre-0027 rows keep legacy governed counts. `go build`/`vet` clean; `go test ./... -p 1` w/ test DB: 14 pkgs ok / 0 FAIL. ADR-049, ADR-050. |
| 7 | Minecraft Hosting (run 8th) | `phase-07-minecraft-hosting.md` | TODO | — |
| 8 | Discord Bot Hosting (run 9th) | `phase-08-discord-bot-hosting.md` | TODO | — |
| 10 | Billing + Provisioning | `phase-10-billing-provisioning.md` | TODO | — |
| 11 | Backups | `phase-11-backups.md` | TODO | — |
| 12 | Security Hardening | `phase-12-security-hardening.md` | TODO | — |
| 13 | Monitoring / Alerts | `phase-13-monitoring-observability.md` | TODO | — |
| 14 | UI/UX Polish | `phase-14-ui-ux.md` | TODO | — |
| 15 | Performance/Scale → Production | `phase-15-performance-scale-production.md` | TODO | — |

## Global contracts (every session must honor — copied from master doc)
1. **Control Plane + Node Agent.** Control plane says WHAT should happen; agent decides HOW to safely perform it. Control plane never executes arbitrary server commands/SSH for ops.
2. **The flow:** `API → Job → Queue → Node Agent → Execution → Event → WebSocket → UI` — never `HTTP → SSH → wait → return`.
3. **Jobs:** idempotent, retryable, logged, auditable, reversible where practical.
4. **Metrics:** instantaneous ≠ historical; live dashboard never reads the historical DB; every live value carries `LIVE / STALE / OFFLINE` + age (`updated 240ms ago` / `last update 18.4s ago`); never present old metrics as current.
5. **Limits:** displayed usage and enforced limits come from the SAME authoritative node-side layer; agent enforces, not displays.
6. **Security:** no arbitrary shell to customers; server-side authz always; secrets never in logs/API responses.
7. **Frontend:** ONE design system, TWO experiences — `packages/{ui,icons,charts,tables,forms,design-system}` + `apps/{admin,customer}`; no emoji; no unnecessary animations; proper icon library.
8. **Backend taste:** boring and reliable — Go + chi/pgx + PostgreSQL + Redis + WS + OTel + Prometheus + systemd; don't introduce 20 frameworks; don't rewrite KEEP modules for style.
9. **APIs versioned** (`/v1` today; `/api/v1/...` target per doc — Phase 2 decides once).

## Session protocol (for the agent)
- **Start:** read your phase file top-to-bottom, then its "read in this order" list. Do not skip the verbatim prompt — it is the contract.
- **During:** specifically-mentioned items (lists, names, formats, diagrams) must be implemented AS WRITTEN — same names, same states, same fields. Deviations require a note in the handoff with a reason.
- **End:** fill the "Session Handoff" section in your phase file, update this README's status row + handoff summary, and record decisions in `EPICPANEL.md` (ADR style, continuing numbering).
