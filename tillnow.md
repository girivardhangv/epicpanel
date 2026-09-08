# EpicPanel — tillnow.md

> Snapshot of everything built so far: features, methods, architecture, API surface, and what remains.
> Generated from a full codebase walkthrough (backend + frontend + migrations + docs).
> Companion doc: `EPICPANEL.md` (architecture decisions / ADRs / session handoff).

---

## 1. What EpicPanel Is

**Linux-first hosting control panel** (cPanel/WHM-class, RunCloud/Ploi-class agent model):

- A **Go control-plane API** (`epicpanel-api`) + **PostgreSQL** metadata store.
- A **Go server agent** (`epicpanel-agent`) installed on every managed Linux server — does all real work via **typed jobs** (no SSH-as-architecture).
- A **React 19 + TypeScript + Vite + Tailwind** frontend that is a pure API client.
- **cPanel account model**: platform admins create customer accounts, own provisioning, and manage packages; customers see only their assigned org's resources.

### Tech Stack

| Layer      | Choice |
|------------|--------|
| Backend    | Go (stdlib `ServeMux`, pgx/v5, no heavy frameworks) |
| Database   | PostgreSQL 16 (control plane), MySQL/MariaDB/PostgreSQL (customer DBs) |
| Agent      | Go binary, poll loop, typed job execution |
| Frontend   | React 19, React Router 7, Tailwind 3, lucide-react, xterm.js |
| Auth       | bcrypt + opaque session tokens (SHA-256 hashed), API tokens (`epk_`), agent tokens (`agt_`) |

### Core Principles

- API-first (`/v1/...`), desired-state + reconciliation, idempotent provisioning
- **Runtime is shared, execution is isolated** (central PHP/Node/Python/Go binaries; per-site pools/users)
- Provider abstractions: `WebServerProvider`, `DatabaseProvider`, `CertificateProvider`, `RuntimeProvider`
- Everything async via jobs table (`SELECT ... FOR UPDATE SKIP LOCKED` claim, retry ≤3)
- Server-side authorization always; 404-cloaking for non-members; audit on every mutation

---

## 2. Architecture

```
Frontend (React, Vite dev proxy → :8080)
   ↓
API (stdlib ServeMux, /v1, CORS, rate-limit, request logging + IDs)
   ↓
CONTROL PLANE (Go, internal/* modules)
   ├─ auth / users / organizations+RBAC / audit
   ├─ servers (registry, agent enroll, heartbeat, metrics, maintenance)
   ├─ websites (desired state, lifecycle, files, config, staging, wordpress)
   ├─ runtimes (+extensions) / databases / domains+SSL / deployments
   ├─ backups / monitoring / crons / sshkeys / terminal / apps / packages
   ├─ jobs (queue) / apitokens / settings (setup wizard) / secretbox (AES-GCM)
   ↓ jobs table (claim/result/progress)
AGENT (Go, internal/agent/*) — one per Linux server
   ↓
Linux data plane: nginx / apache / OLS, PHP-FPM pools, systemd app services,
   MySQL/MariaDB/PostgreSQL, bubblewrap sandboxes, crontabs, backups
```

### Repository Layout

```
epicpanel-2/
├── EPICPANEL.md                 # architecture source of truth + ADRs (001–037)
├── install.sh                   # installer (setup-token bootstrap)
├── prompts/                     # planning docs
├── backend/
│   ├── cmd/api/main.go          # control-plane entrypoint (graceful shutdown)
│   ├── cmd/agent/main.go        # agent entrypoint (poll loop + heartbeat)
│   ├── cmd/agent-shell/         # epicpanel-shell (restricted login shell)
│   ├── internal/
│   │   ├── api/                 # server.go route wiring, scheduler.go, pma_sso.go, openapi
│   │   ├── httpapi/             # router, CORS, errors, rate-limit, token auth, middleware
│   │   ├── auth/ users/ organizations/ audit/
│   │   ├── servers/ websites/ runtimes/ databases/ domains/
│   │   ├── deployments/ backups/ monitoring/ crons/ sshkeys/
│   │   ├── apps/ packages/ settings/ apitokens/ terminal/
│   │   ├── isolation/           # bubblewrap sandbox + seccomp + specs
│   │   ├── agent/               # agent-side library (26 op files)
│   │   ├── jobs/ ports/ secretbox/ config/ db/ migrations/
│   ├── migrations/0001–0018     # embedded SQL migrations
│   └── Makefile
└── frontend/
    └── src/{pages,components,context,lib}   # 19 pages, WHM-style UI
```

