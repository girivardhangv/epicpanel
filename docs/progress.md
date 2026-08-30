# EpicPanel — Build Progress & Status

> Snapshot of what exists in the repository today (Phase 1 foundation +
> Phase 2 website hosting engine + Phase 3 monitoring & telemetry +
> Phase 4 SSL/ACME + Phase 5 notifications + Phase 6 managed databases).
> Companion docs: [`architecture.md`](architecture.md) (design decisions) and
> [`api.md`](api.md) (HTTP contract).

---

## 1. Repository layout

| Path        | What it is                                                                 |
| ----------- | -------------------------------------------------------------------------- |
| `backend/`  | Go 1.26 REST API — chi v5 router, pgx v5 (PostgreSQL), Argon2id, embedded migrations |
| `frontend/` | React 19 + TypeScript + Vite 8 + Tailwind v4 SPA, TanStack Query 5, react-hook-form + zod |
| `agent/`    | Cross-platform Go agent (Linux/Windows · amd64/arm64) — enrollment + inventory heartbeats |
| `scripts/`  | Dev-only licensing stub server (`:9911`) mirroring the real `/v1` contract |
| `shared/`   | Cross-component contract notes (planned: generated TS types + OpenAPI)     |
| `docs/`     | Architecture decisions, API reference, this progress document              |

**Architecture in one line:** browser → `/api/v1` (Go API, PostgreSQL) → agent
protocol (`register` / `heartbeat`) → managed Linux/Windows machines. No shell
execution is exposed anywhere; the agent exposes typed operations only.

---

## 2. Backend — implemented

### 2.1 Database schema (embedded, forward-only migrations)

`migrations/0001_foundation.sql`:

| Table | Purpose |
| ----- | ------- |
| `users` | Panel accounts — Argon2id PHC hashes, lockout counters, case-insensitive unique username/email |
| `roles`, `permissions`, `user_roles`, `role_permissions` | Full RBAC model with `granted_by` audit and system-role protection |
| `sessions` | Server-side opaque sessions — SHA-256 token hash, per-session CSRF token, revocation reason |
| `installations` | Single-row installer lock (`pending → completed`) + persistent `instance_id` |
| `licenses` | License state cache — plan, status, features, masked key hint, fingerprint, raw payload |
| `audit_logs` | Actor/action/resource/IP/UA/metadata trail (actor types: user/system/agent/installer) |
| `system_settings` | JSONB KV store for operator-tunable settings |
| `servers` | Server inventory — specs JSONB snapshots, agent token hash, online/offline/revoked |

Seed data: **23 permission codes** (incl. forward-seeded `websites.*`,
`databases.*`) and system roles `super_admin` / `operator` / `viewer`.

`migrations/0002_password_reset.sql`: `password_reset_tokens` — single-use,
60-minute, SHA-256 hashed reset tokens.

### 2.2 Feature services (`internal/`)

- **auth** — login with per-account lockout + timing equalization (dummy Argon2
  verify), opaque 256-bit session tokens (only digests stored), sliding expiry
  with token rotation on refresh, per-session CSRF, Argon2id with transparent
  parameter upgrade, password policy validation, reset-token lifecycle.
- **users** — full admin management: list/get/create/update/delete, role
  binding, admin password reset (revokes target sessions), guards for
  self-deletion and last-active-super-admin.
- **servers** — agent enrollment (48-byte token shown once, stored hashed),
  list/get/revoke with liveness computed from `last_seen_at`, heartbeats that
  upsert inventory, dashboard aggregates.
- **licensing** — remote client for an external licensing server
  (`/v1/activate|validate|deactivate`), pure `Policy` grace-mode evaluation
  (unit-tested), full keys never persisted (masked hint only).
- **installer** — 9-endpoint first-run wizard state machine, permanently locked
  server-side after completion (`INSTALLER_LOCKED`), live host requirement
  probes (build-tagged Linux/Windows), DB version verification, DSN staging.
- **rbac** — `RequirePermission` middleware resolves effective permissions via
  joins on every request; role CRUD with system-role immutability; denials
  audited as `authz.denied`.
- **audit** — best-effort structured inserts, 21 named action constants.
- **settings** — cached typed KV (15 s), agent registration key auto-generation.
- **httpx** — request IDs, security headers, body limits, trusted-proxy-aware
  client IP, in-memory fixed-window rate limiting.
- **apierror** — uniform `{status, code, message}` error envelope.

### 2.3 HTTP surface (see `docs/api.md` for full contract)

- Public: login/logout/refresh/me/change-password, forgot/reset-password,
  `/installer/*` (9 endpoints), `/system/info`, `/healthz`.
