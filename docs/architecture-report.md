# EpicPanel — Architecture Report (Phase 1 Codebase Audit)

> Audit of `epicpanel-2` as of 2026-09-07. Read-only: **zero production code changed**.
> Method: full walkthrough of Go control plane (126 files), Go agent, React frontend, 18 migrations;
> real call paths traced; documented claims in `EPICPANEL.md`/`tillnow.md` verified against code.
> Every finding carries `file:line` evidence. All paths relative to repo root.

---

## 1. Current architecture

```
frontend/ (React 19 + Router 7 + Tailwind, 21 pages)
   │  HTTPS/JSON  /v1/...  (cookie session | Bearer epk_ token)   [polling only, no WS except terminal]
   ▼
backend/cmd/api  (Go, stdlib ServeMux, pgx/v5 → PostgreSQL 16)
   ├─ auth/users/organizations(RBAC)/audit      session+token auth, 404-cloaking
   ├─ httpapi  (router, CORS, in-memory rate limit, API-token scope middleware)
   ├─ servers · websites · runtimes · databases · domains/SSL · deployments
   ├─ backups · monitoring · crons · sshkeys · terminal(WS) · apps · packages
   ├─ settings (setup wizard) · apitokens · secretbox (AES-GCM) · ports · isolation (bwrap)
   ├─ jobs     (Postgres queue: FOR UPDATE SKIP LOCKED claim, retry ≤3, fanout callbacks)
   └─ scheduler (hourly: status refresh, SSL renewals, backups, cron re-sync, site usage)
   │
   │  HTTP polling — agent claims jobs, reports results/progress, sends heartbeats
   ▼
backend/cmd/agent  (Go, ONE goroutine: ticker → heartbeat → drain job queue synchronously)
   ▼
Linux data plane: nginx (+apache/OLS behind nginx proxy), PHP-FPM pools, systemd app services,
MariaDB/MySQL/PostgreSQL, bubblewrap sandboxes, per-site users, crontabs, backups on local disk
```

