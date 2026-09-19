# EPICPANEL

Persistent source of truth for EpicPanel's architecture, decisions, implementation state, and roadmap.
This file exists so any future AI-agent session (or human) can enter the repository with ZERO prior context and continue correctly.

**Before ANY development work: read this file, inspect the code, compare documented state vs. actual state, and continue from there. Never redesign architecture casually. Update this file when an important decision or state change occurs.**

---

## 1. Project Vision

EpicPanel is a **Linux-first, next-generation hosting control panel and infrastructure platform** — not a simple cPanel clone and not a Linux dashboard.

It is intended to support:

* hosting companies
* multiple customers (multi-tenant)
* multiple websites
* multiple servers
* multiple web servers (OpenLiteSpeed, Nginx, Apache)
* multiple runtime versions (PHP, Node.js, Python, Go, static)
* databases (MySQL, MariaDB, PostgreSQL)
* domains / DNS
* SSL certificates
* staging environments
* deployments
* backups
* monitoring
* public APIs / automation
* future AI agents (via a controlled tool registry, never a root shell)

EpicPanel must be capable of starting on a single small server (1 server, 10 websites) while having an architecture capable of scaling to professional hosting infrastructure (100 servers, 10,000+ websites) **without architectural rewrites**.

---

## 2. Current Scope

* **V1 is Linux-only.**
* Windows is NOT part of V1 and must not influence the initial Linux architecture.
* The architecture may remain extensible for future platforms, but Linux is the primary and optimized target.

---

## 3. Technology Stack

| Layer | Choice |
|---|---|
| Frontend | React + TypeScript + Vite + Tailwind CSS |
| Backend (control plane) | Go |
| Server Agent | Go (`epicpanel-agent`) |
| Control-plane database | PostgreSQL |
| OS target | Linux only (V1) |

Architecture principles:

* API-first (the frontend is a client of the API)
* Control plane / data plane separation
* Server agent (no SSH-as-architecture)
* Provider abstractions
* Desired state + reconciliation
* Idempotent provisioning
* Modular monorepo — no premature microservices
* No Rust unless a concrete technical requirement justifies it

---

## 4. Core Architecture

```
Frontend (React)
   |
   v
API (/v1)
   |
   v
CONTROL PLANE (Go)
   +-- Authentication / Authorization
   +-- Organizations / RBAC
   +-- Websites
   +-- Domains / DNS
   +-- Runtime Manager
   +-- Database Manager
   +-- SSL
   +-- Deployments / Staging
   +-- Backups
   +-- Monitoring
   +-- Jobs / Events / Audit
   +-- AI (future)
   |
   v
Jobs / Events
   |
   v
EpicPanel Server Agent (Go)  — one per managed Linux server
   |
   v
Linux Server (data plane)
   +-- Web servers (OLS / Nginx / Apache)
   +-- Managed runtimes (shared)
   +-- Isolated workloads (per-tenant)
   +-- Databases
```

**Control plane** — owns metadata and desired state: users, orgs, roles, permissions, servers, agents, websites, domains, runtimes, databases, SSL, deployments, jobs, events, audit, API credentials.

**Data plane** — actually runs customer workloads: websites, PHP-FPM pools, Node/Python/Go processes, static content, databases, web servers, filesystems.

The control plane must NOT depend on arbitrary SSH commands as its primary architecture. A dedicated, authenticated agent performs server-side operations.

---

## 5. Critical Architectural Invariants

These rules must never be casually violated.

### Invariant 1 — User isolation
Every customer/workload must be isolated from other customers. A customer must not be able to read/modify another customer's files, control their processes, access their databases, or access another organization's resources. Authorization is enforced **server-side**, never only in the frontend.

### Invariant 2 — Shared managed runtimes + isolated execution contexts
Do NOT install a complete copy of PHP/Node/Python/etc. inside every user environment.

```
PHP 8.3 runtime (shared, centrally managed)
   +-- User A PHP-FPM pool (isolated)
   +-- User B PHP-FPM pool (isolated)
   +-- User C PHP-FPM pool (isolated)
```

**RUNTIME IS SHARED. EXECUTION IS ISOLATED.**

### Invariant 3 — Multiple runtime versions
EpicPanel must support multiple versions of the same runtime (e.g. PHP 8.1/8.2/8.3/8.4). Websites select a version. The website model must not assume a single version. Supported versions are configuration-driven, not hardcoded.

### Invariant 4 — Web-server abstraction
Never hard-code OpenLiteSpeed. Use a `WebServerProvider` interface (OpenLiteSpeed, Nginx, Apache). Provider-specific config lives inside provider implementations only.

### Invariant 5 — Database abstraction
Never assume MySQL is the only database. Use a `DatabaseProvider` interface (MySQL, MariaDB, PostgreSQL). EpicPanel's own control-plane DB is PostgreSQL; customer databases are independent managed resources.

### Invariant 6 — API-first
Everything important must be possible through the versioned API (`/v1/...`). No infrastructure logic in React. The UI requests "Create Website", not "run these Linux commands".

### Invariant 7 — Server agent
The agent exposes typed operations (`CreateWebsite`, `InstallRuntime`, `CreateDatabase`, `IssueCertificate`, `DeployApplication`, ...) — never "execute arbitrary root command" as the fundamental API. The agent may run system commands internally, but they are generated by trusted provider implementations.

### Invariant 8 — Idempotent provisioning
Provisioning operations must be safely retryable. Partial failure must not create duplicate users/config/databases/certificates. Explicit lifecycle states: `pending → provisioning → configuring → validating → ready` plus `failed / deleting / deleted`.

### Invariant 9 — Security
No future AI agent gets unrestricted root shell access. AI operates through a Tool Registry → Permission Layer → EpicPanel API/Agent, with validation, tenant scoping, and auditing on every tool.

---

## 6. Multi-Tenancy Model

```
User
  |
  v
Organization
  +-- Members (Owner / Administrator / Developer / Billing / Support)
  +-- Websites
  +-- Domains
  +-- Databases
  +-- Servers
  +-- Deployments
  +-- Backups
  +-- API tokens
```

* Ownership model is **User → Organization → Resources** — never User → Website directly.
* Organizations may contain multiple users with roles (RBAC; exact model may evolve).
* Every resource has an ownership/scope; authorization is enforced server-side.

---

## 7. Website Model

A website is a **first-class managed infrastructure resource**, not a directory on disk.

```
Website
+-- Organization
+-- Server
+-- Domain(s) (primary, aliases, subdomains)
+-- Web server (provider + config)
+-- Runtime + version
+-- Filesystem (dedicated, permissioned)
+-- Process configuration (PHP-FPM pool / app process)
+-- Environment variables
+-- Resource limits (CPU, memory, pids, fds, disk)
+-- SSL certificates
+-- Database connections
+-- Deployment configuration
+-- Staging relationships
+-- Backups
```

---

## 8. Runtime Manager

EpicPanel owns and manages runtime versions centrally (e.g. `/epicpanel/runtimes/php/8.3/` — exact layout may change, conceptual ownership must not).

```
Runtime Manager
+-- PHP      8.1 / 8.2 / 8.3 / 8.4
+-- Node.js  20 / 22 / 24
+-- Python   3.11 / 3.12 / 3.13
+-- Go
+-- Static
```

The manager tracks per runtime version: path, binaries, config, extensions, health, availability, compatibility, install/update state. Supported versions are configuration-driven.

---

## 9. Isolation Model

Use mature Linux primitives — do not invent custom mechanisms, do not blindly choose containers/VMs.

Planned primitives:

* dedicated Unix users per website/tenant
* filesystem permissions + quotas
* cgroups / systemd resource controls (CPU, memory, pids, fds)
* namespaces where appropriate
* process ownership (e.g. per-site PHP-FPM pools running as the site's user)

Decision (Phase 3/4, implemented): **dedicated Unix user per website + filesystem permissions + per-site PHP-FPM pools (cgroup/systemd limits planned)**. Verified live: shared `php-fpm8.3` master runs isolated pools per site, each pool executing as the site's own system user with its own socket (`/run/epicpanel/php-fpm/<website_id>.sock`), open_basedir confinement, disabled exec functions, per-pool memory limits. The same runtime binary serves many isolated pools — `RUNTIME IS SHARED, EXECUTION IS ISOLATED`.

Status: **Planned** (nothing implemented yet).

---

## 10. Server Agent

`epicpanel-agent` — a Go binary installed on every managed Linux server.

Responsibilities: website provisioning, runtime management, web-server configuration, users/permissions, PHP-FPM pools, app processes, resource limits, metrics, health checks, deployments, backups, state reconciliation, failure reporting.

Typed operations (agent protocol, Phase 0/2 to formalize):

```
CreateWebsite, DeleteWebsite, InstallRuntime, CreateRuntimePool,
CreateDatabase, ConfigureDomain, IssueCertificate, DeployApplication,
RestartApplication, ReadApplicationLogs, CreateBackup, RestoreBackup
```

Communication: secure authenticated protocol (TLS) with the control plane. Not designed as a generic shell-execution service.

Status: **Planned** — protocol to be defined in Phase 0/2 and documented here once implemented.

---

## 11. Provider Architecture

Provider interfaces (to be defined as Go interfaces in Phase 0/1+):

```
RuntimeProvider       (PHP, Node.js, Python, Go, Static)
WebServerProvider     (OpenLiteSpeed, Nginx, Apache)
DatabaseProvider      (MySQL, MariaDB, PostgreSQL)
DNSProvider           (abstract; providers later)
CertificateProvider   (e.g. Let's Encrypt; replaceable)
BackupProvider
DeploymentProvider
```

Core business logic operates on abstract capabilities; provider-specific behavior stays inside providers. Providers must be replaceable without rewriting core domain logic.

---

## 12. Desired State

The control plane stores desired configuration; the agent observes actual state and reconciles.

Example:

```
Website:
  domain: example.com
  webServer: openlitespeed
  runtime: php
  version: 8.3
  ssl: enabled
  resources:
    memory: 1GB
```

More robust than one-time shell scripts. Which resources support reconciliation will be documented here as implemented. Status: **Planned**.

---

## 13. Jobs

Long-running operations must not block API requests: website provisioning, runtime installation, SSL issuance, deployment, backup/restore, staging clone, service restart, DNS configuration.

```
POST /v1/websites  →  returns resource/job state (202-style), not a blocking response
```

Frontend receives status via polling initially (simplest reliable option); SSE/WebSockets possible later.

Jobs record: state, progress, errors, retry status, timestamps. Status: **Planned**.

---

## 14. Events

Domain events designed in from the beginning (clean abstraction first, no heavy distributed platform prematurely):

```
website.created / website.updated / website.deleted / website.provisioning_failed
deployment.started / deployment.completed / deployment.failed
certificate.issued / certificate.renewal_failed
server.connected / server.offline
```

Events support future notifications, automation, monitoring, billing, AI, audit. Status: **Planned**.

---

## 15. API

Versioned from day one:

```
/v1/auth
/v1/users
/v1/organizations
/v1/servers
/v1/websites
/v1/domains
/v1/dns
/v1/runtimes
/v1/databases
/v1/ssl
/v1/backups
/v1/deployments
/v1/monitoring
/v1/jobs
```

Conventions: resource-oriented; authentication, authorization, pagination, filtering, sorting, validation, structured errors, idempotency where appropriate, audit info. API tokens have scopes, expiration, revocation, audit. Raw secrets are never recoverable after creation.

**No endpoints are implemented yet. Do not document imaginary endpoints as completed.**

---

## 16. Security

| Mechanism | Status |
|---|---|
| Authentication (users, bcrypt + sessions) | **Implemented** |
| Session revocation / logout | **Implemented** |
| Authorization (server-side org RBAC) | **Implemented** |
| Audit logging (auth + org + member events) | **Implemented (foundation)** |
| API tokens (scopes/expiry/revocation) | Planned |
| Secret management / encryption at rest | Planned |
| TLS (control plane ↔ agent) | Planned |
| Agent authentication | Planned |
| Filesystem restrictions / path traversal prevention | Planned |
| Process isolation (Unix users, cgroups) | Planned |
| Rate limiting / brute-force protection on auth | Planned |
| Tenant boundaries | Implemented (org-scoped queries + 404 cloaking) |

Principles: least privilege; the agent is highly privileged so it must be strongly authenticated, input-validated, and audited. Never assume frontend validation is security. Prevent path traversal (`../../`), command injection, and privilege escalation.

---

## 17. AI — Future

AI is a **future phase** (Phase 13). Do not integrate AI deeply before the control plane is stable.

```
AI Agent
   |
   v
Tool Registry (filesystem.read/write, logs.read, website.create/update,
               deployment.create, database.inspect, backup.create, service.restart)
   |
   v
Permission Layer
   |
   v
EpicPanel API / Agent
```

Every tool: validates arguments, enforces authorization, tenant scope, resource scope, produces audit records, returns structured results. AI never bypasses platform APIs. Status: **Planned**.

---

## 18. Development Phases

```
Phase 0  — Architecture (domain model, schema, API model, agent protocol, provider interfaces, isolation, security)   [x]
Phase 1  — Foundation (auth, users, orgs, RBAC, PostgreSQL, migrations, API framework, audit)                          [x]
Phase 2  — Server Management (registration, agent auth, heartbeat, health, metrics, inventory)                         [x]
Phase 3  — Website Engine (website resource, filesystem, Unix user, isolation, limits, lifecycle, idempotency)         [x]
Phase 4  — Runtime Manager (PHP first: registry, versions, FPM pools, per-site config, health)                         [x]
Phase 5  — Web Server Providers (OpenLiteSpeed, Nginx, Apache via provider interfaces)                                 [x] (nginx done; OLS/Apache pending)
Phase 6  — Databases (MySQL, MariaDB, PostgreSQL)                                                                      [x] (MariaDB live-verified; MySQL same code path; PostgreSQL implemented, smoke pending)
Phase 7  — Domains / DNS / SSL                                                                                          [x] (domains+aliases, DNS verification, selfsigned live-verified; Let's Encrypt via lego implemented, needs public-server smoke)
Phase 8  — Deployment / Staging (git deploy, clone, promote)                                                            [x] (git deploy + rollback live-verified; staging clone/promote implemented)
Phase 9  — Backups (scheduled, files, DBs, retention, restore, remote storage)                                          [x] (files+db backups, retention, restore live-verified; remote storage pending)
Phase 10 — Monitoring (metrics, health checks, alerts)                                                                  [x] (http health checks, alert transitions, metrics history; server-side up)
Phase 11 — Public API (tokens, scopes, rate limiting, OpenAPI docs)                                                     [x]
Phase 12 — Multi-Server Scaling (scheduling, resource-aware placement)                                                  [x] (auto-placement, maintenance mode, capacity API)
Phase 13 — AI (tool registry, permissions, sessions, approvals, audit)                                                  [ ] (explicitly deferred by owner)
```

Work phase-by-phase. At each phase: inspect existing architecture → define interfaces → smallest correct foundation → tests → integrate → verify security boundaries → document → continue.

---

## 19. CURRENT IMPLEMENTATION STATE

```
Current Phase:          NEW 15-PHASE PLAN (phases/) — Phases 5 (Customer
                        cPanel) + 9 (Resource & Limit Engine) DONE (2026-09-08;
                        ADR-048/049/050; Phase 5+9 ran in parallel: 5 = frontend
                        only, 9 = backend only). Next: 6 → 7 → 8 → 10 …
Audit report:           docs/architecture-report.md (Phase 1 baseline; ADR-038)
Last Completed Task:    Phase 5 + Phase 9 (session 2026-09-08): Phase 5 —
                        frontend monorepo packages/{core,ui,icons,charts,
                        tables,forms,design-system} + apps/customer (14 screens,
                        WS-live dashboard w/ freshness badges, email deferred —
                        no backend; terminal/sshkeys server authz raised
                        dev→admin) + UX kit for Phase 6. Phase 9 — internal/
                        resources engine + agent enforcement (cgroups v2
                        slices, XFS/ext4 quotas, nftables accounting, FPM
                        max_children, fail-closed count gates), anti-drift by
                        construction, over-limit suspend policy, migration 0027
                        (7 verbatim plans). Phase 3 metrics pipeline (session
                        2026-09-07):
                        agentproto v1 stream (GET /v1/agent/stream; seq +
                        ring-buffer resume replay, ingest dedup + gap
                        counters), agent loop decoupled (stream/job/legacy-
                        heartbeat goroutines), delta-based CPU + network +
                        disk-IO + swap + inodes + TCP + services + per-site
                        cgroups + containers + app envelopes (MC/Discord
                        reserved), self-throttling collector, in-memory
                        LiveStore as sole live source (LIVE/STALE/OFFLINE +
                        age + node_state registry), WS push via /v1/ws
                        (BroadcastMetrics), async batched history writer +
                        24h raw retention + 5-min rollup 30d (migration
                        0022), frontend WS client + useMetrics/useFreshness
                        + FreshnessBadge, polling removed. Tests: reconnect
                        no-gaps/no-dupes, high-load ingest (-race), stale
                        timing, e2e agent-stream → live → REST/WS, history
                        persistence + rollup. Protocol doc:
                        docs/protocol-metrics.md.
Next Task:              Phase 6 Admin WHM (needs Phase 5 packages — DONE),
                        then 7 Minecraft + 8 Discord (consume Phase 9 engine).
                        See phases/README.md execution order:
                        6 → 7 → 8 → 10 → 11 → 12 → 13 → 14 → 15.
Blockers:               None. (Migrations 0023–0027 shipped with Phases 4/9.)
Known Bugs:             See §21 (Phase 1 audit; several items fixed in Phase 2
                        and marked there).
```

---

## 20. COMPLETED FEATURES

```
[x] Repository initialized (Go backend, module: github.com/epicbyte/epicpanel/backend)
[x] PostgreSQL connection (pgx/v5 pool)
[x] Embedded SQL migrations + minimal runner (schema_migrations table)
[x] Authentication (bcrypt password hashing, register/login/logout/me)
[x] Sessions (opaque tokens, SHA-256 hashed in DB, cookie + Bearer support, revocation)
[x] First-user bootstrap (first registered user becomes platform admin)
[x] Organizations (CRUD, slug auto-generation + validation)
[x] RBAC (owner/admin/developer/billing/support, server-side rank checks, last-owner protection)
[x] Audit foundation (audit_logs table, best-effort recording of auth + org + member events)
[x] API framework (stdlib ServeMux, structured JSON errors, request logging, request IDs)
[x] Server management (register server, one-time registration tokens with rotation, agent enrollment, agent tokens)
[x] Agent heartbeat + metrics ingestion (server_metrics table, online/offline status derived from heartbeat freshness)
[x] Server agent binary (cmd/agent: poll loop, heartbeat w/ /proc metrics, typed job execution)
[x] Jobs system (atomic claim via FOR UPDATE SKIP LOCKED, retry with max_attempts, per-website job history)
[x] Website engine (org-scoped websites, desired-state provisioning, unix user per site, idempotent provision/delete)
[x] Runtime Manager (registry per server, PHP install via distro/PPA packages, refcount-protected removal)
[x] PHP-FPM pools (per-site pool files, atomic write + -t validation before swap, dedicated socket per site, live version switching with stale-pool cleanup)
[x] Job outcome fanout (single agent result endpoint; subsystems subscribe via OnJobFinished)
[x] Web server serving (WebServerProvider interface; Nginx provider: vhost render for static+php, atomic write + nginx -t before reload, symlink enable, delete cleanup; web_server field on websites incl. `none`)
[x] Databases (DatabaseProvider ops: engine install via apt, db+user create/drop via native clients with regex-validated identifiers; AES-GCM credential vault with EPICPANEL_SECRET_KEY, plaintext scrubbed from jobs, audited reveal endpoint; website linking)
[x] Domains (first-class resources; GLOBAL uniqueness — one domain one website; primary + aliases; vhost reconcile on change)
[x] DNS verification (agent-side resolve + server-IP match; RunCloud/Ploi model; DNS record hosting deferred to future DNSProvider integration)
[x] SSL (CertificateProvider: self-signed in-process; Let's Encrypt via lego HTTP-01 webroot through nginx; per-domain ssl_mode/state/expiry)
[x] SSL-aware vhosts (443 block + 80→443 redirect for secured domains + ACME webroot location; SNI; TLSv1.2/1.3)
[x] Renewal scheduler (hourly control-plane loop: certs <30d expiry re-enqueued; also refreshes server online/offline statuses; now also due backup schedules)
[x] Deployments (git https deploys, atomic release dirs via symlink swap, .env/uploads carry-over, first-deploy migration of manual content, release pruning keep-5, rollback to last successful, encrypted deploy tokens)
[x] Staging (one staging env per site: own unix user/pool/vhost, files cloned + databases cloned via root-socket dump/restore — no credentials cross the wire; promote = admin-only, new release + db restore)
[x] Backups (manual + scheduled daily/weekly with retention 1-30, site tar + per-db gz dumps, manifest, restore re-extracts + re-imports; retention prunes rows+archives)
[x] Monitoring (60s HTTP health checks on primary domains, alerts on down/up transitions w/ auto-resolve, unresolved-alert dedupe unique index, metrics history endpoint)
[x] API tokens (epk_ prefix, SHA-256 hashed, scopes read/write per resource group, expiry, revocation, audited; org-confinement regardless of creator memberships)
[x] Rate limiting (token-bucket: 10/min burst 20 on auth, 300/min burst 600 on API, per client IP / per org+token)
[x] Platform admin API keys (epa_): cross-org machine principals with admin:read/admin:write for the /v1/admin* surface + org scopes elsewhere; key create/revoke session-only; full API automation guide docs/api-reference.md; organizations-route token confinement fixed (RequireMinimumRole)
[x] OpenAPI 3.0.3 spec served at /v1/openapi.json (168 paths / 215 ops; regenerated via internal/api/openapi_gen.py — edit the route table, run the script)
[x] Auto-placement (websites may omit server_id; least-loaded online non-maintenance server, runtime-version-aware, CPU 60%/memory 40% weighted score)
[x] Server maintenance mode (blocks job claims + placement; PATCH endpoint, audited)
[x] Capacity API (per-server status/loads/site counts/eligibility for admin+)
[x] CORS middleware (EPICPANEL_CORS_ORIGINS; defaults to localhost:5173 dev origins, credentials enabled)
[x] Agent CLI lifecycle (`epicpanel-agent enroll --url <panel> --token <reg>` exchanges token + persists /etc/epicpanel/agent.env; `epicpanel-agent run` starts from env or persisted config; verified live)
[x] cPanel account model (ADR-030): platform admins create customer accounts (POST /v1/admin/users), sites are created ONLY by admins/API (server-side 403 for members), admins operate across all orgs via RBAC bypass, customers see only their assigned org's resources; admin "Manage Accounts" page in UI
[x] Hosting packages (ADR-031): admin-defined plans (max sites/databases, disk MB, FPM memory + max_children, allowed runtimes, price) + Starter/Pro/Enterprise seeds; org assignment reconciles existing sites via apply_quota agent jobs (pool limits rewritten atomically + reloaded — live-verified 128M/10 -> 64M/3); site/db creation enforced against package limits server-side (403 with plan name); Packages admin UI with assign wizard
[x] File manager (backend: list/read/write/create/rename/delete/download per-site tree, strict path-containment w/ symlink resolution — traversal returns 403; 2MB editor limit, 32MB uploads, whole-site tar.gz download; frontend: browser-based file manager page w/ breadcrumbs, editor modal, upload, rename, per-site access from Sites rows)
[x] Frontend v1 — EpicHost UI (frontend/): login/register, dark sidebar shell per design spec, dashboard (metric cards, websites list, quick actions, server status rings, activity, getting started, help), Sites (create w/ auto-placement + PHP version, filter, delete), Databases (create/attach/reveal creds/delete), Servers (admin-only: register w/ one-time token wizard, per-server rings, maintenance), Activity (audit feed), Team (add/remove members), Backups (per-site history), Security, Settings (API token create/revoke wizard); skeletons + empty states + responsive; lucide-react icons; builds clean (88 KB gzip)
[x] Integration tests (full API flow + server/website/runtime/vhost/database/domain+SSL/deployment+staging/backup+monitoring/token+scope/scheduler+placement lifecycles against live PostgreSQL)
[ ] OpenLiteSpeed / Apache providers (interface ready)
[x] Node.js runtime (NodeSource; tolerant of broken apt repos), Python (apt), Go (official toolchain — newest patch of a minor auto-resolved from go.dev, versioned install + /usr/local/bin symlinks; live-verified go 1.22)
[x] Database tools (phpMyAdmin + Adminer): agent job deployes both under /srv/epicpanel/dbadmin, dedicated www-data FPM pool + nginx vhost on port 8081; POST /v1/organizations/{org}/servers/{id}/database-tools (admin+); live-verified both serving 200
[x] Runtimes version check constraint relaxed to allow node-style majors (22.0)
[x] Kernel-enforced sandbox layer (ADR-036/037, internal/isolation module): bubblewrap mount+PID namespaces for ALL shell sessions (SSH + web terminal) — site tree at /site writable, /usr+/bin+/lib read-only, minimal /etc, /srv+/root+/home+other sites NOT MOUNTED (invisible, not hidden); PID namespace blocks cross-tenant process visibility/kill; no-new-privs + nosuid blocks setuid escape; rlimits (nproc/nofile/as/fsize/cpu) kernel-enforced per session; sandbox spec files (/etc/epicpanel/sandbox/<id>.json) = documented integration contract for the software agent's runtime mounts; live-verified escape matrix over real SSH (shadow/srv/root invisible, ps=5, rlimits active)
[x] SSH access + restricted shell (ADR-034): per-site SSH keys (ssh_keys table, 10-key cap, format validation + SHA256 fingerprints) synced into the site user's authorized_keys with no-port-forwarding options; login shell is epicpanel-shell (Go restricted shell — blocks sudo/su/systemctl/network servers, allows site work); keys enable SSH+SFTP as the SITE USER only; nologin when zero keys; live-verified end-to-end (whoami as site user, sudo blocked)
[x] Web terminal (xterm.js over WebSocket — ADR-035: RequestLog's statusRecorder now implements Hijack/Flush or WS upgrades 500; vite dev proxy needs ws:true; live-verified full round-trip as site user, pty via creack/pty; runs the restricted shell as the site user in the site docroot via setpriv; limits: developer+ interactive sessions only (no API tokens), 2 sessions/site, 32 global, 16KB message cap, 10-minute idle timeout, audited open/close/timeout)
[x] Cron jobs (per-site scheduled commands; 5-field cron validation + shell-injection guards — no chaining/pipes/backticks/sudo; agent renders the site user's crontab atomically from desired state (ADR-033); hourly re-sync safety net; UI: presets, pause/resume, delete)
[x] Site management hub (cPanel-style): /sites/:id page per site — quick stats, domains list with SSL state, tool grid (Files/Cron/SSH/Terminal/Backups/SSL), attached databases, WordPress install, admin-only delete; site name click from Sites list opens hub
[x] WordPress one-click (wp-cli download, core download + config + install as site user via sudo, admin password returned in job result; auto-creates fresh MariaDB DB through the pipeline; refuses double-install; UI: WordPress button on ready PHP sites)
[ ] PostgreSQL database-engine live smoke (implemented; MariaDB verified live)
[ ] Let's Encrypt live issuance (needs internet-reachable server; code path complete, ACME staging via EPICPANEL_ACME_DIRECTORY)
[ ] Remote backup storage (S3/SFTP via BackupProvider abstraction)
[ ] Notification channels for alerts (email/Slack; alerts table ready)
[ ] Deployments / Staging
[ ] Backups
[ ] Monitoring
[ ] Public API / OpenAPI
```

### Implemented API endpoints (Phase 1)

```
GET  /healthz                                              health (DB ping)
POST /v1/auth/register                                     create user (first = platform admin)
POST /v1/auth/login                                        session login (cookie + token)
POST /v1/auth/logout                                       revoke session
GET  /v1/auth/me                                           current user (auth required)
POST /v1/organizations                                     create org (creator becomes owner)
GET  /v1/organizations                                     list orgs of current user
GET  /v1/organizations/{org_id}                            org details (billing+)
PATCH /v1/organizations/{org_id}                           rename org (admin+)
GET  /v1/organizations/{org_id}/members                    list members (billing+)
POST /v1/organizations/{org_id}/members                    add member by email (admin+)
PATCH /v1/organizations/{org_id}/members/{user_id}         change member role (admin+)
DELETE /v1/organizations/{org_id}/members/{user_id}        remove member (admin+, last owner protected)
GET  /v1/audit-logs?organization_id=...                    audit trail (org members billing+; all orgs for platform admin)

Phase 2 (servers + agent):

POST /v1/organizations/{org_id}/servers                              register server (admin+)
GET  /v1/organizations/{org_id}/servers                              list org servers (billing+)
GET  /v1/organizations/{org_id}/servers/{server_id}                  server details (billing+)
DELETE /v1/organizations/{org_id}/servers/{server_id}                delete server (admin+)
POST /v1/organizations/{org_id}/servers/{server_id}/registration-token  rotate one-time registration token (admin+)
GET  /v1/organizations/{org_id}/servers/{server_id}/metrics          latest metrics (billing+)
POST /v1/agent/enroll                                                exchange registration token for agent token (no auth)
POST /v1/agent/heartbeat                                             agent liveness + metrics (Bearer agent token)

Phase 3 (websites + jobs):

POST /v1/organizations/{org_id}/websites                             create website (developer+) -> 202 + job_id
GET  /v1/organizations/{org_id}/websites                             list org websites (billing+)
GET  /v1/organizations/{org_id}/websites/{website_id}                website details (billing+)
DELETE /v1/organizations/{org_id}/websites/{website_id}              request deletion -> delete job (admin+)
GET  /v1/organizations/{org_id}/websites/{website_id}/jobs           job history (billing+)
POST /v1/agent/jobs/claim                                            agent claims next pending job (Bearer agent token)
POST /v1/agent/jobs/{job_id}/result                                  agent reports job outcome (Bearer agent token)

Phase 4 (runtimes):

GET  /v1/organizations/{org_id}/servers/{server_id}/runtimes          list runtimes on server (billing+)
POST /v1/organizations/{org_id}/servers/{server_id}/runtimes          request install {type, version} (admin+) -> 202
DELETE /v1/organizations/{org_id}/servers/{server_id}/runtimes/{id}   request removal (admin+; 409 while in use) -> 202

PATCH /v1/organizations/{org_id}/websites/{website_id}                change runtime_version -> reconcile job (developer+)

Phase 5 (web servers):

websites carry `web_server` field: nginx (default) | openlitespeed | apache | none
POST /v1/organizations/{org_id}/websites {web_server: ...}            chosen at creation
(nginx installed + configured agent-side during provision_website jobs;
 vhost files /etc/nginx/sites-{available,enabled}/epicpanel-<id>.conf)
```

Serving model (live-verified): nginx vhost per website → `fastcgi_pass unix:/run/epicpanel/php-fpm/<website_id>.sock` (PHP sites) or static `try_files` (static sites); dotfile access denied; per-site access/error logs under the site tree. Deletion removes the vhost and reloads; unmatched hosts fall through to the distro default site.

Phase 6 (databases):

POST /v1/organizations/{org_id}/databases                             create {name(label), server_id, engine, website_id?} (developer+) -> 202
GET  /v1/organizations/{org_id}/databases                             list (billing+)
GET  /v1/organizations/{org_id}/databases/{db_id}                     details (billing+)
DELETE /v1/organizations/{org_id}/databases/{db_id}                   request drop (admin+) -> 202
GET  /v1/organizations/{org_id}/databases/{db_id}/credentials         reveal decrypted credential (developer+, audited)

Database names/users derived: `ep_<org8>_<label>` (regex-validated, collision-safe per server+engine). Credentials: generated agent-side, returned in job result, AES-GCM encrypted into `databases.password_encrypted`, plaintext scrubbed from the jobs table, reveal is an explicit audited API call.

Phase 7 (domains + SSL):

GET  /v1/organizations/{org_id}/websites/{website_id}/domains          list domains (billing+)
POST /v1/organizations/{org_id}/websites/{website_id}/domains          add alias {domain, kind} (developer+)
DELETE /v1/organizations/{org_id}/websites/{website_id}/domains/{id}   remove alias (admin+)
POST /v1/organizations/{org_id}/domains/{domain_id}/ssl                set ssl_mode {none,selfsigned,letsencrypt} (developer+)
POST /v1/organizations/{org_id}/domains/{domain_id}/verify-dns         queue DNS verification (developer+)

Domains are globally unique across the panel. SSL issuance is an issue_certificate agent job; vhost reconciles automatically on alias/SSL changes. Cert renewal: hourly scheduler re-enqueues letsencrypt certs expiring <30d. Agent ACME config: EPICPANEL_ACME_EMAIL, EPICPANEL_ACME_DIRECTORY (override for staging).

Phase 8 (deployments + staging):

PATCH /v1/organizations/{org_id}/websites/{website_id}/deployment-config   {repo_url, branch, deploy_token?} (developer+)
POST /v1/organizations/{org_id}/websites/{website_id}/deploy               trigger deploy (developer+) -> 202
POST /v1/organizations/{org_id}/websites/{website_id}/rollback             rollback to last successful (admin+) -> 202
GET  /v1/organizations/{org_id}/websites/{website_id}/deployments          deployment history (billing+)
POST /v1/organizations/{org_id}/websites/{website_id}/staging              create staging env + clone (admin+) -> 202
POST /v1/organizations/{org_id}/websites/{website_id}/promote              promote staging -> production (admin+) -> 202

Deploys: release dirs under /srv/epicpanel/releases/<site>/ with atomic symlink activation, keep-5 pruning; deploy tokens stored AES-GCM encrypted, injected into clone URL agent-side only. Staging databases use <name>_stg suffix; clone/promote dump+restore via root sockets (no db credentials in payloads).

Phase 9-10 (backups + monitoring):

POST /v1/organizations/{org_id}/websites/{website_id}/backups              manual backup (developer+) -> 202
GET  /v1/organizations/{org_id}/websites/{website_id}/backups              backup history (billing+)
PATCH /v1/organizations/{org_id}/websites/{website_id}/backup-config       {schedule: off|daily|weekly, retention: 1-30} (admin+)
POST /v1/organizations/{org_id}/backups/{backup_id}/restore                restore (admin+) -> 202
GET  /v1/organizations/{org_id}/alerts                                     alerts (unresolved by default; ?resolved=true) (billing+)
GET  /v1/organizations/{org_id}/websites/{website_id}/health               last 50 http checks (billing+)
GET  /v1/organizations/{org_id}/servers/{server_id}/metrics/history        last 200 metric points (billing+)

Backups: tar.gz of site tree + gzipped db dumps + manifest under /srv/epicpanel/backups/<backup_id>/; scheduled backups run hourly-scan (due daily/weekly); retention prunes oldest rows+archives. Monitoring: health checker (interval EPICPANEL_HEALTH_CHECK_INTERVAL, default 60s) GETs primary domains, records http_checks, raises/resolves website.down alerts (unique unresolved per resource).

Job types (current): `provision_website`, `delete_website`, `install_runtime`, `remove_runtime`, `create_database`, `delete_database`, `issue_certificate`, `verify_domain`, `deploy_website`, `rollback_website`, `clone_staging`, `promote_staging`, `create_backup`, `restore_backup`. Failed jobs retry up to max_attempts (3) then fail terminally. All agent job results go through ONE endpoint (`/v1/agent/jobs/{id}/result`); subsystem state machines (websites, runtimes, databases, domains, deployments, backups) subscribe via fanout callbacks (OnJobFinished + OnJobClaimed).

Phase 11-12 (public API + multi-server):

POST /v1/organizations/{org_id}/api-tokens                             create token {name, scopes[], expires_in_days?} (admin+) — raw shown once
GET  /v1/organizations/{org_id}/api-tokens                             list tokens (admin+; never raw)
DELETE /v1/organizations/{org_id}/api-tokens/{token_id}                revoke (admin+)
GET  /v1/openapi.json                                                  OpenAPI 3.0.3 spec (public)
POST /v1/organizations/{org_id}/websites (server_id omitted)           auto-placement (least-loaded eligible server)
GET  /v1/organizations/{org_id}/servers/capacity                       placement overview (admin+)
PATCH /v1/organizations/{org_id}/servers/{server_id}/maintenance       {enabled} (admin+)

API tokens: `epk_`-prefixed, SHA-256 hashed at rest, scopes enforced per method+path (GET->read, mutations->write; deployments/backups/domains/monitoring refinements under /websites). Tokens are CONFINED to their issuing organization even if the creator belongs to others (creator losing membership also kills org access). Rate limits: auth 10/min (burst 20) per IP; API 300/min (burst 600) per client — in-memory buckets, single-instance; front with a shared limiter for multi-instance control planes.

Platform admin API keys (full-panel automation):

POST /v1/admin/api-keys                                               create key {name, scopes["*"|...], expires_in_days?} (admin SESSION only) — raw epa_ shown once
GET  /v1/admin/api-keys                                               list keys (admin session or epa_ with admin:read)
DELETE /v1/admin/api-keys/{key_id}                                    revoke (admin SESSION only)

epa_ keys act as the platform admin: any org's routes (with the matching org scope) and the /v1/admin* + /v1/jobs + /v1/settings surface (with admin:read/admin:write). Org tokens epk_ remain org-confined; tokens can never mint or revoke epa_ keys. Scope map: deny-by-default per method+path (x-scope in openapi.json); org:read/org:write now also cover /v1/organizations (list/create) and /v1/organizations/{org} (details/rename); billing:read/billing:write cover the org-side billing surface.

Agent protocol (current): agent enrolls once with a one-time `reg_...` token → receives a persistent `agt_...` token (only its SHA-256 hash is stored) → sends heartbeats with `Authorization: Bearer agt_...` including optional metrics. Servers show `pending` (registered, not enrolled), `online` (heartbeat within 2 minutes), or `offline` (stale).

---

## 21. KNOWN ISSUES

Phase 1 audit (2026-09-07) found the documented "None yet" was false. Full detail in
`docs/architecture-report.md`. Items by phase owner (**bold FIXED in Phase 2**, session 2026-09-07):

- **~~Metrics stale/inaccurate under load~~ FIXED** (ADR-046: decoupled
  agent loops, delta metrics, LiveStore + freshness labels, WS push,
  retention + rollups). Remaining: Redis-backed multi-process fan-out when
  the control plane scales beyond one instance (noted in protocol doc).
- **~~Cross-org server fleet takeover~~ FIXED** (server mutations now admin-session-only),
  **~~token admin-inheritance + fail-open scopes~~ FIXED** (ADR-043), **~~public register never
  disabled~~ FIXED** (setup_completed lockdown), **~~no job lease/reaper~~ FIXED** (ADR-044).
- **~~Scheduled backups dead on arrival~~ FIXED** (ADR-044; detect-software + WP DB FK class fixed
  via NULL created_by).
- **~~CSRF/CORS/XFF-trust/body-limit gaps~~ FIXED** (ADR-045). Remaining hardening (2FA UX polish,
  PMA-gate host pinning, setup verify-hostname oracle, WP password scrub, service-account audit
  surface) → Phase 12.
- **Injection gaps**: nginx rewrite-snippet smuggling **FIXED (control plane + agent)**; cron
  `$(` **FIXED**; backup-restore `bash -c` interpolation still open — Phase 11.
- **Secret leaks**: apps `env_vars` **FIXED** (encrypted at rest, keys-only GET); WP admin password
  in job results **MITIGATED** (developer+ gate) — full scrub Phase 12; SSO key plaintext in
  system_settings — Phase 12.
- **Unbounded growth**: jobs **FIXED** (30d prune); server_metrics/http_checks/audit_logs/
  deployments.log — Phase 3.
- **Connection leak** in metrics-history endpoint (`monitoring/handler.go:144`, unclosed rows) — Phase 3.
- **Isolation gaps**: no seccomp, host network shared, cron/app processes unsandboxed — Phase 9.
- **Doc-vs-code divergence is systemic**: trust `docs/architecture-report.md` over prose; ADR-037
  referenced but never written (numbers now continue 041+).

---

## 22. ARCHITECTURAL DECISIONS

```
ADR-001
Decision: Use Go for the control-plane backend and the server agent.
Reason:   Strong concurrency, excellent Linux tooling, single language across
          backend + agent, fast builds/deployment, sufficient system capability.
Status:   Accepted

ADR-002
Decision: Centrally managed runtime binaries with isolated execution pools.
Reason:   Avoid duplicating full PHP/Node/Python installs per customer (RAM, disk,
          update complexity) while preserving per-tenant process/filesystem isolation.
Status:   Accepted

ADR-003
Decision: Dedicated EpicPanel Agent with typed operations; no SSH-as-architecture.
Reason:   Security, auditability, idempotency, structured errors; SSH as the core
          mechanism is untyped, unauditable, and unsafe at scale.
Status:   Accepted

ADR-004
Decision: PostgreSQL as the control-plane database.
Reason:   Relational integrity for metadata (orgs, RBAC, resources, jobs, events).
          Customer databases remain independent managed resources via providers.
Status:   Accepted

ADR-005
Decision: Modular monorepo; no premature microservices.
Reason:   Start simple, keep clean domain/API boundaries so services can be split
          later only when operational/scaling requirements justify it.
Status:   Accepted

ADR-006
Decision: React + TypeScript + Vite + Tailwind frontend as a pure API client.
Reason:   Modern DX; UI holds zero infrastructure logic.
Status:   Accepted

ADR-007
Decision: Session auth via opaque random tokens stored SHA-256-hashed in the DB
          (cookie + Bearer), bcrypt for passwords, stdlib ServeMux + pgx/v5 for
          the API layer, embedded SQL migrations with a minimal runner.
Reason:   No heavyweight frameworks; tokens are unrecoverable from DB;
          migration runner avoids external tooling dependency; pgx is the
          standard high-performance PostgreSQL driver for Go.
Status:   Accepted

ADR-008
Decision: Organization membership RBAC via role ranks; non-members receive 404
          (not 403) for organization resources.
Reason:   Prevents leaking organization existence across tenants; rank model
          (owner > admin > developer > billing = support) keeps server-side
          authorization simple and auditable.
Status:   Accepted

ADR-009
Decision: Agent authentication via one-time registration tokens exchanged for
          persistent per-server agent tokens (SHA-256 hashed at rest), with
          heartbeat-based online/offline status derived at read time.
Reason:   No agent secrets stored in plaintext; enrollment is single-use and
          rotatable; status computation avoids background scanners in V1.
Status:   Accepted

ADR-010
Decision: Agent endpoints (/v1/agent/*) authenticate via Bearer agent tokens,
          separate from user session auth; servers are org-scoped resources.
Reason:   Agents are machine principals; mixing them into session auth would
          conflate trust domains. Org scoping of servers prepares multi-server
          placement (Phase 12).
Status:   Accepted

ADR-011
Decision: Website provisioning is asynchronous via a jobs table; agents claim
          work with SELECT ... FOR UPDATE SKIP LOCKED and report results.
Reason:   Infrastructure operations must not block API requests (ep-plan §17);
          SKIP LOCKED makes claiming safe under concurrency/retries without a
          message broker; jobs are auditable and retryable (max_attempts=3).
Status:   Accepted

ADR-012
Decision: The agent executes only typed operations and never a shell; payloads
          carry identifiers, not commands. Provisioning is idempotent (existing
          user/dirs are detected and reused).
Reason:   Security boundary (ep-plan §3/§28): command injection is impossible
          via payload because argv is constructed in code from validated
          inputs; idempotency makes retry-safe reconciliation possible.
Status:   Accepted

ADR-013
Decision: Website filesystem layout is /srv/epicpanel/websites/<website_id>/
          {public,logs,tmp}, owned by a dedicated per-site system user
          (ep-<org8>-<name>, useradd --system --no-create-home, nologin shell);
          the agent reports the actual document_root back (actual state wins).
Reason:   UUID-based paths survive renames; dedicated user per site is the
          Phase 3 isolation primitive; control plane stores desired state and
          the agent owns filesystem layout details (provider boundary seed).
Status:   Accepted

ADR-014
Decision: PHP runtimes are installed via OS packages (distro repos first,
          Ondřej Surý PPA fallback on Debian/Ubuntu), not compiled from source;
          a failing `apt-get update` (broken third-party repo) is tolerated —
          installs proceed from cached indexes.
Reason:   This is how production panels actually manage PHP versions; source
          builds are fragile and slow. Tolerating partial repo failures
          matches operator practice in the field.
Status:   Accepted

ADR-015
Decision: One job-result endpoint; subsystem state machines subscribe via a
          fanout callback (api wires websites + runtimes transitions).
Reason:   Phase 4 initially had a separate runtime-result endpoint the agent
          never called — registry stuck on `installing` (caught in live
          smoke test). Single report path eliminates out-of-order/dropped
          notification classes of bugs.
Status:   Accepted

ADR-016
Decision: FPM pool files are written atomically (tmp+rename) and the whole
          fpm config is validated (`php-fpm{v} -t`) BEFORE the swap; on
          failure the previous file is restored. Version switches remove the
          site pool from every other PHP version dir and reload those masters.
Reason:   One bad pool must never take down other sites on the master; stale
          pools across versions cause "another FPM instance already listens
          on socket" crashes (both caught live).
Status:   Accepted

ADR-017
Decision: WebServerProvider interface on the agent (Ensure/Remove per site);
          Nginx is the first implementation. Vhosts are prefixed
          `epicpanel-<website_id>` in sites-available + symlink in
          sites-enabled, written atomically and validated with `nginx -t`
          before reload; on validation failure the previous vhost is restored.
          `web_server=none` serves nothing (files-only websites allowed).
Reason:   Same reload-safety pattern as PHP pools: a bad vhost must never
          take down other sites on the box. Prefix namespace makes stale-file
          cleanup trivial and keeps coexistence with hand-managed vhosts.
Status:   Accepted

ADR-018
Decision: Database credentials are generated agent-side, returned once in the
          job result, encrypted with AES-GCM (key from EPICPANEL_SECRET_KEY)
          into the databases table, and the plaintext is scrubbed from the
          jobs table. Reveal is an explicit, audited API call.
Reason:   Panels need credential retrieval for app configuration, but plaintext
          at rest in the jobs table was an unacceptable leak surface (caught
          during Phase 6 design review). Missing secret key → ephemeral key
          with a loud warning (dev-only).
Status:   Accepted

ADR-019
Decision: DB identifiers are panel-derived (`ep_<org8>_<label>`) and
          regex-validated before reaching the agent; agent-side SQL is built
          only from these validated identifiers via native clients
          (mysql/psql), never free-form user SQL.
Reason:   Eliminates SQL injection into administrative statements; deterministic
          naming gives per-server uniqueness and easy ownership tracing.
Status:   Accepted

ADR-020
Decision: Domains are panel-globally unique (one domain can only belong to one
          website), primary domains auto-registered at website creation and
          not deletable (delete the website instead).
Reason:   Prevents cross-tenant domain hijacking; the vhost and SSL state are
          keyed to the domain, so ambiguous ownership would corrupt both.
Status:   Accepted

ADR-021
Decision: DNS record hosting is deferred (future DNSProvider integration);
          Phase 7 ships agent-side DNS verification (resolve + compare to
          server IPs) and SSL via ACME HTTP-01 webroot through nginx.
Reason:   Agent-based panels (RunCloud/Ploi class) do not run nameservers;
          hosting DNS without a nameserver component would produce
          non-functional records. Verification + HTTP-01 covers the SSL flow.
Status:   Accepted

ADR-022
Decision: SSL via CertificateProvider abstraction: self-signed (in-process
          crypto) and Let's Encrypt (lego, HTTP-01 webroot). Vhost rendering
          includes a dedicated redirect block for secured domains and serves
          the ACME challenge webroot on both port-80 blocks. Certificate
          activation re-triggers the vhost reconcile.
Reason:   Mixed secured/plain domains need separate server blocks (a shared
          block would redirect plain domains too); the initial Phase 7 smoke
          caught a stale-vhost bug — activation must converge the vhost.
Status:   Accepted

ADR-023
Decision: Git deployments use immutable release directories
          (/srv/epicpanel/releases/<site>/<timestamp-id>) with an atomic
          symlink swap for activation; last 5 releases retained; rollback
          repoints the symlink at the last successful release. First deploy
          migrates manually-provisioned content into `initial-manual`,
          carrying over only .env/uploads/storage (repo content wins).
Reason:   Zero-downtime atomic activation, instant rollback, disk-bounded
          history. Caught live: symlink-over-directory rename failure and
          merge-overwriting-repo — both fixed with the migration/carry-over
          split. Shallow clone falls back to full clone (dumb-HTTP remotes).
Status:   Accepted

ADR-024
Decision: Staging clone/promote move database data via engine root sockets
          (mysqldump/psql on the box); database credentials never appear in
          job payloads or cross the API. Staging DB names get a _stg suffix;
          staging sites are full websites (own user/pool/vhost) with
          is_staging/staging_of linkage.
Reason:   The agent already runs with root socket access; piping credentials
          through the control plane would widen the secret surface for zero
          benefit. Full-site staging keeps isolation invariants intact.
Status:   Accepted

ADR-025
Decision: Backups are site tar.gz + per-database gz dumps + manifest.json
          under /srv/epicpanel/backups/<backup_id>/; schedules (daily/weekly)
          and retention (1-30) are per-website; the hourly scheduler enqueues
          due backups and prunes beyond retention. Restore re-extracts and
          re-imports (database drop+create+import).
Reason:   Simple, transparent, restorable-by-hand archives; per-site retention
          matches hosting plans; remote storage (S3/SFTP) remains a future
          BackupProvider without changing the archive format.
Status:   Accepted

ADR-026
Decision: Monitoring is a control-plane HTTP health checker (default 60s)
          against primary domains of ready websites, recording http_checks and
          managing website.down alerts with unresolved-alert dedupe (partial
          unique index) and auto-resolve on recovery.
Reason:   Black-box HTTP checks catch the failure class customers actually
          notice (site down) without agent-side daemons; the unique index
          makes alert raising idempotent under concurrency.
Status:   Accepted

ADR-027
Decision: API tokens (epk_) are org-confined: authorization checks resolve
          against the token's issuing organization, never the creator's other
          memberships, and the creator's own membership still gates access
          (removal from the org kills the token's reach).
Reason:   Live smoke caught a cross-tenant leak: a platform admin's token
          could read every org they belonged to. Tokens are automation
          identities FOR one org, not super-credentials for their owner.
Status:   Accepted

ADR-028
Decision: Rate limiting via in-memory token buckets keyed by client IP
          (port stripped) — auth 10/min burst 20, API 300/min burst 600.
Reason:   Port-per-connection keys made the limiter useless (caught live);
          IP keys collapse client connections correctly. Single-instance
          simplicity now; documented Redis/shared-limiter upgrade path.
Status:   Accepted

ADR-036
Decision: Shell access is confined with bubblewrap (kernel namespaces): the
          site tree binds at /site (writable), /tmp is private per site,
          /usr+/bin+/lib and a minimal /etc are read-only — /srv, /root and
          other sites are NOT in the namespace at all. Site base dirs are
          provisioned 0750. The web terminal and SSH sessions both run
          through this sandbox.
Reason:   Live incident: the command-blocklist approach was bypassable
          (also: shell resolve errors now include the uid so a missing/
          deleted site tree is diagnosable from the error alone)
          (bash spawns unrestricted shell; cd /etc readable). Mount-level
          confinement cannot be escaped by spawning shells; 0750 stops
          cross-site reads even outside the sandbox. Verified: /etc/shadow,
          /srv and other sites' files all invisible; site work unaffected.
Status:   Accepted

ADR-035
Decision: Middleware wrappers must implement http.Hijacker (and Flusher)
          delegation; the vite dev proxy sets ws:true; gorilla upgrader
          rejects hijack-less writers with HTTP 500.
Reason:   Live incident #2: the web-terminal path runs epicpanel-shell
          INSIDE the pre-built sandbox — that shell must detect
          EPICPANEL_SANDBOX=1 and act as the plain interactive shell instead
          of re-resolving /srv (not mounted in the namespace) and failing.
          Live incident: RequestLog's statusRecorder silently broke every
          WebSocket upgrade with a 500. ResponseWriter wrappers must preserve
          the optional interfaces the underlying writer supports. Second live
          incident: gorilla's default CheckOrigin rejected the dev-server
          origin and the failed upgrade left the connection hanging —
          CheckOrigin now allows all (auth is via session cookie) and failed
          upgrades are logged. Requires `make run` restart to take effect.
Status:   Accepted

ADR-034
Decision: SSH/terminal access is key-only, per-site, and runs as the site's
          Unix user through a restricted shell (epicpanel-shell) — never as
          root, never with passwords, API tokens excluded from the web
          terminal. authorized_keys carry no-port-forwarding/no-X11/no-agent
          restrictions; zero keys disables the shell (nologin). Web terminal
          adds idle timeout + session caps + auditing.
Reason:   Users need real shell access for site work (composer, wp-cli, git),
          but the isolation boundary must hold: the site user's filesystem
          permissions are the sandbox, the restricted shell blocks privilege
          and network-server escape vectors, and everything is audited.
Status:   Accepted

ADR-033
Decision: Cron management stores desired entries in cron_jobs and the agent
          renders the SITE USER's crontab atomically (crontab -u), with
          command validation rejecting chaining/pipes/backticks/sudo, and an
          hourly scheduler re-sync as a safety net.
Reason:   Per-user crontabs keep commands running as the isolated site user
          (no root, no shared crontab); validation blocks the obvious
          injection/escape vectors; desired-state re-sync heals drift.
Status:   Accepted

ADR-031
Decision: Hosting packages live in a hosting_packages table (limits: sites,
          databases, disk MB, FPM memory/children, allowed runtimes) assigned
          per organization with a default fallback; assignment enqueues
          apply_quota jobs that patch existing sites' FPM pool files
          atomically and reload; creation-time enforcement returns 403 with
          the plan name. Disk is usage-accounted (agent reports used_mb);
          hard enforcement is a control-plane check against that number.
Reason:   Operator requested cPanel-style plans. FPM-level limits give real
          per-plan memory/process isolation without root fs quota support
          requirements; usage accounting works on any filesystem.
Status:   Accepted

ADR-032
Decision: apply_quota pool patching clamps spare-server settings relative to
          max_children and validates the whole fpm config with restore-on-
          failure (same safety pattern as EnsurePool).
Reason:   Live incident: patching max_children=2 left spares at 1/3, fpm -t
          failed, the 8.5 master crashed and took DB tools + php sites down.
          Config math (spares <= children) must be part of any pool rewrite.
Status:   Accepted

ADR-030
Decision: Sites are created exclusively by platform administrators or scoped
          API tokens; customers receive accounts (created by admins) and
          manage assigned resources. Platform admins bypass org membership
          checks (cPanel root model).
Reason:   Operator requirement: hosting company controls provisioning;
          customers never self-provision. Server-side 403 (not just UI).
Status:   Accepted

ADR-029
Decision: Auto-placement scores online, non-maintenance servers by
          0.6*CPU% + 0.4*mem% (latest metrics, neutral 50% without data);
          optionally requires a runtime version; static sites skip the
          runtime filter. Maintenance mode also blocks agent job claims.
Reason:   Keeps 1-server installs trivial while enabling N-server growth
          without changing the API contract (server_id simply optional).
Status:   Accepted

ADR-038
Decision: Phase 1 codebase audit completed; `docs/architecture-report.md`
          is the authoritative baseline for all later phases. No production
          code changed during the audit. Module classification: 16 KEEP /
          15 REFACTOR / 2 REWRITE / 0 REMOVE (12 dead artifacts) / 16 MISSING.
Reason:   Master-doc Phase 1 contract: recover architecture + safe incremental
          rework plan before touching functionality. Audit verified documented
          claims against code and found systemic doc-vs-code divergence (see
          ADR-039) — future sessions must trust the report, not prose.
Status:   Accepted

ADR-039
Decision: Metrics staleness root cause is architectural, not cosmetic: the
          agent runs ONE goroutine (heartbeat, then synchronously drains the
          job queue), so any busy queue head-of-line-blocks heartbeats and
          freezes metrics exactly under load; the only CPU figure sent is a
          since-boot lifetime average (single /proc/stat sample); network is
          never sampled; and no layer labels values LIVE/STALE/OFFLINE+age.
          Fix is scoped to Phase 3 (agent loop rewrite + metrics pipeline),
          NOT a display patch.
Reason:   Global contract #4 (instantaneous != historical, every live value
          carries freshness) is currently violated end-to-end. Confirmed by
          tracing cmd/agent/main.go:107-126, internal/agent/client.go:194-213,
          internal/servers/store.go:205-226. Per-site cgroup delta math
          (site_usage.go) already proves the correct pattern.
Status:   Accepted

ADR-040
Decision: Deferred to Phase 2 (decide once, do not drift): (a) server tenancy
          model — routes are org-scoped but the store is platform-wide
          (servers/store.go:246-274 ignores org), enabling cross-org server
          takeover; recommendation is platform-wide fleet + admin-only server
          routes, then remove the org pretense. (b) API version path /v1 vs
          /api/v1. (c) deny-by-default token scope map + tokens must never
          inherit platform-admin. (d) shared authz middleware package to kill
          the 10 duplicated requireOrg copies that already drifted into
          authz holes.
Reason:   These are architecture decisions with cross-phase blast radius;
          Phase 1 is audit-only. Recorded here so Phase 2 inherits them as
          explicit open decisions rather than rediscovering the bugs.
Status:   Accepted (deferral)

ADR-041
Decision: The canonical public API prefix is /api/v1 (master doc verbatim).
          httpapi.APIPrefixRewrite rewrites /api/v1/* to the /v1 handlers so
          both prefixes serve identically with zero route duplication; the
          /v1 spelling remains valid for existing clients and the frontend.
          The embedded OpenAPI spec is already stale (42 paths vs ~120 wired
          routes) — regeneration needs tooling and is tracked for a later
          phase rather than hand-edited.
Reason:   Phase 2 contract requires deciding the prefix ONCE. A rewrite
          middleware keeps one authoritative route table; hand-maintaining
          two route tables or a mass rename both risk drift.
Status:   Accepted

ADR-042
Decision: Event bus = durable `events` table + Driver interface with two
          implementations: Postgres LISTEN/NOTIFY (default) and Redis
          pub/sub (EPICPANEL_REDIS_URL, go-redis v9.5). WS endpoint GET /v1/ws
          streams org-scoped JSON frames (session or token auth; tokens
          scoped to their org; origin checked against the CORS allowlist).
          NOTIFY payloads are capped at ~7KB (payload dropped, table is
          source of truth).
Reason:   Global contract #2 (API → Job → Queue → Agent → Execution → Event →
          WebSocket → UI) needs an event channel before Phase 3's metrics
          protocol. Postgres NOTIFY keeps single-instance installs dependency-
          free; Redis scales the pub/sub when the operator already runs it.
          Boring and swappable per contract #8.
Status:   Accepted

ADR-043
Decision: RBAC v2: granular permission strings (internal/permissions) are the
          long-term authorization vocabulary; the org-role rank model stays
          for existing routes. Mapping (no grants dropped): Super Admin =
          is_platform_admin, Admin = org admin, Reseller = new org role
          'reseller' (rank 3, between admin and developer), Customer = org
          owner, Support = org support; developer/billing preserved. API
          tokens NEVER inherit platform-admin (Resolve sets IsAdmin=false;
          RequireMinimumRole/audit-logs/actorIsOwner all exclude tokens;
          /auth/me returns a token identity without the creator's flag).
Reason:   Phase 1 audit S1/S2: tokens were cross-org master keys and the
          scope map failed open. Deny-by-default ScopeEnforce + the role
          mapping closes both without breaking the existing surface.
Status:   Accepted

ADR-044
Decision: Jobs gain idempotency keys (partial unique index on
          (idempotency_key) WHERE status IN (pending,running); ON CONFLICT
          returns the live job), per-type leases stamped on claim, exponential
          retry backoff via visible_after, a 30s lease reaper (running jobs
          with expired leases → pending/failed), a 30-day retention prune, a
          fast scheduler loop (30s: statuses + reaper) beside the hourly one,
          and event emission on claim/success/failure/requeue. Scheduled
          backups fixed: DueBackup carries server_id, system backups use NULL
          created_by (runtimes/databases created_by relaxed to NULL).
Reason:   Audit: agent death mid-job wedged jobs and every entity state
          machine forever; the hourly status loop left dead agents "online"
          for up to an hour; scheduled backups never ran (two FK violations
          the docs claimed were live-verified). Retry-visibility scales
          (30s unit overridable via SetBackoffUnit for tests).
Status:   Accepted

ADR-045
Decision: 2FA foundation without new dependencies: stdlib RFC-6238 TOTP
          (SHA-1, 6 digits, ±1 window), secrets stored secretbox-encrypted
          (base64 TEXT), login becomes two-step for MFA users (hashed
          one-shot mfa_challenges, 5-minute TTL) with 10 hashed single-use
          recovery codes. Disabling requires the account password. CSRF:
          cookie-authenticated mutations require the X-EpicPanel header
          (Bearer auth exempt); CORS refuses "*" with credentials; XFF is
          only honored from EPICPANEL_TRUSTED_PROXIES peers (rightmost
          non-trusted hop wins; rate-limit keys and audit IPs share the one
          httpapi.ClientIP implementation).
Reason:   Audit S3/S9/S11 + master doc "2FA foundation". Zero-dependency
          TOTP avoids toolchain churn (go-redis pinned to 1.22-compatible
          v9.5.1 as it is); header-based CSRF is the pragmatic defense while
          the frontend is a same-origin SPA.
Status:   Accepted

ADR-046
Decision: Phase 3 real-time metrics pipeline. Dedicated persistent agent
          stream: GET /v1/agent/stream (WebSocket, agent token) speaking
          agentproto v1 (internal/agentproto — hello/resume/metrics/
          heartbeat + welcome/ack/ping; session-scoped monotonic seq, 256-
          frame ring replay on reconnect, ingest-side dedup + visible gap
          counter). Agent architecture: three decoupled loops — metrics
          stream (5s default, self-throttling when collection overruns the
          interval: interval doubles to 30s cap, degraded flag + warning,
          halves back on recovery), job worker (unchanged claim/execute),
          legacy HTTP heartbeat only while the stream is DOWN (fixes the
          head-of-line starvation that froze heartbeats behind 10-minute
          jobs). CPU/network/disk-IO are two-sample deltas over the window,
          never since-boot averages; collection list covers node (CPU, RAM,
          swap, load, disks+inodes, IO, net RX/TX, TCP, procs, systemd
          services), per-site cgroup slices, Docker containers (systemd +
          cgroupfs drivers, 30s-cached name resolution), and per-app units
          with kind inference (app/minecraft/discord; players/tps/mspt and
          restart_count reserved in the protocol for Phases 7/8). Control
          plane: in-memory LiveStore (internal/metrics) is the ONLY source
          for live reads — REST /metrics and batched /servers/metrics serve
          LIVE/STALE/OFFLINE + age_ms + node_state (ONLINE ≤30s, STALE
          ≤120s, else OFFLINE) straight from memory; WS /v1/ws pushes every
          ingested frame (BroadcastMetrics, no DB write in the live path)
          plus server_state transitions. Historical path is async-only:
          bounded queue (2000) → 8s pgx Batch flushes → server_metrics
          (extended: swap, net rates, IO, TCP, procs, inodes, degraded —
          migration 0022), raw pruned at 24h, server_metrics_rollup_5m
          built hourly and kept 30 days; history API ?range=7d|30d reads
          the rollup; audit's monitoring connection leak fixed (QueryRow
          instead of unclosed Pool.Query rows). Frontend: singleton WS
          client with exponential backoff, useMetrics/useFreshness hooks
          (client-side 1s age recompute so pushed frames degrade LIVE→STALE
          →OFFLINE without traffic), FreshnessBadge with the verbatim
          "updated 240ms ago"/"last update 18.4s ago" formats, per-server
          metrics N+1 removed; legacy MetricPoint shapes still normalized
          for agents that never stream.
Reason:   Master doc Phase 3 (priority phase) + architecture report §★:
          heartbeat starvation behind synchronous jobs, since-boot CPU
          averages, network never collected, invisible staleness, unbounded
          server_metrics. Push pipeline (Agent → stream → LiveStore → WS →
          React) with freshness labels is the doc's verbatim contract; the
          async writer keeps expensive persistence out of request handlers.
Status:   Accepted
```

ADR-047
Decision: Phase 4 web-hosting engine. (1) COMMON WEB-SERVER ABSTRACTION
          (internal/agent/webserver.go): WebServerProvider interface
          (Name/Ensure/Remove) with NginxProvider + ApacheProvider +
          OLSProvider adapters over the shared engines; all filesystem
          paths injectable (tests use temp dirs + stub binaries);
          dead proxy_ops.go deleted; dbadmin vhost/pool writers deduped
          onto the shared pipeline. (2) CONFIG-SAFETY PIPELINE
          (internal/agent/atomic.go): AtomicWriteFile (tmp+rename in
          same dir) → SwapValidated (in-memory backup → atomic write →
          validate under 120s cap → restore-on-failure) → ValidateCmd
          (argv-only exec, timeout, tail-500). nginx -t / apache2ctl
          configtest / php-fpm -t all timeout-bounded; Apache reloads
          instead of restarting when the vhost exists with an unchanged
          Listen port; OLS gains in-memory backup + restore+retry rollback
          (was zero validation). One shared rewrite-core
          (sanitizeRewriteLine) enforces per-provider allowlists + the
          same smuggling checks across nginx/Apache/OLS. (3) PROVIDER
          RENDER MATRIX: redirects (from/to/status 301|302|307|308) and
          suspended (503 "Account suspended" stub, no PHP/proxy) rendered
          natively by all 3 providers; ProvisionPayload carries redirects/
          suspended/fpm_memory_limit_mb/fpm_max_children. (4) FTP/SFTP
          (internal/ftpaccounts + agent/ftp_ops.go, migration 0023):
          per-site accounts (10/protocol cap), SHA-512-crypt $6$ one-way
          hashes travel in job payloads (NEVER plaintext; plaintext shown
          once at create/rotate/reveal, audited); SFTP via sshd Match
          blocks + internal-sftp ForceCommand (no chown of site dirs;
          chroot deliberately omitted — documented); FTP via vsftpd
          virtual users (PAM pwdfile, dedicated service drop-in,
          prefix-scoped merge so a site sync never touches other sites'
          accounts); sshd -t validated with rollback. (5) DNS ZONES
          (internal/dns + agent/dns_zone_ops.go, migration 0024):
          per-website zones with 8 record types (strict per-type
          validation, CNAME exclusivity, 100-record cap, serial bump on
          every change), publish = sync_dns_zone job rendering RFC1035
          zone files under /var/lib/epicpanel/dns-zones + a global BIND
          include file (MergeZoneDeclarations keeps only still-existing
          zone files), named-checkzone validated with rollback, rndc
          reload never restarts named. (6) REDIRECTS (domains/redirects,
          migration 0025): domain-level redirect rules attached to the
          site's own domains, carried in DesiredPayload.redirects and
          rendered by all providers. (7) LIFECYCLE (websites/lifecycle +
          agent/lifecycle_ops.go, migration 0026): suspend/resume as
          first-class agent jobs (idempotency keys, status guards: ready
          → suspended → ready; suspended blocks Update/WP/staging but not
          Delete; MarkReady never resurrects deleting/suspended); agent
          parses agent-rendered vhosts (strict regexes, refuses
          non-agent config), swaps in the 503 stub via the config-safety
          pipeline with <file>.epicpanel-suspend-bak backups (first
          backup never overwritten), resume restores byte-identical.
          (8) LIMITS SEAM (internal/limits): ForPackage maps the hosting
          package to FPM pool values at ONE call-site (buildDesiredPayload
          via PackageForOrg); Phase 9 replaces the stub with the unified
          engine. (9) SSL events: ssl.issued / ssl.failed /
          ssl.renewal_started on the event bus via domains.OnSSLEvent.
          New agent ops: sync_ftp_accounts, sync_dns_zone,
          suspend_website, resume_website. New endpoints: /ftp-accounts
          (CRUD+password+reveal), /dns-zone + /dns-zones + /dns-records +
          publish, /redirects, /suspend, /resume. Frontend: alias CRUD,
          redirects card, FTP/SFTP card (one-time password reveal), DNS
          zone page, suspend/resume header actions, SSL expiry chips.
Reason:   Master doc Phase 4 verbatim list (hosting accounts, isolation,
          domains/subdomains/aliases/redirects, vhosts, PHP-FPM, MySQL/
          MariaDB, DNS zones/records, SSL, cron, FTP/SFTP, limits) +
          architecture report Phase-4 row (rewrite-snippet S6, vhost/pool
          writer duplication, apache/OLS modes). All provisioning through
          the agent as typed ops; validation before live config changes;
          a failed config rolls back and cannot take down unrelated
          sites (proven by tests).
Status:   Accepted
```

ADR-048
Decision: Phase 5 customer cPanel + frontend monorepo. (1) PACKAGES: shared
          components extracted into `frontend/packages/` consumed as
          `@epicpanel/*` via npm workspaces + tsconfig paths + vite aliases
          (no bundler-for-packages step — smallest change keeping both the
          legacy root app and `apps/customer` green). SEVEN packages, not
          the doc's six: `core` (NEW — api client, ApiError, all wire
          types, WS singleton w/ backoff, useMetrics/useFreshness/
          computeFreshness/formatAge, AuthProvider/ROLE_RANK, Phase-4
          typed API helpers) because api/ws/metrics/auth had no home in
          the doc's list; `ui` (Card/StatCard/StatusBadge/EmptyState/
          Skeleton/ProgressBar/Ring, UX kit: PageTitle/Breadcrumbs/
          Toolbar/UsageCard/QuickAction/RowActions/Toaster, AppSidebar
          w/ nav-as-props, FreshnessBadge w/ verbatim LIVE/STALE/OFFLINE
          age formats); `forms` (Modal/ConfirmDialog/Field/Select/
          ErrorNote); `charts` (AreaChart/ChartControls/Sparkline);
          `tables` (generic DataTable w/ search + filter slots); `icons`
          (lucide re-export); `design-system` (tokens, TONE palette,
          chart colors, LIVE≤15s/STALE≤120s constants). Legacy `src/`
          kept building through re-export shims — zero duplicated
          component code. (2) APPS/CUSTOMER: dashboard (WS-live CPU/RAM/
          Disk/Bandwidth UsageCards each with freshness badge + age,
          quick actions, activity timeline), websites, domains, DNS zone
          editor (records CRUD + publish), file manager, FTP (one-time
          passwords), databases + phpMyAdmin SSO, PHP selector, crons,
          backups (create/restore/schedule), metrics history (24h raw +
          7d/30d rollups; "Live now" strip WS-only, clearly separated
          from historical per the Phase 3 rule), SSL (issue/renew/
          expiry countdown), security (2FA + API keys), account. Runs on
          :5174; root dev app on :5173; both proxy /v1 → 127.0.0.1:8080
          (ws:true). (3) AUTHZ > UI HIDING: Terminal WS route and
          ssh-keys List/Create raised RoleDeveloper → RoleAdmin
          (`terminal/terminal.go`, `sshkeys/handler.go`) so the customer
          nav removal is backed by server enforcement, not just hiding.
          (4) Email accounts screen DEFERRED — no backend module exists;
          never faked. UX kit + FreshnessBadge + TwoFactorCard are
          drop-ins for Phase 6 admin.
Reason:   Master doc Phase 5 ("The customer shouldn't need to understand
          Linux"; ONE design system across two experiences; work item
          explicitly required verifying server-side authz, not just UI).
          A shared data-layer package keeps the single-design-system
          invariant true at the logic layer; source-TS packages avoid a
          build-tooling migration while both apps stay green.
Status:   Accepted
```

ADR-049
Decision: Phase 9 unified resource engine (internal/resources +
          internal/resourcelimits adapter + agent enforcement). Data-
          driven Plan→limits matrix: the plan row IS the matrix (9
          columns in hosting_packages via migration 0027) — adding a
          plan changes only data, never code (TestAddPlanIsDataOnly).
          Single API: Engine.GetLimits(workload) / GetUsage(workload) /
          Enforce(workload). AGENT ENFORCEMENT, same node-side layer as
          Phase 3 collection: cgroups v2 per-site slices (cpu.max,
          memory.max + memory.high@90%, pids.max; sync timer re-captures
          respawned workers), disk via XFS project quotas / ext4 user
          quotas with detect-then-apply (unsupported → honestly
          `accounted` with reason, never silent), bandwidth via
          nftables per-account RX+TX counters + monthly high-water in
          workload_resource_usage, cgroup io.weight, FPM
          pm.max_children bound to plan RAM/32 MB (Phase 4 internal/
          limits stub reduced to an engine shim). Count resources
          (databases/domains/ports/backups/email): control-plane
          create-time gates (fail-closed) + agent reconciliation guard.
          ANTI-DRIFT BY CONSTRUCTION: one shared cgroup reader
          (readSliceLimits) serves BOTH the display collectors and
          enforcement verification, so usage bars and kernel caps
          physically cannot diverge (drift test e2e: enforce payload ==
          display payload). LEGACY ROWS: pre-0027 hosting_packages rows
          (all-default columns → LegacyRow) keep exactly the legacy
          governed counts, so existing packages never silently change
          behavior. Plan assignment converges sites via idempotent
          `enforce_limits` jobs; existing endpoint response shapes
          unchanged.
Reason:   Master doc Phase 9 verbatim: "The agent must enforce limits,
          not merely display them" + enforcement/display same-source
          rule. Detect-then-apply with mode+mechanism+reason on every
          outcome honors the fail-safely philosophy; kernel-capped
          resources must not fight a second control loop.
Status:   Accepted
```

ADR-050
Decision: Phase 9 over-limit policy matrix. Kernel-capped resources
          (cpu/ram/io/processes) take action `none` — the cgroup
          controller already enforces; a duplicate throttle/kill would
          fight it. Disk and bandwidth (accounted-only) take action
          `suspend`: idempotent `suspend_website` job (only `ready`
          sites suspendable — never corrupts state), `limits.breach`
          event on the bus + audit row (Phase 10 consumes the suspend
          hook). `throttle` re-asserts enforcement without state
          changes. Bandwidth is intentionally accounted-only (no tc
          shaping) — one mechanism (nftables counters) instead of two.
Reason:   Master doc: over-limit actions must never silently corrupt
          workload state; enforce-≠-display drift is eliminated at the
          source. Keeping exactly one enforcement mechanism per
          resource preserves the boring-reliable backend taste.
Status:   Accepted
```

---

## 23. REJECTED APPROACHES

* **Per-user full PHP/runtime installation** — wasteful RAM/disk, update/maintenance nightmare; replaced by shared runtime + isolated pools.
* **Arbitrary SSH as the core control mechanism** — untyped, unsafe, unauditable.
* **Hard-coded OpenLiteSpeed** — violates web-server abstraction; OLS is just one provider.
* **Single global PHP runtime** — violates multi-version invariant.
* **Frontend-controlled infrastructure** — UI must never hold infra logic; authorization is server-side.
* **Unrestricted AI root shell** — AI goes through Tool Registry + Permission Layer only.
* **Premature microservices** — modular monorepo first.
* **Windows support in V1** — Linux only.

---

## 24. REPOSITORY STRUCTURE

Actual structure (Phase 1):

```
epicpanel-2/
+-- EPICPANEL.md                          this file
+-- opencode.json
+-- prompts/                              planning docs (ep-plan, epicpanel-docs)
+-- backend/
    +-- cmd/api/main.go                   control-plane API entrypoint (graceful shutdown)
    +-- cmd/agent/main.go                 server agent entrypoint (poll loop + heartbeat)
    +-- internal/
    |   +-- api/server.go                 route wiring, healthz, audit-logs endpoint
    |   +-- auth/
    |   |   +-- password.go               bcrypt hash/check
    |   |   +-- session.go                opaque tokens, SHA-256 storage, revocation
    |   |   +-- handler.go                register/login/logout/me + audit hooks
    |   |   +-- middleware.go             session auth -> request context
    |   |   +-- password_test.go
    |   +-- users/store.go                user store (first-user bootstrap txn)
    |   +-- organizations/
    |   |   +-- store.go                  orgs, members, roles, slug helpers
    |   |   +-- rbac.go                   RequireMinimumRole middleware (404 for non-members)
    |   |   +-- handler.go                org + member endpoints + audit hooks
    |   |   +-- ids.go, store_test.go
    |   +-- servers/
    |   |   +-- store.go                  server registry, enrollment txns, tokens, metrics
    |   |   +-- handler.go                org-scoped server endpoints + RBAC + audit
    |   |   +-- agent.go                  agent enroll/heartbeat endpoints + agent-token middleware
    |   |   +-- context.go, metrics.go
    |   +-- websites/
    |   |   +-- store.go                  website registry, lifecycle transitions
    |   |   +-- handler.go                org-scoped website endpoints + validation + audit
    |   |   +-- agent_endpoints.go        agent job claim/result + lifecycle transitions
    |   +-- jobs/store.go                 job queue: enqueue, atomic claim, retry, results
    |   +-- runtimes/
    |   |   +-- store.go                 runtime registry per server + refcounting + job outcome application
    |   |   +-- handler.go               install/remove/list endpoints with validation + audit
    |   +-- databases/
    |   |   +-- store.go                 database registry, credential vault (encrypted), lifecycle
    |   |   +-- handler.go               db endpoints + reveal + job outcome application (encrypt+scrub)
    |   +-- domains/
    |   |   +-- store.go                 domain registry (global uniqueness), SSL state machine, serving list
    |   |   +-- handler.go               domain endpoints + ssl/verify jobs + reconcile trigger
    |   +-- deployments/
    |   |   +-- store.go                 deployment history (pending/running/successful/failed)
    |   |   +-- handler.go               config/trigger/rollback/history + job outcome application
    |   +-- backups/
    |   |   +-- store.go                 backup registry, due-schedule query, retention pruning
    |   |   +-- handler.go               backup endpoints + config + restore + job outcome application
    |   +-- monitoring/
    |   |   +-- checker.go               http health check loop + alert raise/resolve (deduped)
    |   |   +-- handler.go               alerts / website health / server metrics history endpoints
    |   +-- apitokens/
    |   |   +-- store.go                 token registry (hashed, scoped, org-confined)
    |   |   +-- handler.go               create/list/revoke endpoints (raw shown once)
    |   +-- secretbox/secretbox.go       AES-GCM encryption for credentials at rest
    |   +-- agent/                        (agent-side library)
    |   |   +-- client.go                 control-plane client, /proc metrics collection
    |   |   +-- executor.go               typed ops: ProvisionWebsite, DeleteWebsite (idempotent, no shell)
    |   |   +-- fpm.go                    PHP-FPM pool render/atomic-write/validate/reload + stale-pool cleanup
    |   |   +-- nginx.go                  WebServerProvider (nginx): vhost render, atomic+validated reload
    |   |   +-- database_ops.go           DatabaseProvider ops: create/drop db+user (mysql/mariadb/postgresql)
    |   |   +-- db_helpers.go             mysql/psql exec helpers, wait-for-ready, password gen
    |   |   +-- ssl_ops.go                Certificates: self-signed (in-process) + Let's Encrypt (lego, webroot HTTP-01)
    |   |   +-- dns_verify.go             DNS verification (resolve + match server IPs)
    |   |   +-- deploy_ops.go             Git deploys: releases, symlink swap, carry-over, rollback, pruning
    |   |   +-- staging_ops.go            CloneStaging/PromoteStaging (files + root-socket db dump/restore)
    |   |   +-- backup_ops.go             CreateBackup/RestoreBackup (site tar + db dumps + manifest)
    |   |   +-- runtime_ops.go            InstallRuntime/RemoveRuntime (apt + PPA fallback)
    |   |   +-- worker.go                 poll-and-execute loop
    |   |   +-- config.go
    |   +-- audit/audit.go                audit store (Record, RecordBestEffort, List)
    |   +-- httpapi/                      errors, JSON helpers, router, middleware
    |   +-- config/config.go              env-driven config
    |   +-- db/                           pgx pool + embedded migrations runner
    +-- migrations/0001_init.sql          users, organizations, org members, sessions, audit_logs
    +-- migrations/0002_servers.sql       servers, registration/agent tokens, metrics
    +-- migrations/0003_websites_jobs.sql websites, jobs (+ job_type/job_status enums)
    +-- migrations/0004_runtimes.sql      runtimes registry, websites.runtime_version, job enum values
    +-- migrations/0005_webserver.sql     websites.web_server column
    +-- migrations/0006_databases.sql     databases table + engine/status enums + job enum values
    +-- migrations/0007_domains_ssl.sql   domains table + ssl enums + job enum values
    +-- migrations/0008_deployments.sql   deployments, staging columns, deploy/clone/promote job enums
    +-- migrations/0009_backups.sql       backups table, schedule columns, backup job enums
    +-- migrations/0010_monitoring.sql    http_checks + alerts tables
    +-- migrations/0011_api_tokens.sql    api_tokens table
    +-- migrations/0012_server_ops.sql    servers.maintenance_mode
    +-- internal/api/openapi.json         embedded OpenAPI 3.0.3 spec
    +-- Makefile                          build / test / run
```

Update this section to match the actual repository as it evolves.

### Frontend structure (frontend/)

```
frontend/
+-- index.html, vite.config.ts, tailwind.config.js, tsconfig.json
+-- src/
    +-- main.tsx, App.tsx            router + guarded shell
    +-- index.css                    tailwind + card/button/badge components
    +-- lib/api.ts                   fetch client (credentials: include), ApiError
    +-- lib/types.ts                 API types + fmtBytes/timeAgo helpers
    +-- context/AuthContext.tsx      auth state, org switcher, refresh
    +-- components/Sidebar.tsx       dark sidebar: nav, org switcher, resource card, account
    +-- components/Header.tsx        search (Ctrl K), bell w/ alert dot, avatar
    +-- components/ui.tsx            Modal, Field, ErrorNote, Select
    +-- components/cards.tsx         Card, MetricCard, ProgressBar/Ring, StatusBadge, EmptyState, Skeleton
    +-- pages/                       Auth, Dashboard, Sites, Databases, Servers, Activity, Team, Backups, Security, Settings
```

Role-awareness: Servers page (registration/maintenance) and Team/Settings token management render admin-only (others see explanatory empty states); server-side RBAC remains the real gate.

Run: `cd frontend && npm install && npm run dev` (vite proxies /v1 to 127.0.0.1:8080). Production: `npm run build` -> dist/.

### Environment (this dev machine)

* Go 1.22, PostgreSQL 16 (systemd service, active)
* Dev DB: `postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel` (role `epicpanel`, password `epicpanel_dev` — development only)
* Test DB: `epicpanel_test` (integration tests run when `EPICPANEL_TEST_DATABASE_URL` is set)
* `epicpanel_v1_backup` — stale schema from a previous EpicPanel attempt found in the original `epicpanel` DB; renamed and preserved, NOT part of this codebase
* Run dev server: `cd backend && make run` (listens on 127.0.0.1:8080 by default)
* Run tests: `EPICPANEL_TEST_DATABASE_URL="postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable" go test ./...`

---

## 25. SESSION HANDOFF

At the end of every significant session, update this file with: what was completed, what changed, files/components affected, tests performed, current phase/state, remaining work, new architectural decisions, new blockers, next recommended task. **Compact engineering handoff — not a conversation transcript.**

### Session 2026-09-08 — Phase 5 + Phase 9 (parallel)
- **Phase 5 DONE:** frontend monorepo (`packages/{core,ui,icons,charts,tables,forms,design-system}` via `@epicpanel/*`; legacy `src/` = re-export shims) + `apps/customer` (:5174; 14 screens; Email deferred — backend missing; terminal/sshkeys server-side authz raised dev→admin). UX kit ready for Phase 6.
- **Phase 9 DONE:** `internal/resources` engine + `internal/resourcelimits` adapter + `internal/agent/enforce.go` (cgroups v2, XFS/ext4 quotas, nftables accounting, FPM pm.max_children, fail-closed count gates); anti-drift shared cgroup reader; over-limit suspend policy; migration `0027_resource_plans.sql` (7 verbatim plans; legacy rows keep legacy counts). `internal/limits` is now an engine shim.
- **Verified:** `go build`/`go vet` clean; `go test ./... -p 1` w/ test DB = 14 pkgs ok / 0 FAIL; frontend `tsc -b` clean, root 804 kB + customer 382 kB builds green; both dev proxies `/v1` → :8080 (ws:true).
- **Next:** Phase 6 (Admin WHM) → 7 (Minecraft) → 8 (Discord) — 6 consumes Phase 5 packages, 7/8 consume Phase 9 engine (`WorkloadKind minecraft|discord`, `GetLimits/EnforcePlan`).

---

## 26. NEW SESSION PROCEDURE

1. Read `EPICPANEL.md`.
2. Inspect the repository.
3. Compare documented state vs. actual code; investigate discrepancies before changing architecture.
4. Identify current phase, current task, blockers, relevant code/tests.
5. Continue from the documented state.
6. Do not restart the project or redesign completed architecture without a concrete reason.
7. After meaningful work, update `EPICPANEL.md`.

---

## 27. DEVELOPMENT PHILOSOPHY

Priority order:

1. Security
2. Correctness
3. Isolation
4. Reliability
5. Maintainability
6. API stability
7. Extensibility
8. Performance
9. Developer experience

Do not optimize implementation speed at the expense of infrastructure correctness. Infrastructure software must fail safely. Never silently swallow errors. Assume operations fail; every operation needs clear state, error reason, retry behavior, safe rollback where possible, and audit info.

Testing is mandatory: unit, API, provider, provisioning, isolation, permission, agent, integration, failure/retry tests. Critical cases: tenant A cannot access tenant B's files/processes/resources; tokens without scope cannot perform privileged ops; provisioning retry is safe; runtime version changes don't affect unrelated websites.

---

## 28. FINAL RULE

`EPICPANEL.md` is the persistent memory of the project. Keep it accurate, usable, and synchronized with the codebase. A future agent must be able to enter with zero prior knowledge and know: what EpicPanel is, why it exists, how it is architected, what decisions were made, what is implemented, what is in progress, what remains, and what must not be changed casually.

---

## 29. Wave ADRs — Phases 6-15 parallel execution (2026-09-09)

ADR-051 — Minecraft as first-class workload (Phase 7). `internal/minecraft` with verbatim
state machine; ContainerDriver interface + SystemdDriver shipped (systemd transient units
+ cgroups v2 — same isolation family as `internal/apps`/Phase 9). Docker driver is a slot-in
(later): the environment has no docker.sock and the platform rule "customers never touch
the Docker daemon" holds either way. Providers (vanilla/paper/purpur/fabric/forge/neoforge)
fetch version manifests at runtime with on-disk cache + seed fallback; zero provider
branches in core. RCON client is stdlib-only. TPS/MSPT flow the Phase 3 minecraft envelope
with honesty flags (tps_source) — never fake numbers.

ADR-052 — Discord bots as first-class workload (Phase 8). `internal/discord` + BotRuntime
(Node/Python shipped, Java stub). Sandbox: dedicated ep-bot-* system users + systemd
transient units + cgroups; env/secrets sealed via secretbox, 0600 EnvironmentFile,
write-only API surface (masked), scrub-on-ingest log ring. Secret-scrub proofs exist at
agent AND API layers.

ADR-053 — Billing as an explicit state machine (Phase 10). Verbatim 8-state machine with a
single guarded transition function; every transition = idempotent job + audit + event.
Money = integer minor units; invoices immutable (DB trigger). PaymentProvider interface
with FakeProvider (tests) + ManualProvider (v1); no gateway SDK (webhook replay idempotency
proven). Suspend semantics reuse existing workload lifecycle job types — no new side
channels. Terminate = backup-first (Phase 11 terminate_backup hook).

ADR-054 — Backups unified engine (Phase 11). BackupSink drivers: Local / Remote (config
only) / S3-compatible via dependency-free SigV4 client. All six verbatim types ride the
same job family; encryption happens on-node before egress (per-backup data key, wrapped
via secretbox at rest). Verification = checksum + restore-to-scratch; unverified →
backup.verify_failed (Phase 13 input). Retention combines plan count (Phase 9) with
time-based pruning. Legacy website rows mapped to the new vocabulary (0031). Sink kind
vocabulary normalized in sink.Open ("s3"→"object").

ADR-055 — Alerts are async, always (Phase 13). Rule engine (threshold/state/time with
hysteresis + dedup + lifecycle) evaluates ONLY from bus subscriptions and periodic sweeps —
never in request handlers (Phase 3 rule generalized). WHM observability reads LiveStore
only (never the history DB). Panel self-telemetry = hand-rolled Prometheus text on /metrics
(request class counters/latency wired via requestMetricsMiddleware, WS conns, queue depth).

ADR-056 — Security hardening as executable evidence (Phase 12). The authz matrix is a
route-table completeness gate + live probes (261 routes; cross-tenant = 404; tokens
refused on admin surface; agent surface agent-only). Secret-leak scanning is a reusable
package (Scan/ScanPlant) used to prove planted secrets never leak into responses, logs,
or audit rows. AgentReplayGuard wired in advisory mode (strict flip documented). Shipped
exceptions (owner-approved): TLS termination via reverse proxy, firewall CRUD deferred —
see docs/security-checklist.md.

ADR-057 — One design system, two experiences (Phase 14). ui-ref tokens extracted into
packages/design-system (CSS vars + Tailwind theme); tokens.css now genuinely imported by
packages/ui (was dead before — fixed). Missing verbatim components added to shared packages
only (palette/breadcrumbs/timeline/bulk-bar/skeletons); apps consume, never fork. A11y:
focus-visible ring, keyboard nav, reduced-motion; 180ms functional transitions; zero emoji.

ADR-058 — v1 release posture (Phase 15). Scale verified to 100 simulated nodes on the dev
box (numbers in docs/scale-report.md; 1000-node = extrapolated, marked). Sync-exec sweep:
API→Job→Queue→Agent→Event→WS→UI holds in all 12 areas. deploy/ ships hardened systemd
units (the LIVE services already run from this pattern), env template, logrotate, chaos
drills; install.sh enforces backup-before-migrate. v1.0.0 ships with two owner-approved
security exceptions and honest [est] scale markers.

ADR-059
Decision: Platform admin API keys (epa_, admin_api_keys table) are the machine
          counterpart of an admin session: cross-organization reach plus the
          /v1/admin* surface, scope-gated end to end (admin:read/admin:write on
          the admin surface; org resource scopes on /v1/organizations/...
          routes; scope "*" expands at creation). Key create/revoke stay
          session-only — a leaked key cannot mint or revoke keys. The six
          duplicated requireAdminSession/requirePlatformAdmin gates collapse
          into httpapi.RequireAdmin; RequireMinimumRole now enforces org-token
          confinement (a token could previously read every org its creator
          belonged to through the organizations routes — leak fixed);
          openapi.json regenerated from openapi_gen.py (ADR-041 tooling debt).
Reason:   "Admin controls everything via API" was impossible: platform-admin
          routes refused every token (ADR-027/043 made epk_ org-confined) and
          the spec had drifted (55 of ~150 paths). epa_ is a separate principal
          table, not a flag on api_tokens, so ADR-027's boundary holds; the
          shared gate keeps admin-route policy from drifting per feature.
Status:   Accepted

ADR-060
Decision: Invisible tenancy: organizations stay the isolation engine, but
          users never manage them. Every account is created inside its own
          auto-provisioned personal organization (auth.Register and
          POST /v1/admin/users call organizations.EnsurePersonalOrg,
          idempotent, collision-safe slugs); every org-scoped route
          /v1/organizations/{org_id}/X is ALSO served at /v1/X via the
          ResolveOrgAlias middleware (session auth -> alias -> CSRF -> scope
          enforcement), resolving the active org as: org tokens = bound org
          (header ignored, confinement cannot be escaped); X-EpicPanel-Org
          header if present; else the caller's primary (earliest) org. The
          org switcher renders only for multi-org members; the create-org
          screen remains as a legacy-user fallback.
Reason:   Org CRUD, slugs, switchers and "create an organization first"
          onboarding were pure friction for customers (1:1 user:org in
          practice) while the tenancy layer is load-bearing (isolation
          invariant, billing, reseller, RBAC) and must NOT be removed —
          ADR-027/043 confinement and the phase-12 matrix keep working
          unchanged because the alias rewrites BEFORE scope enforcement and
          every handler keeps its own org resolution.
Status:   Accepted

Coordinator notes (wave execution): subagents ran with exclusive file ownership
(phases/wave-contract.md); shared files (server.go routes, worker dispatch, App.tsx) were
wired centrally. Two crash interruptions were healed by the coordinator (agent
workload.go:123 empty-line panic fix; sink JSONB/sink-kind fixes; route mounts). Test DBs
are per-agent (epicpanel_test_<suffix>). Full verification: go build/vet/test ./... -p 1
green (22 pkgs), tsc -b clean, all three app builds green.