- Agent protocol: `POST /servers/register` (`X-Agent-Key`), `POST
  /servers/heartbeat` (`Bearer` per-server token).
- Product APIs (session + CSRF + permission): dashboard summary, servers
  list/get/revoke/registration-key, license status/refresh/deactivate, users
  CRUD, roles CRUD + permissions list.
- Static SPA serving with path-traversal guards when `EPICPANEL_DIST_DIR` is set.

### 2.4 Composition root

`cmd/panel/main.go` + `support.go`: config → logger → pool → migrations →
service wiring → chi server with graceful shutdown. Includes the licensing
fingerprint derivation (`ep-` + sha256 of `instance_id`), first-admin
provisioner, dev-only reset-token console sink, and atomic config persistence
for installer DSN changes.

---

## 3. Frontend — implemented

### 3.1 Routes (`src/app/router.tsx`)

- `/` → `BootstrapGate` (installer vs login, driven by `GET /installer/status`)
- `/login` → `LoginPage` · `/install` → 7-step `InstallerWizard`
- `/app` (protected): **Dashboard, Servers, License, Users, Roles, Profile** —
  all real. **Websites, Databases** — honest `PlannedModulePage` placeholders
  (disabled sidebar entries with tooltip).
- Legacy aliases (`/servers`, `/dashboard`, …) redirect to `/app/*`.

### 3.2 Pages

- **Dashboard** — stat cards (servers, users, sessions, license badge) with 30 s
  polling; recent security events (8 audit items); telemetry card is an honest
  `EmptyState` until the monitoring module exists.
- **Servers** — inventory table, "Connect a server" panel (registration key +
  copy-paste agent commands for Linux/Windows), detail modal with hardware
  specs, revoke gated on `server.manage`.
- **Users** — table with role badges, create/edit modals (roles checklist,
  password strength meter, admin reset with session-revocation note, active
  toggle), delete gated on `users.delete`, last-super-admin protection notice.
- **Roles** — role cards (system/custom), create/edit modal with permission
  catalogue grouped by prefix, delete with confirm.
- **License** — status badges, full license detail rows, "Validate now",
  "Deactivate", grace-period explainer.
- **Profile** — account info, self-service password change (zod-validated).
- **Installer wizard** — requirements (live probes), license, database test /
  DSN override, configuration, administrator, security defaults, irreversible
  finish step. Server-truth step model; every POST re-validates server-side.

### 3.3 Infrastructure

- `lib/api.ts` — typed fetch client: same-origin cookies, automatic CSRF echo
  (`X-CSRF-Token` from the readable cookie), uniform `ApiError` with
  `code`/`requestId`, network-error normalization.
- `services/index.ts` — typed endpoint catalog (installer, auth, servers,
  license, users, roles, dashboard, system).
- `features/auth/AuthContext.tsx` — `useQuery(["auth","me"])` as session truth;
  `hasPermission()`; logout clears the whole query cache.
- `components/Guards.tsx` — `BootstrapGate`, `ProtectedArea` (frontend checks
  are UX only; the API is the security boundary).
- UI primitives: `Button`, `Input`/`PasswordInput` (strength meter), `Modal`,
  `Card`/`Badge`, `Alert`, `States` (Spinner/Error/Empty/NotConfigured).
- Tests (vitest + testing-library): `api.test.ts`, `Button.test.tsx`,
  `Input.test.ts` (password strength). Lint via oxlint.

---

## 4. Agent — implemented

- `cmd/agentd/main.go` — flags with env fallbacks (`-url`, `-key`, `-label`,
  `-interval`, `-ca-file`, `-dir`, `-enroll`); enrolls once, persists
  credentials (`credentials.json`, 0600, atomic write), heartbeats every 60 s,
  graceful shutdown, distinct exit codes (2 = missing key, 3 = token rejected).
- `internal/client` — enrollment (`X-Agent-Key`) + heartbeat (`Bearer`), TLS ≥
  1.2 with optional private-CA bundle, sentinel `ErrTokenInvalid`.
- `internal/platform` — build-tagged collectors (`_linux.go` / `_windows.go` /
  `_other.go`): hostname, OS/version, arch (amd64/arm64), CPU cores, memory,
  disk usage. Unknown values are reported as unknown — never fabricated.
- By design: **no command-execution surface**; inventory snapshots only
  (streaming metrics are the planned next agent feature).

---

## 5. Tooling & support components