---

## 3. Feature Inventory (all implemented)

### 3.1 Auth, Users, Organizations, RBAC
- Register/login/logout/me; **bcrypt** passwords; opaque session tokens stored **SHA-256-hashed**; cookie + Bearer; revocation
- **First registered user = platform admin**; admins create customer accounts (`POST /v1/admin/users`)
- Organizations CRUD, slug auto-generation; members with roles **owner > admin > developer > billing/support** (rank model)
- Server-side rank checks; **last-owner protection**; non-members get **404** (not 403) — tenant cloaking
- Platform admins bypass org membership (cPanel root model)
- **Audit logging** (auth, org, member, and all subsystem mutations) — `audit_logs` table, best-effort recorder

### 3.2 Servers & Agent
- Server registry (org-scoped), one-time **registration tokens** (`reg_…`, rotatable)
- Agent enrollment: `reg_…` → persistent `agt_…` token (SHA-256 at rest), persisted to `/etc/epicpanel/agent.env`
- Heartbeat + **metrics ingestion** (CPU/mem/disk from /proc); status derived at read time: `pending` / `online` (<2 min) / `offline`
- **Maintenance mode** per server (blocks job claims + auto-placement), audited PATCH
- **Capacity API**: per-server loads/site-counts/eligibility (admin+)
- **Auto-placement**: create website without `server_id` → least-loaded online non-maintenance server scored `0.6·CPU% + 0.4·mem%`, runtime-version-aware
- **Software detection** (`detect_software` job) + agent CLI lifecycle (`enroll`, `run`)
- Server metrics history endpoint (last 200 points)

### 3.3 Websites Engine
- Org-scoped websites, **desired-state provisioning** (async 202 + job_id)
- Filesystem: `/srv/epicpanel/websites/<website_id>/{public,logs,tmp}`; dedicated **Unix user per site** (`ep-<org8>-<name>`, nologin), 0750 base dirs
- Idempotent provision/delete; lifecycle `pending → provisioning → ready | failed …`
- `web_server` field: `nginx` (default) | `nginx,apache` | `nginx,openlitespeed` | `none` (files-only)
- PATCH website → runtime version reconcile; per-site job history
- **Backend port allocator** (`portalloc.go`): stable private ports in configurable ranges for apache/OLS backend mode; DB-persisted + loopback-bind checked
- **PHP-FPM pools**: per-site pool files, atomic write + `php-fpm -t` validate-before-swap, dedicated socket `/run/epicpanel/php-fpm/<id>.sock`, live version switching with stale-pool cleanup, open_basedir confinement
- **Nginx vhosts**: atomic + `nginx -t` validated reload, per-site logs, dotfiles denied, SSL-aware (443 + 80→443 redirect + ACME webroot)
- **Apache + OpenLiteSpeed providers** (`webserver_ops.go`): rendered as **backend proxies behind nginx** (RenderApacheSite / RenderOLSVhconf, ensure/remove, stop-if-unused, OLS user normalization)
- **Reverse proxy vhosts** (`proxy_ops.go`): nginx → `127.0.0.1:<backend_port>` for process apps (WebSocket upgrade headers, X-Forwarded-*)
- **File manager** (backend + UI): list/read/write/create/rename/delete/upload/download per-site with strict path containment (symlink-resolved traversal → 403), 2 MB editor / 32 MB upload caps, whole-site tar.gz export
- **Site config**: rewrite rules editor (PUT /config/rewrite), config view
- **WordPress one-click**: wp-cli download + install as site user, auto MariaDB creation, refuses double-install, admin creds returned in job result
- **Staging**: one staging env per site (own user/pool/vhost, `_stg` DBs), clone + **promote** (admin-only) via root-socket dump/restore — no DB creds cross the wire