Communication model is **pull-based**: control plane never contacts agents; agents poll
`/v1/agent/jobs/claim` on a 10 s ticker (default, `EPICPANEL_AGENT_POLL_INTERVAL`). There is **no
WebSocket/event stream** from control plane to UI (contract #7 in phases/README.md is unmet);
the UI polls REST endpoints. The single WS usage is the web terminal (`internal/terminal`).

**Plane-separation violations inside the control plane process**: the file manager performs
`os.WriteFile/os.RemoveAll/os.Chown` directly on `/srv/epicpanel/websites` (`internal/websites/files.go:201-208,325,460-461`)
and the port allocator probes `127.0.0.1` binds in-request (`internal/websites/portalloc.go:82-89`).
Both silently assume panel host == web host, contradicting the agent model used everywhere else.

## 2. Existing modules

| Module | Purpose (verified from code) |
|---|---|
| `internal/auth` | bcrypt, opaque sessions (SHA-256 at rest), cookie+Bearer, register/login/logout/me |
| `internal/users` | user store, first-user-becomes-admin bootstrap, admin account creation (`/v1/admin/users`) |
| `internal/organizations` | orgs, members, role ranks owner>admin>developer>billing/support, 404-cloaking |
| `internal/audit` | `audit_logs` append + list, best-effort recorder |
| `internal/httpapi` | router/middleware, JSON errors, CORS, token-bucket rate limit, API-token scope enforcement |
| `internal/apitokens` | `epk_` tokens: hashed, scoped, expiring, org-confined |
| `internal/servers` | server registry, reg/agent tokens, heartbeat+metrics ingestion, maintenance, capacity, auto-placement |
| `internal/jobs` | queue: enqueue, atomic claim, retry, progress, scrub; `OnJobFinished`/`OnJobClaimed` fanout |
| `internal/websites` | site lifecycle, files, config/rewrites, staging, WordPress, usage snapshots, agent claim/result endpoints |
| `internal/runtimes` | per-server runtime registry, refcounted removal, PHP/OLS extensions, detect-software adoption |
| `internal/databases` | DB provisioning, AES-GCM credential vault + audited reveal, phpMyAdmin SSO |
| `internal/domains` | global-unique domains, primary/aliases, DNS verify, SSL state machine |
| `internal/deployments` | git deploy/rollback history, encrypted deploy tokens |
| `internal/backups` | manual/scheduled backups, retention, restore |
| `internal/monitoring` | HTTP health checker, alerts (dedupe partial index), metrics history endpoint |
| `internal/crons` | per-site crontab desired state + sync jobs |
| `internal/sshkeys` | per-site SSH keys → authorized_keys sync jobs |
| `internal/terminal` | xterm.js over WS, pty, sandboxed site-user shell |
| `internal/isolation` | bubblewrap spec/build/exec (+seccomp placeholder) |
| `internal/apps` | process apps (node/python/go): build/start/stop/restart/status/logs via systemd |
| `internal/packages` | hosting plans, quota gates, apply_quota fanout |
| `internal/settings` | system settings, setup-token first-boot wizard |
| `internal/ports` | env-configured backend port ranges |
| `internal/secretbox` | AES-GCM at-rest encryption |
| `internal/config`, `internal/db` | env config, pgx pool + embedded migrations |
| `internal/agent` (26 files) | agent-side typed ops (fpm, nginx, apache/OLS, proxy, db, ssl, dns, deploy, staging, backup, runtime, app, cron, wp, ssh, quota, usage, detect) |
| `cmd/agent`, `cmd/agent-shell` | agent loop + restricted login shell |
| `frontend/` | 21 pages, WHM-style shell, api client, AuthContext |

## 3. Dependency relationships

- **`jobs` is the hub.** Every mutating subsystem (websites, runtimes, databases, domains,
  deployments, backups, crons, sshkeys, apps, packages, settings) enqueues jobs and advances its
  state machine in `OnJobFinished` fanout wired in `internal/api/server.go`. The agent endpoints
  (claim/result/progress) are physically hosted by `websites/agent_endpoints.go` — a misplaced
  dependency (websites → whole platform).
- **`servers` ← everything**: placement (`AutoPickServer`, `internal/servers/store.go:360-385`),
  capacity, maintenance gating in `ClaimNext` (`internal/jobs/store.go:102`).
- **`organizations` RBAC ← all handlers** via per-package copies of `requireOrg`/`resolveOrg`
  (10 duplicated implementations, e.g. `websites/handler.go:112-165`, `runtimes/handler.go:36-95`, …).
- **`secretbox` ← databases, deployments, settings** (credential vault, deploy tokens, SSO key).
- **`isolation` ← terminal + agent-shell** (sandbox spec files are the integration contract).
- **Frontend → API only** (correct: `frontend/src/lib/api.ts` is a bare fetch wrapper; no infra logic in UI).
- **Cycles/leaks**: fanout callbacks run with `context.Background()` and no timeout
  (`websites/agent_endpoints.go:186`, `deployments/handler.go:326-330`, `domains/handler.go:430`) —
  a slow DB call inside result-reporting blocks the agent's HTTP handler.

## ★ Metrics root-cause deep-dive (feeds Phase 3)

**Why CPU/RAM/disk/network go delayed or inaccurate under high load — five compounding causes:**

1. **Heartbeat starvation by head-of-line blocking (the primary cause).** The agent runs ONE
   goroutine: on each ticker tick it sends one heartbeat, then synchronously drains the entire job
   queue (`cmd/agent/main.go:107-126` → `agent.PollAndExecute` loop at :116-124). Jobs are long:
   apt/runtime installs run up to 10 min per exec (`internal/agent/runtime_ops.go:328`), backups
   tar+mysqldump (`backup_ops.go:47-81`), ACME issuance (`ssl_ops.go:138-232`), DB wait-for-ready
   60 s (`database_ops.go:219-237`). While any job runs, **no heartbeat is sent**. Under load
   (backed-up queue — exactly when metrics matter most) the inner loop never returns to the
   ticker, `last_seen_at` ages past the 2-minute `OfflineAfter`
   (`internal/servers/store.go:31-33`), the server flips to **offline**, and metrics freeze at
   their last pre-busy value while the UI keeps rendering them as current.
2. **CPU% is mathematically wrong.** `readCPUIdleComplement` (`internal/agent/client.go:194-213`)
   takes a SINGLE cumulative sample of `/proc/stat` and returns busy/total **since boot** — a
   lifetime average that barely moves under load. The comment claims "the control plane can refine
   this" — it never does: `Store.Heartbeat` just INSERTs the value (`internal/servers/store.go:205-226`).
   Auto-placement then scores servers on this bogus number (`store.go:375-377`).
   (Contrast: per-site usage does it right — cgroup `cpu.stat` deltas between samples,
   `internal/agent/site_usage.go:78-95` — proving the pattern was known but not applied to servers.)
3. **Network metrics are never collected at all.** `metricsPayload` has no network fields
   (`internal/agent/client.go:115-124`); `/proc/net/dev` is never read. The master doc requires
   network usage — it is MISSING, not merely stale.
4. **Staleness is invisible by design-omission.** `GET .../metrics` returns the latest row with no
   age/freshness field (`internal/servers/handler.go:273-290`); the frontend renders it without any
   `LIVE/STALE/OFFLINE + age` indicator (global contract #4 violated everywhere — e.g.
   `frontend/src/pages/Dashboard.tsx:268-275`). Server `status` is only recomputed by an **hourly**
   sweep (`internal/api/scheduler.go:17,50-59`), so a dead agent shows "online" for up to 1 h and a
   recovered one shows "offline" for up to 1 h.
5. **Unbounded history + no downsampling.** `server_metrics` is append-only at ~1 row/10 s/server
   (8 640 rows/day/server) with no retention/pruning anywhere (grep: no `DELETE FROM server_metrics`);
   same for `http_checks`, `jobs`, `audit_logs`, `deployments.log` (unbounded TEXT,
   `migrations/0008_deployments.sql:12`). Read path is index-supported (`idx_server_metrics_server_time`,
   `migrations/0002_servers.sql:51`) but the table grows forever.

Secondary: per-site `site_usage` snapshots ride the same blocked queue (enqueued via jobs,
`internal/websites/usage.go:51-55`), so cPanel-style per-site usage goes stale identically;
`GET .../metrics/history` leaks a pooled connection per call — `Pool.Query` rows never closed
(`internal/monitoring/handler.go:144`).

## 4. KEEP modules

Sound architecture + working functionality; fix listed bugs in place, do not rewrite:

| Module | Justification | Notes (must-fix) |
|---|---|---|
| `auth` | bcrypt, hashed opaque sessions, revocation, cookie+Bearer | disable public register post-setup; CSRF defense |
| `users` | first-admin bootstrap transactional | count-check not locked (`users/store.go:51-60`) — race = multiple admins |
| `organizations` | rank RBAC + 404-cloaking correct | admin can grant/demote owner (`organizations/store.go:227-240`) |
| `audit` | append-only, best-effort, indexed | retention policy needed |
| `secretbox` | AES-GCM, nonce handling correct | silently persists env key to file / auto-generates (`secretbox/secretbox.go:41-69`) |
| `config`, `db`, migrations | boring, embedded, runner works | fix duplicate ALTER (`0018:106,120`) |
| `runtimes` | ON CONFLICT retry logic sound (`runtimes/store.go:92-110`) | `CreateAvailable` inserts `uuid.Nil` into NOT NULL FK `created_by` (`store.go:65,120` vs `0004:10`) → detect-software adoption always fails |
| `databases` | vault + scrub + audited reveal design good | `lookupWebsite` uses `Exec` on SELECT, ignores rows → cross-org attach (`databases/handler.go:222-226`) |
| `domains` | global uniqueness, state machine, partial index match | renewal dead-end: `failed` never re-enqueued (`domains/store.go:246-253` vs `scheduler.go:73-76`) |
| `deployments` | release dirs/symlink swap solid | no in-flight guard (`handler.go:189-230`); unbounded log |
| `packages` | server-side gates centralized | `UsageForOrg` queries nonexistent `websites.metadata` (`packages/store.go:231`); `Delete` can write no response (`handler.go:230-240`) |
| `settings` | hashed single-use setup tokens, constant-time compare | `Complete` non-atomic (`setup.go:237-238`); public `verify-hostname` DNS oracle (`setup.go:118-136`) |
| `ports` | pure value package | allocator duplicates its loop (`websites/portalloc.go:61-79`) |
| `isolation` | bwrap mount-namespace model verified live | no seccomp (`seccomp.go:3-14`), host network shared (`spec.go:52-65`), ulimit inside bash (`exec.go:17-52`) |
| agent ops: `executor.go` `fpm.go` `nginx.go` `database_ops.go` `db_helpers.go` `ssl_ops.go` `dns_verify.go` `deploy_ops.go` `site_usage.go` `extension_ops.go` `cron_ops.go` `quota_ops.go` `pkgr.go` `ssh_ops.go` `sandbox_sync.go` | argv+whitelist core, atomic-write/validate/restore pattern | add timeouts to `nginx -t`/`php-fpm -t` (`nginx.go:289`, `fpm.go:217`) |
| `cmd/agent-shell` | restricted shell + env contract | `EPICPANEL_SANDBOX=1` pre-set bypasses sandbox (`main.go:29,99-108`) |

## 5. REFACTOR modules

Right concept, structural problems to fix incrementally:

| Module | Core problems |
|---|---|
| `servers` | **Tenant model contradiction**: routes are org-scoped but store ignores org — `GetByID`/`ListAll` have no org filter (`store.go:246-274`), `serverFromPath` discards `orgID` (`handler.go:198-211`) → any org's admin can DELETE any server, rotate any registration token, toggle maintenance. Status derived hourly (stale). Metrics ingestion has no age/retention. Phase 2 must decide once: platform-wide fleet + admin-only routes, or org-scoped. |
| `jobs` | No lease/timeout/reaper: agent death mid-job leaves `running` forever (`store.go:96-114,131-142`) and every dependent state machine stuck. No retry backoff (failed → immediately `pending`). No retention. |
| `httpapi` | ScopeEnforce **fail-open** — unmapped routes pass any token (`tokenauth.go:66-73,113-127`); XFF trusted unconditionally for rate-limit key + audit IP (`tokenauth.go:155-158`, 6× `clientIP` copies); limiter buckets never evicted (memory leak, `ratelimit.go:32-35`); no `MaxBytesReader` on JSON bodies (`respond.go:18-25`). |
| `apitokens` | Tokens inherit creator's platform-admin role (`store.go:154,163`) and `RequireMinimumRole` bypass doesn't exclude tokens (`organizations/rbac.go:28-32`) → admin-created token is a cross-org master key. |
| `websites` | Job-driven core sound; file manager + port probing run in control-plane process (plane violation); enqueue-before-status races (`handler.go:631-635`, `runtimes/handler.go:219-226`, `databases/handler.go:291-296`); `MarkReady` no status guard — late result can resurrect a `deleting` site (`store.go:201-206`); `Update` = 5 un-tx'd writes + no package check on runtime change (`handler.go:426-540`); rewrite-snippet injection — only first token validated, rest rendered verbatim into nginx (`config_handler.go:39-50` + `agent/nginx.go:155-179`). |
| `api` (wiring + scheduler) | Scheduled backups dead on arrival: `Create(..., uuid.Nil, ...)` violates `backups.created_by` FK (`scheduler.go:90` vs `0009:19`) AND `Enqueue(ctx, d.WebsiteID, ...)` passes website UUID as `server_id` (`scheduler.go:104` vs `jobs/store.go:80-89` FK) — contradicts "live-verified" claim in EPICPANEL.md; server jobs feed returns full payload/result to any-org members (`server.go:86-118`); WP DB `created_by uuid.Nil` FK (`server.go:659` vs `0006:16`); quota gates fail open on DB error (`server.go:526-533,568-572`). |
| `backups` | Manual path works; restore has no concurrency guard (`handler.go:198-242`); `MarkJobClaimed`/`ApplyJobOutcome` don't check `job.Type` (`handler.go:268-273,356-367`); failed rows never pruned (`store.go:171-180`); `DueForSchedule` full-scan, no index. |
| `monitoring` | Health checker is sequential with 10 s timeout/site — 500 sites × slow sites overruns the 60 s interval (checks fall behind = stale health, `checker.go:57-95`); connection leak in history endpoint (`handler.go:144`); no `http_checks` retention; no LIVE/STALE semantics. |
| `crons` | Command validation is a blacklist — `$(...)` passes (`store.go:38-49`) → arbitrary shell as site user; `enqueueSync` swallows error and enqueues EMPTY list → transient DB blip wipes a site's crontab (`handler.go:83-93`); dead columns `last_status/last_run_at`. |
| `sshkeys` | `List` has no website→org authorization — cross-tenant read of any site's keys by UUID (`handler.go:66-81`); sync enqueue error swallowed with no periodic re-sync (`handler.go:170-173`). |
| `terminal` | `CheckOrigin: true` (`terminal.go:60-66`); per-site session cap never enforced — `admit` checks map but never inserts (`:254-276`); idle timer one-shot, never reset on activity (`:189`). |
| `apps` | `env_vars` plaintext at rest AND returned by GET (`store.go:41`, `handler.go:241-249`) — no secretbox unlike deploy tokens; Status/Logs enqueue a job per request (`handler.go:343,365`) → queue grows with UI polling; port default has no uniqueness check (`handler.go:149-152`). |
| `internal/agent` (lib, remaining files) | `RestoreBackup` interpolates `db.TargetName`/`dumpFile` into `bash -c` WITHOUT `safeIdentifier` (`backup_ops.go:120-145` — the one real injection gap; Create path validates at :53); `webserver_ops.go` 482-line mixed concerns, restarts shared Apache per site (`:194`); `runtime_ops.go` god-file (772 lines) + curl\|bash; 4 duplicate exec helpers, 3× vhost-writer, 2× pool-writer duplication; dead code `app_ops.go:258-262`. |
| `frontend` | Correct primitives + clean API layer, but: no data layer (no cache/dedup/cancel — `api.ts:28-36`); 24 swallowed `.catch(() => …)` render failures as empty states; org-switch/unmount races (no abort, `Dashboard.tsx:45-79`); N+1 client loops (`Servers.tsx:30-35`, `Software.tsx:107-128`, `Backups.tsx:45-52`); Dashboard 3-stage waterfall (:50-70); zero polling on "live" surfaces + bell never refreshes; 8 pages duplicate card/stat/bar/badge styles; no freshness/stale UI anywhere. |

## 6. REWRITE modules

| Module | Why |
|---|---|
| `cmd/agent` main loop (`main.go:91-127`) + `client.go` metrics collection | The single-goroutine heartbeat-then-drain design is the root cause of stale metrics (§★). Needs: heartbeat on its own goroutine/interval, job execution concurrent (bounded lanes/priority), CPU from two-sample `/proc/stat` deltas, network from `/proc/net/dev`, timestamps + monotonic counters in payload. Structure of `PollAndExecute` (typed switch) is fine and stays. |
| Metrics pipeline end-to-end (`servers/store.go` ingestion + `servers/handler.go` + `monitoring/handler.go` + frontend display) | Needs retention/downsampling, `LIVE/STALE/OFFLINE + age` contract, one batched fleet-metrics endpoint (kills the N+1), and push (WS/SSE) instead of per-page polling. |

## 7. REMOVE modules

No whole module is dead — the codebase is lean. Remove these dead artifacts:

- `websites.web_servers` column (`0018:64`) — never read; `web_server` (0005) is used.
- `domains.redirect_to_https` (`0007:12`), `cron_jobs.last_status/last_run_at` (`0014:8-9`),
  `applications.restarts` (`0016:12`) — never written by any code.
- `websites.status='deleted'` enum value — rows are hard-deleted (`websites/store.go:340-349`).
- Duplicate `ALTER TABLE domains ADD COLUMN IF NOT EXISTS docroot_suffix` (`0018:106` and `:120`).
- Dead code: `_ = payload` (`websites/wordpress.go:87-99`), quoted-shell builder (`agent/app_ops.go:258-262`),
  unused `ports.Allocates` (`ports/ports.go:54-61`), unused `isUniqueViolation` (`runtimes/store.go:188`).
- Redundant indexes covered by UNIQUE prefixes: `idx_websites_server`, `idx_runtimes_server`,
  `idx_databases_server`, `idx_ssh_keys_website` (`0003:17,20`, `0004:12,16`, `0006:18,21`, `0015:11,13`).

## 8. Missing modules

vs master plan (`prompts/epicpanel-docs`) and global contracts:

1. **WebSocket/event stream** (contract #7: `… → Event → WebSocket → UI`) — entirely absent; UI is 100 % polling.
2. **Network metrics** — never collected (§★3).
3. **Metrics retention/downsampling + LIVE/STALE/age semantics** — absent (§★4,5).
4. **Job lease/reaper + retry backoff** — absent (§5 jobs).
5. **Unified resource & limit engine** — package limits only patch PHP-FPM pool values (ADR-031);
   no cgroup/systemd enforcement for CPU/mem/pids on pools or apps; displayed usage and enforced
   limits do NOT come from the same authoritative layer (contract #5).
6. **Minecraft hosting** — no module, no job types, no schema.
7. **Discord bot hosting** — same.
8. **Billing/invoices/subscriptions** — same (packages have a `price` field only).
9. **2FA** — no TOTP code anywhere.
10. **Service accounts / Reseller / Super-Admin roles** — role set is still owner/admin/developer/billing/support.
11. **DNS record hosting** — deferred by ADR-021 (acceptable, but must be tracked).
12. **Remote backup storage (BackupProvider)** — local disk only.
13. **Alert notification channels** — alerts table exists, no email/Slack sender.
14. **Shared rate limiter** — in-memory, single-instance only (ADR-028 acknowledged).
15. **OpenAPI spec drift** — `api/openapi.json` documents 42 paths; the router now wires ~120.
16. **Agent TLS** — agent↔control-plane is plain HTTP bearer tokens by default (`config.go:22`, setup URL printed `http://`).

## 9. Critical technical debt

1. **Doc-vs-code divergence is systemic.** EPICPANEL.md claims servers are org-scoped (they're
   platform-wide), scheduled backups "live-verified" (broken by two FK bugs, §5 api), "Known
   Issues: None yet" (this report lists 60+). `tillnow.md` is closer but still wrong on fleet
   scoping. Any future session trusting the docs will build on sand.
2. **No convergence when the agent dies** — jobs stuck `running` forever; websites stuck
   `provisioning`, deployments/backups/runtimes/SSL stuck mid-flight (§5 jobs). The desired-state
   promise (EPICPANEL.md §12) is only half-implemented: enqueue exists, reconcile does not.
3. **Authorization is copy-pasted and drifting** — 10× `requireOrg`, 6× `clientIP`, 6×
   `isUniqueViolation`; drift already produced the sshkeys-List and databases-lookupWebsite holes.
4. **Fanout callbacks with `context.Background()`** in the agent-result request path (§3) —
   one stuck subsystem write blocks the whole job pipeline.
5. **Unbounded growth** of jobs/server_metrics/http_checks/audit_logs/deployments.log (§★5) —
   guaranteed operational failure at scale.
6. **Poll-driven job enqueue** (site_usage per request, app status/logs per request) — the UI
   literally manufactures queue backlog; combined with §★1 this is a feedback loop into staleness.
7. **Secret handling debt**: apps env_vars plaintext, WP password in job results visible to
   billing role, SSO key plaintext in system_settings, secretbox auto-generating keys silently.
8. **API inconsistencies**: `/v1/...` vs doc target `/api/v1/...` (contract #9 — Phase 2 must
   decide once); agent endpoints wired under websites package; capacity route registered under
   `{org_id}` but ignores it.

## 10. Security risks

Ranked; all verified in code (see §5 notes for file:line):

| # | Risk | Severity |
|---|---|---|
| S1 | Cross-org server fleet takeover: any org admin can delete/re-enroll (rotate reg token) any server in the fleet | **Critical** |
| S2 | API tokens inherit platform-admin; `RequireMinimumRole` bypass applies to tokens; ScopeEnforce fail-open for unmapped routes (`/v1/admin/*`, `/v1/settings`, members…) | **Critical** |
| S3 | Public `/v1/auth/register` never disabled + unlocked first-admin count → race to own the panel pre-setup | **Critical** |
| S4 | Cross-tenant reads: sshkeys List IDOR; server jobs feed leaks payloads (encrypted creds, repo URLs) to any-org billing; databases website-attach check broken | **High** |
| S5 | WP admin password plaintext in `jobs.result` + returned at RoleBilling; visible in host `ps` via wp-cli argv (`agent/wp_ops.go:131`) | **High** |
| S6 | nginx rewrite-snippet line injection (first-token-only validation) → arbitrary directives in shared server blocks | **High** |
| S7 | Cron command blacklist bypass `$(…)` → arbitrary shell as site user (weaker than sandbox, but cron runs UNSANDBOXED — see S10) | **High** |
| S8 | RestoreBackup `bash -c` interpolation without identifier validation (defense-in-depth failure) | **High** |
| S9 | CSRF: cookie auth on all state-changing routes, no CSRF token; CORS `*`+credentials echo; WS `CheckOrigin: true` | **High** |
| S10 | Isolation gaps: no seccomp, host network reachable from sandbox (DB ports!), cron/app processes unsandboxed, `EPICPANEL_SANDBOX=1` bypass, ulimit-not-exec rlimits | **Medium** |
| S11 | X-Forwarded-For trusted unconditionally → rate-limit bypass + audit-IP poisoning; limiter map unbounded (memory DoS) | **Medium** |
| S12 | Org admin can grant/demote `owner` without consent | **Medium** |
| S13 | No request body size limit (JSON endpoints) | **Medium** |
| S14 | secretbox: env key silently written to `/etc/epicpanel/secret.key`; missing key → silent ephemeral key (data unrecoverable on restart) | **Medium** |
| S15 | repo_url SSRF (agent clones internal URLs); public `verify-hostname` DNS oracle; setup token in query strings (logged) | **Low-Med** |
| S16 | Dev defaults in prod paths: `sslmode=disable` DB default, `CookieSecure=false` unless `EPICPANEL_ENV=production`, HTTP agent URLs | **Low-Med** |

Positive findings (keep): no SQL injection found (parameterized everywhere; agent-side identifiers
whitelist-validated); tokens all SHA-256-hashed at rest, constant-time compares where raw;
path traversal in file manager properly closed (Clean+EvalSymlinks+prefix); agent argv discipline
good outside S7/S8; fail-closed sandbox defaults; audit on mutations.

## 11. Performance risks

1. **Metrics/heartbeat starvation loop** (§★1) — also a correctness risk; the #1 fix.
2. **Sequential health checker**: 500 targets × up to 10 s timeout per 60 s pass → checks fall
   behind, alerts late (`monitoring/checker.go:57-95`).
3. **Connection leak**: unclosed `Pool.Query` rows in metrics-history endpoint (`monitoring/handler.go:144`) —
   pool exhaustion under dashboard polling.
4. **N+1 + waterfalls**: frontend per-server metrics loop (`Servers.tsx:30-35`), per-site backups
   loop, Dashboard 3-stage waterfall; backend `CapacityAll` per-server LATERAL + correlated
   website-count subquery (acceptable at 10s of servers, not 1000s).
5. **Blocking work in request path**: port loopback probes 300 ms/port (`portalloc.go:83`),
   `notifyChanged` synchronous with Background ctx (`domains/handler.go:430`), AssignOrg enqueues
   N jobs in-request (`packages/handler.go:263-286`), file-manager tar walker ignores client
   disconnect (`files.go:371-415`).
6. **Missing indexes**: `domains.organization_id` (list query), `websites(status, usage_sampled_at)`
   (StaleUsage scan), `websites.backup_schedule` (DueForSchedule scan), `jobs(server_id, created_at)`
   (recent-jobs sort).
7. **Unbounded tables** (§★5) → vacuum/IO degradation; `deployments.log` TEXT per deploy.
8. **Queue churn**: hourly full crontab re-sync of every site + poll-driven usage/status jobs
   (`scheduler.go:113-132`, `usage.go:51-55`, `apps/handler.go:343,365`).
9. **In-memory rate limiter + single-instance assumption** blocks horizontal scaling of the API.
10. **Frontend over-fetching**: fetch-all-then-filter-client-side (`SiteDetail.tsx:66-69`),
    unclosed setTimeout reloads, no request cancellation → wasted bandwidth + state races.

## 12. Recommended target architecture

Keep the shape — it is right (control plane + pull-agent + jobs + Postgres). Fix the seams:

```
React UI ──REST /v1──► Control plane (Go, chi-or-stdlib, pgx) ──► PostgreSQL
   ▲                      │  ▲                                    (jobs + metrics w/ retention,
   │ WS event stream      │  │ agent poll: claim/heartbeat/result  partitioned/downsampled)
   └──────────────────────┘  │
        (Postgres LISTEN/NOTIFY → in-proc event hub → WS hub; Redis only when 2+ API instances)
                             │
                     Node Agent (Go), per server:
                       ├─ heartbeat goroutine (own interval, never blocked by jobs)
                       ├─ collector: /proc/stat DELTAS, meminfo, statfs, /proc/net/dev, uptime
                       │    → every sample timestamped; instantaneous ≠ historical
                       ├─ bounded job lanes: long-install | normal | interactive(priority)
                       └─ enforcement: cgroup v2 slice per site (CPU/mem/pids) = SAME source
                            that feeds displayed usage (contract #5)
```

Concrete decisions to adopt in Phase 2/3:

1. **Events**: `events` table + `LISTEN/NOTIFY` → single WS endpoint `/v1/events` (session-authed)
   pushing job.*, server.*, website.*, alert.* — replaces all page-level polling loops; REST stays
   as source-of-truth fallback. No external broker (contract #8: boring).
2. **Metrics**: agent sends delta-computed CPU + network + timestamped samples; control plane
   writes to `server_metrics` (time-partitioned or cron-pruned: raw 24 h @ 10 s, rollup 5 min @
   30 days); every read response carries `collected_at` + `freshness: live|stale|offline`;
   UI renders age per contract #4. One batched `GET /v1/servers/metrics:latest` for the fleet.
3. **Jobs**: add `lease_expires_at` + reaper (running with expired lease → pending, attempts+1);
   exponential backoff via `visible_after`; nightly retention prune. Keep SKIP LOCKED.
4. **Tenancy decision (Phase 2, once)**: recommend **platform-wide fleet, admin-only server routes**
   (matches the cPanel model already half-implemented) — then delete the org-scoping pretense from
   server routes and fix docs; customer-facing surfaces reference servers read-only via their sites.
5. **Authz dedup**: one shared `orgauth` middleware package used by all handlers (kills the 10
   copies and the drift class); deny-by-default scope map; tokens never carry platform-admin.
6. **Plane separation**: move file manager + port probing behind agent jobs (typed ops
   `ManageFiles`, `AllocatePort`) or explicitly document single-host mode and gate the feature.
7. **Limits engine (Phase 9)**: cgroup v2 per-site slice (already read by `site_usage.go`) becomes
   the enforcement + display source; packages map to slice properties, not just FPM pool values.
8. **API version**: keep `/v1` (working, documented, tested); add `/api/v1` alias only if the
   master-doc path contract is externally required. Decide once in Phase 2.

## 13. Recommended development order

Per `phases/README.md` (master doc order), with this audit's mapping:

```
1 → 2 → 3 → 4 → 5 → 6 → 9 → 7 → 8 → 10 → 11 → 12 → 13 → 14 → 15
```

- **Phase 2 (Core Platform)**: tenancy decision + server-route authz fix (S1), register lockdown
  (S3), token-role/scope fixes (S2), authz dedup, jobs lease/reaper/backoff, scheduler FK bugs,
  rate-limiter key/eviction, body limits, `/v1` vs `/api/v1` decision.
- **Phase 3 (Node Agent + Real-Time Metrics)** ⚑: everything in §★ + §6 rewrites + §12.2 —
  heartbeat decoupling, delta CPU, network metrics, retention, freshness contract, WS event
  stream, batched fleet metrics endpoint, frontend data layer replacing poll loops.
- **Phase 4 (Web Hosting Engine)**: rewrite-snippet injection (S6), vhost/pool writer dedup,
  plane-separation items (§12.6), apache/OLS primary mode.
- **Phase 5/6 (cPanel/WHM)**: customer vs admin surface split makes the S1 route fix testable;
  UI freshness per contract #4.
- **Phase 9 (Resource & Limit Engine)** before 7/8: cgroup enforcement = display source (§12.7);
  fix cron/app unsandboxed execution here.
- **Phase 10 (Billing)**: builds on packages; add invoices/subscriptions module.
- **Phase 11 (Backups)**: fix scheduled path + restore guards + remote BackupProvider.
- **Phase 12 (Security)**: S9-S16 hardening, 2FA, service accounts, TLS for agent channel.
- **Phase 13 (Monitoring/Observability)**: parallel health checker, alert channels, OTel/Prometheus.
- **Phase 14 (UI/UX)**: design-system consolidation (8 duplicated component sets), react-query-style
  data layer, WS integration from Phase 3.
- **Phase 15 (Scale)**: shared limiter (Redis), multi-instance API, partitioning review.

---

### Classification totals

| Class | Count | Items |
|---|---|---|
| KEEP | 16 modules + 15 agent files | auth, users, organizations, audit, secretbox, config, db/migrations, runtimes, databases, domains, deployments, packages, settings, ports, isolation, agent-shell (+ listed agent ops files) |
| REFACTOR | 15 | httpapi, apitokens, servers, jobs, websites, api/scheduler, backups, monitoring, crons, sshkeys, terminal, apps, agent-lib, frontend, (+ OpenAPI spec) |
| REWRITE | 2 | cmd/agent loop + metrics collection; metrics pipeline end-to-end |
| REMOVE | 0 modules, 12 dead artifacts | §7 list |
| MISSING | 16 | §8 list |