- `scripts/licensing-server` — dev-only stub implementing the real `/v1`
  activate/validate/deactivate contract (in-memory, `pro-*` keys → Pro plan,
  12-month expiry, fingerprint binding). Never ships with a panel.
- `.env.example` — full configuration reference (database, server, security,
  licensing, logging, agent variables).
- `shared/README.md` — documents the agent↔panel and licensing contracts;
  planned: generated TypeScript types + OpenAPI so drift fails builds.

---

## 6. Known gaps / partial work (honest list)

1. **Password-reset delivery** — token lifecycle is complete, but delivery is
   dev-console logging only; production SMTP flow ships later.
2. **Background license revalidation** — config + policy helper exist and are
   tested, but no background loop wires them; revalidation is on-demand only.
3. **Websites / Databases modules** — permissions seeded, nav slots reserved;
   no backend feature, tables, or UI yet.
4. **Settings UI** — `settings.view/manage` permissions exist and the backend
   settings service works, but there is no `/app/settings` page.
5. **Audit viewer** — audit events surface only as 8 dashboard items; no
   dedicated browsable log page with pagination/filters.
6. **In-memory rate limiting** — per-IP limiter is single-process; horizontal
   deployments need a shared store (Redis etc.).
7. **Telemetry** — agent sends inventory snapshots only; no CPU/mem/disk time
   series, no charts.
8. **Route-level permission guards** — pages are auth-gated but not
   permission-gated in the router (server-side RBAC is the backstop).
9. **Test coverage is narrow** — backend: 3 pure-unit suites; frontend: 3
   component/client suites. No integration/e2e (Playwright planned).
10. **Minor known issues worth verifying** — dev logs (`panel.err.log`)
    recorded `COALESCE … jsonb to json` SQLSTATE 42846 errors on
    `/api/v1/users` and `/roles/detail`; installer completion does not gate on
    the *security* step; multiple password fields on one page share an
    auto-generated input id; duplicate `AuthProvider` nesting on `/login`.
    Several defined-but-unused helpers exist in both backend and frontend
    (candidates for wiring or deletion).

---

## 7. Suggested next modules (priority order)

### Phase 1 — complete the administration surface (low effort, high value)

1. **Settings module** — `GET/PATCH /api/v1/settings` + `/app/settings` page.
   The RBAC permissions, backend service and all seven canonical setting keys
   already exist; this is mostly transport + UI. Day-one operational value:
   tune password policy, session lifetime, lockout, agent key without redeploy.
2. **Audit log viewer** — paginated/filterable `GET /audit` + dedicated page.
   The table, indexes and `Recent()` query already exist; this turns the
   dashboard teaser into a real security tool.
3. **Wire password-reset delivery** — an SMTP mailer package behind the
   existing `reset.go` sink interface. The flow, tokens, rate limits and
   lockout integration are all built; only the transport is missing.
4. **Background license revalidation loop** — start the ticker that calls the
   already-implemented (and unit-tested) `revalidate` path on
   `EPICPANEL_LICENSE_REVALIDATE_EVERY`.
5. **Fix the known issues** in §6.10 (SQL COALESCE bug, installer security-step
   gate, input id collision, dead code cleanup).

### Phase 2 — monitoring & telemetry (the explicitly planned agent feature)

6. **Metrics streaming** — extend the agent heartbeat (or add a metrics
   channel) with CPU/memory/disk **usage over time**; panel stores a time-series
   table, exposes `GET /servers/{id}/metrics`, dashboard/server detail render
   real charts. This replaces the dashboard's honest `EmptyState` with the
   module the architecture already reserves (ADR-10: real data only).

### Phase 3 — hosting features (the core product)

7. **Websites module** — the flagship feature. Permissions are seeded
   (`websites.*`); the agent's "typed operations, no shell" contract needs its
   first real verbs (Nginx vhost CRUD, certificate management). Introduce the
   Nginx abstraction layer (IIS slotted for later per the architecture docs).
8. **Databases module** — MySQL/PostgreSQL instance + database/user management
   through the same agent operation pattern (`databases.*` permissions seeded).

### Cross-cutting (parallel, ongoing)

9. **Shared codegen** — OpenAPI spec + generated TS types in `shared/` so API
   drift fails builds (promised in `shared/README.md`).
10. **Test expansion** — backend handler/integration tests, Playwright e2e for
    login → installer → users flows.
11. **Horizontal-scale readiness** — shared rate-limit/session store, offline
    signed-token license verification.

---

## 8. Phase 2 — Website Hosting Engine (shipped)

> Objective: connect a server, add a domain, create a website, select a PHP
> version, provision the website, configure Nginx, and access the result.