### 3.4 Runtimes
- Per-server runtime registry with **refcount-protected removal**
- PHP (distro + Ondřej Surý PPA fallback; tolerates broken apt repos), **Node.js** (NodeSource), **Python** (apt), **Go** (official toolchain, newest patch auto-resolved from go.dev)
- **PHP extensions manager**: install/remove per runtime (`php<major>-<name>`, OLS = `lsphp<major>-<name>`), list installed, extension job types
- FPM pool limits configurable per hosting package (memory, max_children, clamped spares — ADR-032)

### 3.5 Databases
- `DatabaseProvider` ops: create/drop DB + user for **MySQL / MariaDB / PostgreSQL** (engine install via apt; native clients; regex-validated identifiers `ep_<org8>_<label>`)
- **Credential vault**: generated agent-side, returned once in job result, **AES-GCM encrypted** at rest (EPICPANEL_SECRET_KEY), plaintext scrubbed from jobs table; **audited reveal endpoint**
- Website linking; engine wait-for-ready helpers
- **phpMyAdmin + Adminer**: agent-deployed under `/srv/epicpanel/dbadmin`, dedicated FPM pool + nginx vhost on **:8081**; install endpoint (admin+)
- **phpMyAdmin SSO** (`pma_sso.go` + `GET …/databases/{id}/pma-sso`): AES-encrypted one-time token (60 s TTL) + gate redirect to :8080/:8081 targets only; consumed-token tracking defense-in-depth

### 3.6 Domains, DNS, SSL
- Domains are first-class, **panel-globally unique** (one domain = one website); primary + aliases; vhost reconcile on change
- **DNS verification**: agent-side resolve + match against server IPs (RunCloud/Ploi model)
- **SSL via CertificateProvider**: self-signed (in-process) + **Let's Encrypt** (lego, HTTP-01 webroot through nginx; ACME staging via `EPICPANEL_ACME_DIRECTORY`)
- SSL-aware vhosts (SNI, TLSv1.2/1.3); per-domain `ssl_mode`/state/expiry
- **Hourly renewal scheduler**: re-enqueues certs expiring <30 d; also refreshes server online/offline + due backups

### 3.7 Deployments & Staging
- **Git HTTPS deploys**: immutable release dirs + **atomic symlink swap**, `.env`/uploads carry-over, first-deploy migration of manual content, **keep-5 pruning**, shallow→full clone fallback
- **Rollback** to last successful release (admin+); encrypted deploy tokens (AES-GCM, injected agent-side only)
- Deployment history (pending/running/successful/failed)

### 3.8 Backups
- Manual + **scheduled** (daily/weekly) with **retention 1–30**; per-site tar.gz + gzipped DB dumps + `manifest.json` under `/srv/epicpanel/backups/<id>/`
- Restore re-extracts + re-imports (drop+create+import); retention prunes rows + archives
- Backup config endpoint; hourly scheduler enqueues due backups
- Remote storage (S3/SFTP) still pending

### 3.9 Monitoring
- **HTTP health checker** (default 60 s) on primary domains of ready sites → `http_checks`
- **Alerts** on down/up transitions with auto-resolve + unresolved-dedupe (partial unique index); alerts endpoint (`?resolved=true`)
- Server metrics history endpoint

### 3.10 Jobs System
- Table queue: atomic **FOR UPDATE SKIP LOCKED** claim, retry (max_attempts 3), progress reporting endpoint, per-website/server job history
- **Single result endpoint** + **fanout callbacks** (`OnJobFinished`/`OnJobClaimed`) drive all subsystem state machines

### 3.11 Security & Isolation
- **Rate limiting**: in-memory token buckets, IP-keyed — auth 10/min (burst 20), API 300/min (burst 600)
- **API tokens**: `epk_` prefix, SHA-256 hashed, **scopes** (read/write per resource group), expiry, revocation, audited; **org-confined** regardless of creator memberships
- **Kernel sandbox** (`internal/isolation`): **bubblewrap** mount+PID namespaces for ALL shell sessions (SSH + web terminal) — site at `/site` writable, /usr+/bin+/lib read-only, /srv//root//home + other sites **not mounted**; no-new-privs + nosuid; rlimits (nproc/nofile/as/fsize/cpu); **seccomp** support; sandbox spec files = integration contract
- **SSH access**: per-site SSH keys (10-key cap, fingerprinted) synced to `authorized_keys` with no-port-forwarding; login shell = **epicpanel-shell** restricted shell (blocks sudo/su/systemctl/network servers); zero keys → nologin
- **Web terminal**: xterm.js over **WebSocket** (gorilla + creack/pty) as site user inside the sandbox; 2 sessions/site, 32 global, 16 KB msg cap, 10-min idle timeout, audited
- **Cron jobs**: per-site crontab rendered atomically from desired state; 5-field validation + shell-injection guards (no chaining/pipes/backticks/sudo); hourly re-sync; UI presets + pause/resume
- AES-GCM `secretbox` for all secrets at rest; CORS middleware (`EPICPANEL_CORS_ORIGINS`)

### 3.12 Hosting Packages & Accounts (cPanel model)
- **Packages** (`hosting_packages`): max sites/databases, disk MB, FPM memory + max_children, allowed runtimes, price; Starter/Pro/Enterprise seeds
- Package assignment per org → **apply_quota jobs** rewrite FPM pools atomically + reload; creation-time enforcement (403 with plan name)
- **Admin pages**: Manage Accounts (`/admin/users`), Packages with assign wizard; org package view
- Disk usage-accounted (agent reports `used_mb`)

### 3.13 Applications (process-based apps) — NEW
- For **node/python/go** runtime sites: `applications` table (startup/build command, startup file, internal port, env vars, process name, **health**)
- Full process lifecycle: **build/start/stop/restart/status/logs** as agent jobs (systemd service per app `AppServiceName`); runs as site user via `runAsSite`
- Per-runtime deploy paths: node (npm install/build), python (venv + pip), Go (go build); sanitized env keys/values
- **Reverse-proxy integration**: nginx proxy vhost on allocated backend port; status probe → health `starting/running/stopped/crashed`
- API: GET/POST/PATCH application, start/stop/restart/status/logs (developer+ for mutations, billing+ for reads)

### 3.14 Setup Wizard & Settings — NEW
- **Installer flow**: one-time **setup tokens** (SHA-256 hashed, 1 h TTL, single-use) minted via CLI (`epicpanel-api setup-token`)
- Public pre-auth endpoints: `GET /v1/setup/status` (+token validity probe), `POST /v1/setup/verify-hostname` (hostname resolves to server IP)
- **Software step** (pre-auth with token): catalog + install jobs
- `POST /v1/setup` completes setup; `GET /v1/settings`, `PATCH /v1/settings/hostname`; `system_settings` + `setup_tokens` tables
- Frontend **/setup** wizard page outside guarded shell

### 3.15 Public API & Docs
- **OpenAPI 3.0.3** spec embedded, served at `/v1/openapi.json`
- API token wizard in Settings UI

---

## 4. Complete API Surface (as wired in code)

### Auth & Setup
```
POST /v1/auth/register | login | logout          GET /v1/auth/me
GET  /v1/setup/status (?token=)                  POST /v1/setup
POST /v1/setup/verify-hostname                   GET/POST /v1/setup/software
GET  /v1/setup/jobs                              GET /v1/settings
PATCH /v1/settings/hostname                      GET /v1/pma-gate
```

### Admin (platform admins)
```
GET/POST /v1/admin/users
GET/POST /v1/admin/packages          PATCH/DELETE /v1/admin/packages/{id}
POST /v1/admin/organizations/{org}/package        (assign package)
```

### Organizations / Members / Audit
```
GET/POST /v1/organizations           GET/PATCH /v1/organizations/{org}
GET/POST /v1/organizations/{org}/members
PATCH/DELETE /v1/organizations/{org}/members/{user}
GET  /v1/audit-logs?organization_id=
```

### Servers & Agent
```
GET/POST /v1/organizations/{org}/servers
GET/DELETE /v1/organizations/{org}/servers/{id}
POST .../registration-token (rotate)             GET .../metrics | metrics/history
PATCH .../maintenance                            GET .../capacity
POST .../database-tools                          POST .../detect-software
GET  .../servers/{id}/jobs
POST /v1/agent/enroll | heartbeat | jobs/claim | jobs/{id}/result | jobs/{id}/progress
```