### 8.1 Server management & registration tokens

- Servers gained `agent_url` (advertised management endpoint), `ops_token`
  (panel→agent secret, never returned by any API) and a `capabilities` JSONB
  probe cache.
- Registration switched from a never-expiring shared key to **single-use,
  expiring (≤7 days), revocable registration tokens** (`epreg-…`, SHA-256
  stored, consumed atomically, plaintext shown exactly once). Old
  `GET /servers/registration-key` removed.
- Server table shows connection state honestly: Online / Connecting (awaiting
  first heartbeat) / Offline / Unknown, plus a per-server **capability
  matrix** (nginx version, PHP runtimes, provisioning, log access; SSL/DNS
  marked as later phases). Nothing is fabricated — unreachable agents render
  the cached probe with an explicit label.

### 8.2 Agent ops channel (panel → agent)

- The agent now serves `/agent/v1/*` (default `:9200`) behind its ops bearer
  token: system info, nginx status/deploy/remove/set-enabled/reload, PHP
  discovery + per-site pools, constrained fs mkdir/write/remove/user, bounded
  log tails.
- **No command execution, no arbitrary file read/write, no shell.** Every path
  is validated against the configured sites root (symlink-evaluated),
  site slugs are strict, PHP settings are allowlisted (memory_limit,
  upload_max_filesize, post_max_size, max_execution_time, max_input_time).
- New agent flags: `-listen`, `-advertise`, `-sites-root` (+ env fallbacks);
  credentials file now carries the ops token; heartbeats advertise the
  management URL.

### 8.3 Website provisioning engine

- **Domains module**: normalized + whitelist-validated (rejects traversal,
  shell metacharacters, malformed labels, misplaced wildcards); types
  primary/alias/subdomain; one domain → at most one website.
- **Websites module**: statuses `provisioning → active | error`, `↔ disabled`,
  `→ deleting`; per-site document roots via a `SitePathResolver` with
  configurable roots (`/srv/panel/sites`, `C:\Panel\Sites`) and prefix-proof
  overrides.
- **Nginx generator** (pure, unit-tested): renders server blocks with
  validated domains/paths, per-site logs, PHP fastcgi pass, dotfile denial.
  Never interpolates unvalidated input.
- **Safe deployment**: agent writes candidate → `nginx -t` validates the full
  configuration → on failure the previous file is restored (rollback) → on
  success atomic deploy + enable + reload. One bad site can never take down
  the others.
- **PHP runtime per website**: Linux PHP-FPM pools (unix sockets, isolated
  pool files under `/etc/php/<v>/fpm/pool.d/epicpanel-<slug>.conf`, shared
  `epicpanel-sites` system user); Windows php-cgi FastCGI on loopback TCP
  ports with agent-managed processes. Same application interface on both.
- **Provisioning is a job** (`jobs` table): provision / reconfigure / delete
  types, real step progress (validate → directories → site user → PHP → nginx
  → reload → default page), in-process worker with SKIP LOCKED claiming,
  stale-run recovery after panel restarts, and idempotent steps so retries are
  safe.
- **Default page**: a clean "Welcome to your new website" page exposing only
  domain + PHP version — no IPs, paths or secrets.

### 8.4 Phase 2 UI

- **Servers page**: token-based enrollment flow (create/list/revoke, one-time
  reveal + ready-to-paste agent commands), capability matrix per server.
- **Domains page**: table + create modal (server/domain/type) + guarded
  deletes.
- **Websites list**: search, status filter, sortable columns, pagination,
  loading/empty/error states, quick actions.
- **Create wizard**: Domain → Runtime → Storage → Review → Provision →
  Complete, with live job progress (real percentages/messages from the jobs
  table), honest PHP discovery ("PHP is not installed on this server" when
  absent) and retry guidance on failure.
- **Website detail**: status header with Visit/Reload/Disable/Enable, config
  card (incl. alias + PHP management behind granular permissions), SSL/DNS/disk
  slots marked honestly, bounded **access/error log viewer** with line filter
  and auto-refresh, destructive delete dialog distinguishing "configuration
  only" vs "configuration + files".
- Toasts for operation feedback; new permission codes hide every action the
  actor cannot perform (backend re-checks each one independently).

### 8.5 Phase 2 known limitations (documented honestly)

1. Site isolation is "shared site user" on Linux (one `epicpanel-sites`
   account, dedicated dirs + FPM pools); per-site users/UIDs and Windows ACL
   pruning are future work. No strong isolation is claimed.
2. Windows php-cgi processes are agent-managed children without supervision;
   they are (re)started by provisioning/reconfiguration, not monitored.