### Runtimes & Extensions
```
GET/POST .../servers/{id}/runtimes       DELETE .../runtimes/{rt}
GET/POST .../runtimes/{rt}/extensions
```

### Websites & Sub-resources
```
GET/POST /v1/organizations/{org}/websites
GET/PATCH/DELETE .../websites/{id}               GET .../websites/{id}/jobs
GET/POST/PATCH/DELETE .../websites/{id}/domains  (alias mgmt)
POST /v1/organizations/{org}/domains/{id}/ssl | verify-dns
GET  /v1/organizations/{org}/domains             (org-wide)
GET/PUT .../websites/{id}/config | config/rewrite
FILES: GET/POST/PATCH/DELETE .../files, /files/content, /files/upload, /files/download
CRONS: GET/POST .../crons   PATCH/DELETE .../crons/{id}
SSH:   GET/POST .../ssh-keys    DELETE /v1/organizations/{org}/ssh-keys/{id}
TERM:  GET .../terminal         (WebSocket upgrade)
WP:    POST .../wordpress
STAGE: POST .../staging | promote
DEPLOY:PATCH .../deployment-config  POST .../deploy | rollback  GET .../deployments
BACKUPS: GET/POST .../backups  PATCH .../backup-config  POST .../backups/{id}/restore (org-level)
APP:   GET/POST/PATCH .../application  POST .../application/{start|stop|restart}
       GET .../application/{status|logs}
```

### Databases, Tokens, Monitoring
```
GET/POST /v1/organizations/{org}/databases
GET/DELETE .../databases/{id}          GET .../databases/{id}/credentials | pma-sso
GET/POST .../api-tokens                DELETE .../api-tokens/{id}
GET .../alerts                         GET .../websites/{id}/health
GET /healthz                           GET /v1/openapi.json
```

---

## 5. Agent Job Types (full switch in `worker.go`)

| Job | Purpose |
|---|---|
| `provision_website` / `delete_website` | site user, dirs, FPM pool, web servers, idempotent |
| `install_runtime` / `remove_runtime` | PHP/Node/Python/Go installs (refcounted) |
| `install_extension` / `remove_extension` | PHP/lsphp extensions |
| `detect_software` | server software inventory |
| `create_database` / `delete_database` | db+user via native clients |
| `issue_certificate` / `verify_domain` | SSL + DNS verification |
| `deploy_website` / `rollback_website` | git releases / symlink rollback |
| `clone_staging` / `promote_staging` | staging clone/promote |
| `create_backup` / `restore_backup` | tar + db dumps / restore |
| `build_app` / `start_app` / `stop_app` / `restart_app` / `app_status` / `app_logs` | process-app lifecycle |
| `sync_crontab` | render site user crontab |
| `install_wordpress` | wp-cli one-click |
| `sync_ssh_keys` | authorized_keys sync |
| `install_database_tools` | phpMyAdmin/Adminer deploy |
| `apply_quota` | hosting-package pool limits |

---

## 6. Database Schema (18 migrations)

| # | Contents |
|---|---|
| 0001 | users, organizations, org members, sessions, audit_logs |
| 0002 | servers, registration/agent tokens, server_metrics |
| 0003 | websites, jobs (enums) |
| 0004 | runtimes registry, websites.runtime_version |
| 0005 | websites.web_server |
| 0006 | databases + engine/status enums |
| 0007 | domains + ssl enums |
| 0008 | deployments, staging columns |
| 0009 | backups, schedules |
| 0010 | http_checks, alerts |
| 0011 | api_tokens |
| 0012 | servers.maintenance_mode |
| 0013 | hosting_packages |
| 0014 | cron_jobs, applications |
| 0015 | ssh_keys, terminal sessions |
| 0016 | applications (process apps) |
| 0017 | system_settings, setup_tokens |
| 0018 | setup/extensions support |

---

## 7. Frontend (WHM-style EpicHost UI)

**Pages** (`frontend/src/pages/`):
- Auth: `Auth` (login/register), `Setup` (installer wizard)
- `Dashboard` — beta notice, onboarding next-steps, metric tiles, server rings, alerts/activity, Tools accordion
- `Sites` (create w/ auto-placement + PHP version, filter, delete) + **`SiteDetail`** hub (stats, domains+SSL, tool grid Files/Cron/SSH/Terminal/Backups/SSL, DBs, WordPress, delete)
- **`ApplicationDetail`** — process-app panel (build/start/stop/restart/status/logs)
- `FileManager`, `FilesLanding`, `CronJobs`, `SSHKeys`, `Terminal` (xterm.js)
- `Databases`, `Servers` (admin: token wizard, rings, maintenance), `Software` (runtime/extension installs)
- `Backups`, `Security`, `Settings` (API tokens), `Activity` (audit), `Team`
- Admin: `AdminUsers`, `Packages` (assign wizard)

**Components**: Sidebar (navy #202958, italic brand, collapsible icon rail, tool search Ctrl+F, org switcher), Header (WHM topbar: meta + CPU/Mem/Disk stats, "/" search, alert bell), cards.tsx (Card/MetricCard/ProgressRing/StatusBadge/EmptyState/Skeleton), ui.tsx (Modal/Field/ErrorNote), AppWizardModal, WordPressModal.

**Stack**: React 19 + Router 7 + Tailwind 3 + lucide-react + xterm.js; `AuthContext` (auth state, org switcher); vite dev proxy `/v1` → 127.0.0.1:8080 (ws:true); build ≈ 88 KB gzip.

---

## 8. Testing

- Integration test suite against live PostgreSQL (`EPICPANEL_TEST_DATABASE_URL`): `api_test`, `servers_test`, `websites_test`, `runtimes_test`, `databases_test`, `domains_test`, `deployments_test`, `backups_test`, `applications_test`, `tokens_scheduler_test`, `vhost_test`, `middleware_test`, plus `isolation` unit + live tests (escape matrix over real SSH)
- Live-verified features (per EPICPANEL.md): FPM pools, nginx vhosts, MariaDB, self-signed SSL, git deploy + rollback, backups/restore, monitoring, quotas, db tools, SSH + sandbox escape matrix, web terminal, cron, WordPress, Go runtime, dbadmin SSO

---

## 9. What's NOT Done Yet (pending)

- [ ] **OpenLiteSpeed / Apache as native primary** — implemented as nginx→backend proxy modes; standalone primary mode pending
- [ ] **PostgreSQL engine live smoke** (code complete; MariaDB verified)
- [ ] **Let's Encrypt live issuance** (code complete; needs public server; ACME staging override available)
- [ ] **Remote backup storage** (S3/SFTP via BackupProvider)
- [ ] **Alert notification channels** (email/Slack)
- [ ] **DNS record hosting** (future DNSProvider; verification only today)
- [ ] **Phase 13 AI** (tool registry + permission layer) — explicitly deferred by owner
- [ ] Rate limiter is in-memory (single-instance; needs shared limiter for HA)
- [ ] cgroup/systemd resource limits for PHP pools beyond package FPM limits

---

## 10. Key Environment Variables

```
EPICPANEL_SECRET_KEY          AES-GCM vault key (missing → ephemeral + warning)
EPICPANEL_CORS_ORIGINS        default localhost:5173 dev origins
EPICPANEL_HEALTH_CHECK_INTERVAL  default 60s
EPICPANEL_ACME_EMAIL / EPICPANEL_ACME_DIRECTORY   Let's Encrypt config (staging override)
EPICPANEL_APACHE_PORT_RANGE / EPICPANEL_OLS_PORT_RANGE  backend port ranges
EPICPANEL_TEST_DATABASE_URL   enables integration tests
```

---

## 11. Where the Project Stands

```
Phases 0–12:  ✅ COMPLETE (architecture → foundation → servers → websites →
              runtimes → web servers → databases → domains/SSL → deployments →
              backups → monitoring → public API → multi-server)
Phase 13 AI:  ⬛ deferred
Frontend:     EpicHost WHM-style UI, 19 pages, iterating (site detail polish,
              staging UI, real storage numbers, notification panel, Ctrl+K)
ADRs:         037 accepted decisions documented in EPICPANEL.md
```