3. Windows nginx integration expects `C:\nginx` (configurable) and the agent
   appends the `include sites/*.conf;` line into `http {}` once.
4. Linux nginx uses the sites-available/sites-enabled layout when present,
   otherwise stock `conf.d/*.conf`.
5. Per-site disk usage is not yet accounted for; the UI says so instead of
   estimating.
6. Rate limiting of provisioning endpoints relies on the global in-memory
   limiter; no per-endpoint quotas yet.
7. Jobs run in a single in-process worker; horizontal panel deployments would
   need claim leases (schema already supports SKIP LOCKED claiming).

---

## 9. Phase 3 — Monitoring, Telemetry & Server Health (shipped)

> Objective: answer "what is happening on my server right now, and what has
> happened over the last hour/day/week?" with real agent telemetry.

### 9.1 Agent telemetry subsystem (`agent/internal/monitoring/`)

- Build-tagged collectors: `linux.go` (/proc/stat CPU deltas incl. steal,
  /proc/loadavg, /proc/meminfo, /proc/mounts + statfs for real filesystems
  only, /proc/net.dev, /proc/[pid] bounded top-CPU/top-memory with jiffy
  deltas, services via systemctl → service script → /proc/comm probe) and
  `windows.go` (GetSystemTimes, GlobalMemoryStatusEx, GetLogicalDrives +
  GetDriveType + GetDiskFreeSpaceEx for fixed drives, GetIfTable, Toolhelp32 +
  GetProcessTimes + K32GetProcessMemoryInfo, SCM via svc/mgr for
  `EPICPANEL_AGENT_SERVICES`).
- Documented platform semantics: Linux `used = MemTotal − MemAvailable`
  (page cache is NOT "used"); Windows `used = TotalPhys − AvailPhys`. Load
  averages are Linux-only; Windows CPU user/system split stays nil. Unavailable
  metrics serialize as explicit `null` — never fabricated.
- `Runner`: interval clamped to 10–300 s (default 15 s, `-collect-interval` /
  `EPICPANEL_AGENT_COLLECT_INTERVAL`), non-overlapping collection (TryLock —
  a slow cycle skips a tick instead of piling up), monotonic **sequence
  counter persisted across restarts** (`telemetry_state.json`), **bounded
  buffer** (240 samples, drop-oldest), delivery every cycle with
  **exponential backoff** (2→32 ticks) and batch restore on failure. Telemetry
  can never crash the agent or block hosting operations.
- Pure parsers (`parse.go`) are unit-tested cross-platform; the Windows
  MIB_IFTABLE decoder is tested with synthetic buffers.

### 9.2 Transport & ingestion

- `POST /api/v1/servers/telemetry` reuses the **existing agent bearer-token
  store** (no second auth); unknown/revoked tokens are rejected.
- Batches: ≤60 samples, lists hard-bounded (24 disks / 32 interfaces /
  20 processes / 32 services); responses report
  `{accepted, duplicates, rejected}`.
- Clock handling: agent timestamps stored for drift diagnostics only;
  ordering is `server_received_at`; future timestamps beyond a 10-minute skew
  are clamped. Deduplication via UNIQUE `(server_id, sequence)`.
- pgx batch inserts; one failing row never aborts the batch.

### 9.3 Storage, aggregation, retention (migration `0004_phase3_monitoring.sql`)

- Narrow raw table `server_metric_samples` (scalar columns + JSONB lists),
  hourly/daily rollups `server_metric_hourly` / `server_metric_daily`
  (min/avg/max CPU & memory, max disk per mount), all keyed
  `(server_id, bucket)` with upserts — restarts and overlaps are safe.
- `MaintenanceWorker`: hourly tick, mutex-guarded non-overlap, backfills
  missed buckets on start, prunes per retention (settings keys
  `monitoring.retention_{raw,hourly,daily}_days`, defaults 7 / 30 / 365).
- History queries are always bounded: ranges `1h|6h|24h|7d|30d` only; ≤6h
  reads bucketed raw, 24h/7d hourly, 30d daily; responses capped at ~240
  points.

### 9.4 Health states & alerts

- Health = `healthy | warning | critical | unknown | offline`, averaged over
  the last 4 samples (a single noisy sample never flips state). Thresholds
  operator-tunable via settings (defaults CPU 80/95, Mem 80/95, Disk 80/90).
- Alert foundation: rules (threshold/duration/severity/enabled) + alerts with
  `triggered → acknowledged → resolved` lifecycle. **One active alert per
  (rule, server)** enforced by a partial unique index — re-evaluation
  refreshes `last_breached_at` instead of inserting. A breach requires ALL
  samples in the window above the threshold AND ≥2 samples spanning ≥80% of
  the duration. Recovery auto-resolves. Evaluation loop: 30 s, non-overlapping,
  fully persisted (no global in-memory state).
- Seeded rules: CPU >90% 5m critical · Memory >90% 5m critical · Disk >90% 10m
  warning · Server offline critical · Service stopped warning.

### 9.5 Monitoring UI

- **Server detail page** (`/app/servers/:id`) with permission-gated tabs:
  Overview (health card, CPU/Mem/Disk/Uptime metric cards, filesystem bars,
  interface counters), Monitoring (CPU/Memory/Load/Disk charts with threshold
  guides + 1h/6h/24h/7d/30d selector, per-interface RX/TX rate charts),
  Services, Processes (bounded top lists), Websites, Activity (per-server
  alerts with acknowledge).
- Dependency-free SVG `LineChart` (tooltips, time labels, threshold guides,
  loading/empty/error states, ≤240 points). Current metrics poll every 10 s;
  history is cached per range (`staleTime: 60s`) and never re-downloaded
  continuously.
- **Fleet dashboard**: servers summary (online/total), websites
  (active/total), fleet table (server, health badge, CPU, memory, disk,
  uptime) and a **recent alerts panel** (severity, server, rule, triggered,
  acknowledge / view server). Users without `monitoring.view` see the classic
  dashboard plus an honest permission notice.
- **Website health** card on the website detail page (nginx / PHP /
  configuration / server) derived server-side from provisioning state + the
  server's newest service snapshot — no external probes (`GET
  /websites/{id}/health`, `websites.view`).
- Capability matrix honesty: `monitoring_capabilities` marks metrics `ok`
  only when the latest sample actually produced data, `unsupported` when the
  platform lacks them, `error:<reason>` when a collector failed.

### 9.6 Panel-internal observability (§42 foundation)

- `internal/metrics` registry + chi middleware: API request counters by
  route pattern, response-class counters, average latency; gauges reserved
  for job queue depth / telemetry ingestion rate. Exposed to operators via
  `GET /system/internal-metrics` (`settings.manage`) — structured so a
  Prometheus bridge or admin dashboard can read the same registry later.

### 9.7 Phase 3 known limitations (documented honestly)

1. Network rate series are computed from raw counters and capped at the
   24h-capped raw window; long-range per-interface rates are not yet
   aggregated (hourly tables store first/last counters for future use).
2. Windows network counters are 32-bit GetIfTable values — interfaces
   wrapping 4 GB show one skipped interval (detectable, documented).
3. Windows CPU user/system split and load averages are not collected
   (would require PDH); they remain explicitly unavailable.
4. Phase 2 limitations are unchanged (shared site user, no per-site disk
   accounting, single in-process job/telemetry workers, etc.). No misleading
   "fully isolated" claims were added.
5. Alert notification channels (email/webhook/Slack/Discord) were not built
   in Phase 3; they ship in Phase 5 (§10 below). The alert lifecycle and
   audit hooks were the extension points.
6. The telemetry/ingestion path is compile-verified and unit-tested at the
   pure-function level; a full DB round-trip smoke test against live agents
   on Linux and Windows hosts remains operator work.

---

## 10. Phase 4 — SSL/ACME & Phase 5 — Notifications (shipped)

### 10.1 SSL / ACME (migration `0005_phase45_ssl_notify.sql`)

- **Per-website certificates**: `website_certificates` table (provider acme|mock,
  SAN domains, cert/key paths, auto_renew, issued/expiry). The website detail
  page gained an SSL card (issue / remove / auto-renew toggle).
- **Agent issuance** (`agent/internal/ssl`): real Let's Encrypt via
  `golang.org/x/crypto/acme` (HTTP-01 through the site webroot — challenge
  files placed under `.well-known/acme-challenge/`, no nginx reload needed for
  the challenge), plus **mock mode** that generates self-signed RSA-2048
  certificates offline for development panels without public DNS. The ACME
  account key persists under the agent data dir.
- **Modes** are operator-controlled via settings: `ssl.acme_mode`
  (`production` | `staging` | `mock`), `ssl.acme_email`, `ssl.auto_renew_days`
  (default 30).
- **Panel flow** (`internal/websites/ssl.go` + `issue_ssl` job): gather the
  mode/contact from settings → ask the agent to obtain the cert → store
  metadata → regenerate the nginx site config with `listen 443 ssl`, `http2`,
  `ssl_certificate(_key)`, an HTTP→HTTPS redirect and `fastcgi_param HTTPS on`,
  then validate + reload. **Auto-renewal** is driven by the maintenance worker
  for certificates expiring within the configured window.
- Agent stores certificates at `<nginxDir>/conf/ssl/<siteSlug>/` so nginx can
  always read them; `ssl/order` and `ssl/remove` ops endpoints.

### 10.2 Notifications (Phase 5)

- **Channels**: `notification_channels` table with types `webhook`, `slack`,
  `discord` and `email` (SMTP with STARTTLS + optional auth), each with a
  severity threshold (`warning` receives both; `critical` only critical).
- **Delivery** (`internal/notifier`): normalized `AlertPayload` posted to
  webhooks (Slack/Discord get their native `text`/`content` shapes) or sent
  via SMTP. Delivery runs as a `notify_alert` job through the runner's retry
  loop; successes are audited.
- **Alert lifecycle integration**: the evaluator now fires notifications on
  `triggered`, `acknowledged` and `resolved` events — one delivery per event,
  no storms.
- **Settings API**: `GET/PATCH /api/v1/settings` exposes operator keys (ACME
  mode/email, monitoring thresholds, retention days); unknown keys rejected.
- **Settings page** (`/app/settings`): ACME defaults + notification channel
  CRUD with a Test button.
- **Dev webhook echo**: in development, `POST/GET /dev/webhook` records
  received payloads in memory so notification delivery can be tested without
  external services.

### 10.3 Verification (this deployment)

- Agent mock issuance verified end-to-end: self-signed cert written to
  `C:\nginx\conf\ssl\epichostly.app\fullchain.pem` + `privkey.pem`, 90-day
  expiry.
- Alert → notification verified: toggling the server offline fired
  `server_offline`; the dev webhook received both `triggered` and `resolved`
  events; the alert auto-resolved when the agent heartbeat resumed.
- Migration 0005 applied (schema_migrations version 5; `website_certificates`
  and `notification_channels` present).

### 10.4 Phase 4/5 known limitations (documented honestly)

1. Real ACME requires a publicly resolvable domain + port 80 reachable from
   the internet; without those, issuance fails with the ACME server's error
   (use `mock` mode locally, or `staging` to avoid prod rate limits).
2. ACME HTTP-01 challenge is HTTP-only; DNS-01 is not implemented.
3. SMTP delivery assumes STARTTLS on the configured port; no implicit-TLS-only
   servers supported yet.
4. Notification channels are per-panel, not per-role/per-server; per-rule
   overrides are a future refinement.
5. Certificate private keys live on the agent in plaintext (0600), like the
   sites themselves; HSM/TPM backing is future work.
6. Renewal failures leave the previous certificate in place (nginx keeps
   serving it) and are logged — no fallback to HTTP is forced.

---

## 11. Phase 6 — Managed Databases (MySQL + PostgreSQL) (shipped)

### 11.1 Model (migration `0006_phase6_databases.sql`)

- `databases` (server_id, website_id?, engine mysql|postgres, name, status
  provisioning|active|error|deleting) with a unique `(server_id, engine, name)`
  index; `database_users` (database_id, username, status) with unique
  `(database_id, username)`. **Passwords are never stored.**
- New job types `provision_database` / `delete_database`; permissions
  `databases.manage` + `databases.users.manage` seeded (view/create/delete were
  seeded in Phase 1) and granted to super_admin.

### 11.2 Agent (`agent/internal/dbops`)

- DDL runs through **Go drivers** (`go-sql-driver/mysql`, `pgx`) — never a
  shell. A common `Ops` interface (Ping/Version/CreateDatabase/DropDatabase/
  DatabaseExists/ListDatabases/CreateUser/DropUser/SetUserPassword/Grant) with
  MySQL and PostgreSQL implementations.
- Strict identifier validation (`^[a-z][a-z0-9_]{0,62}$` db,
  `^[a-z][a-z0-9_]{0,31}$` user) + per-engine quoting; generated passwords use
  a symbol-free alphabet so they embed in DDL without escaping.
- MySQL users created for `localhost` + `127.0.0.1` and granted the DB;
  PostgreSQL roles granted the DB and set as owner (PG15+ schema access).
- Admin credentials come from agent flags/env (`-mysql-user`, `-pg-password`,
  …); an engine without credentials reports `configured: false` honestly.
- Ops endpoints: `GET /agent/v1/db/engines`, `POST /agent/v1/db/{create,drop}`,
  `POST /agent/v1/db/user/{create,drop,password}`.

### 11.3 Panel (`internal/databases`)

- Service + `provision_database`/`delete_database` job handlers; engine
  availability is probed via the agent before creation.
- API: `GET/POST /databases`, `GET/DELETE /databases/{id}`,
  `POST/DELETE /databases/{id}/users[/{userId}[/password]]`,
  `GET /servers/{id}/db-engines` — all RBAC-enforced, destructive ops audited.
- User creation / password rotation return the generated password **once**.

### 11.4 UI

- **Databases page** (`/app/databases`): searchable table (name, engine,
  server, status, users), create modal that detects available engines per
  server and can attach the DB to a website.
- **Database detail** (`/app/databases/:id`): connection details, users table
  with add / reset-password / remove, one-time credential reveal, danger-zone
  delete.
- **Website detail** gained a "Databases" section listing attached databases.
- Databases nav item enabled (was a Phase 2 placeholder).

### 11.5 Verification (this deployment)

- PostgreSQL engine probe live: `{postgres: {configured:true, available:true,
  version:"PostgreSQL 16.4 …"}}`.
- Full cycle verified through the agent: create DB → create user (20-char
  password) → confirmed in `pg_database`/`pg_roles` → drop DB. Cleaned up.
- Backend build/vet/tests green (new `databases` suite); agent cross-compiles
  all 4 targets + `dbops` tests green; frontend build + 36 tests green.

### 11.6 Phase 6 known limitations (documented honestly)

1. MySQL/MariaDB is code-complete and unit-tested but was not live-tested here
   (no MySQL server installed on the dev box); PostgreSQL was verified
   end-to-end.
2. DB admin credentials live on the agent (flags/env), not in the panel DB —
   rotate by restarting the agent. A panel-managed credential store is future
   work.
3. No per-database size/quota yet (lands with Phase 9 resource limits).
4. Connection is localhost-only by design; remote DB access / external hosts
   are out of scope.
5. No phpMyAdmin/Adminer web admin bundled (candidate for a later phase).

---

## 12. Phase 7 — Software Manager (shipped)

> Philosophy: install EpicPanel first; install the hosting stack later, from
> the UI, only when the administrator chooses.

### 12.1 Agent engine (`agent/internal/software`)

- **OS + package-manager abstraction** (`os.go`): detects Debian/Ubuntu → apt,
  RHEL/Rocky/Alma/Fedora → dnf, SUSE → zypper, Windows → winget. No
  Ubuntu-specific commands scattered through the code.
- **Allowlisted execution** (`exec.go`): only a fixed set of binaries may run,
  always with provider-defined argv — never a string from a request. Timeouts,
  exit-code capture, no shell. This is the core safety guarantee.
- **Provider registry** (`provider.go`): Nginx, Apache, MariaDB, Redis, PHP,
  Node.js, Java, Docker — each with per-manager install argv, a service name,
  and a detect command. Remove is derived from install.
- **Manager** (`manager.go`): List (live detect: presence + version + running),
  Install (then enable+start the service), Remove, Service (start/stop/restart/
  enable/disable/status).

### 12.2 Panel (`internal/software`)

- Component state is **detected live from the agent** (no stale cache).
- Install/remove run as **jobs** (`install_software` / `remove_software`,
  migration `0007_phase7_software.sql`) so long operations never block HTTP and
  the UI can poll real progress. Service control is a direct quick op.
- API: `GET /servers/{id}/software`, `POST /servers/{id}/software/{install,
  remove,service}` — gated `server.view` / `server.manage`, audited.

### 12.3 UI + CLI

- **Software tab** on the server detail page: components grouped by category
  with Install / Remove / Start / Stop / Restart, live re-scan, and job
  progress.
- **`epicpanel` CLI** (`agent/cmd/epicpanel`): `status`, `doctor`, and
  `software list|install|remove|service` — reusing the **same** `software.Manager`
  engine as the web UI (no duplicated logic). `doctor` prints ✓/✗ checks with
  fix hints for panel service, health, privileges, OS, and each component.

### 12.4 Phase 7 known limitations (documented honestly)

1. Installs require the agent to run as root/admin (package managers need it).
2. Providers use distro default packages (e.g. `docker.io`, `nodejs`); pinned
   versions / third-party repos (nodesource, official Docker repo, ondrej PHP)
   are future refinements.
3. Dependency resolution (e.g. "install Laravel → ensure PHP+Composer+DB") is
   not yet built; components are installed individually.
4. Windows software install relies on winget being present.
5. No config backup/rollback around installs yet (the nginx site path has it;
   package installs do not).
