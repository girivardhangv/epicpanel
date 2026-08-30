# EpicPanel API — v1

Base URL: `/api/v1`. All bodies are JSON. Errors use the envelope described in
`docs/architecture.md`.

Common status codes: `200`, `201` (created), `202` (accepted — job created),
`400` (`VALIDATION_ERROR`, `INVALID_JSON`), `401` (`AUTH_REQUIRED`,
`SESSION_EXPIRED`, `AUTH_INVALID_CREDENTIALS`), `402`
(`LICENSE_EXPIRED/SUSPENDED/INVALID/REJECTED`), `403` (`FORBIDDEN`,
`CSRF_TOKEN_INVALID`, `INSTALLER_LOCKED`), `404`, `409` (`CONFLICT`,
`SERVER_NOT_MANAGEABLE`), `422` (`WEAK_PASSWORD`, `NGINX_*_FAILED`,
`PHP_POOL_FAILED`, `FS_OPERATION_FAILED`), `429` (`RATE_LIMITED`), `502/503`
(licensing server issues, `AGENT_UNREACHABLE` semantics).

Cookies: `epicpanel_session` (httpOnly) and `epicpanel_csrf`; unsafe requests
must echo the latter in `X-CSRF-Token`.

## Public

| Method | Path                    | Notes                                             |
| ------ | ----------------------- | ------------------------------------------------- |
| POST   | /auth/login             | rate-limited per IP; sets cookies                 |
| POST   | /auth/logout            | revokes session; clears cookies                   |
| POST   | /auth/refresh           | rotates session token (sliding expiry)            |
| GET    | /auth/me                | requires session                                  |
| POST   | /auth/change-password   | session + CSRF                                    |
| GET    | /installer/status       | always readable; drives wizard/login routing      |
| GET    | /system/info            | product/version banner                            |
| GET    | /healthz                | process health                                    |

## Installer — locked server-side after completion

All POST, all refuse with `INSTALLER_LOCKED` once complete.

| Path                        | Purpose                                          |
| --------------------------- | ------------------------------------------------ |
| /installer/requirements     | live host probe report                           |
| /installer/database/test    | verify configured DSN reachability               |
| /installer/database/config  | stage alternative DSN (restart to adopt)         |
| /installer/license          | activate license key                             |
| /installer/configuration    | site name/timezone                               |
| /installer/administrator    | create first admin (+super_admin role binding)   |
| /installer/security         | password policy/lockout/session defaults         |
| /installer/complete         | final validation & permanent lock                |

## Product APIs (session + CSRF + permission)

| Method | Path                       | Permission        |
| ------ | -------------------------- | ----------------- |
| GET    | /dashboard/summary         | dashboard.view    |
| GET    | /servers                   | server.view       |
| GET    | /servers/{id}              | server.view       |
| DELETE | /servers/{id}              | servers.delete    |
| POST   | /servers/registration-tokens   | servers.create |
| GET    | /servers/registration-tokens   | server.view   |
| DELETE | /servers/registration-tokens/{id} | servers.delete |
| GET    | /servers/{id}/capabilities     | server.view   |
| POST   | /servers/{id}/capabilities     | server.manage (probe refresh) |
| GET    | /servers/{id}/php-versions     | server.view   |
| GET    | /domains                   | domains.view      |
| POST   | /domains                   | domains.create    |
| GET    | /domains/{id}              | domains.view      |
| DELETE | /domains/{id}              | domains.delete    |
| GET    | /websites                  | websites.view     |
| POST   | /websites                  | websites.create   |
| GET    | /websites/{id}             | websites.view     |
| PATCH  | /websites/{id}             | per-field: websites.php.manage (php_version), websites.config.manage (php_settings, alias_domain_ids) |
| DELETE | /websites/{id}?delete_files= | websites.delete |
| POST   | /websites/{id}/enable      | websites.edit     |
| POST   | /websites/{id}/disable     | websites.edit     |
| POST   | /websites/{id}/reload      | websites.edit     |
| POST   | /websites/{id}/retry       | websites.edit     |
| GET    | /websites/{id}/logs?type=access\|error&max_bytes= | websites.logs.view |
| GET    | /jobs/{id}                 | websites.view     |
| GET    | /monitoring/fleet          | monitoring.view   |
| GET    | /servers/{id}/metrics/current    | monitoring.server.view |
| GET    | /servers/{id}/metrics/history?range= | monitoring.server.view |
| GET    | /servers/{id}/metrics/network?range= | monitoring.server.view |
| GET    | /servers/{id}/metrics/disk?range=    | monitoring.server.view |
| GET    | /servers/{id}/metrics/services   | monitoring.services.view |
| GET    | /servers/{id}/metrics/processes  | monitoring.server.view + monitoring.processes.view (in-handler, audited) |
| GET    | /alerts                    | monitoring.view   |
| GET    | /alerts/rules              | monitoring.view   |
| PATCH  | /alerts/rules/{id}         | settings.manage   |
| POST   | /alerts/{id}/acknowledge   | monitoring.view   |
| GET    | /websites/{id}/health      | websites.view     |
| GET    | /system/internal-metrics   | settings.manage   |
| GET    | /license/status            | license.view      |
| POST   | /license/refresh           | license.manage    |
| POST   | /license/deactivate        | license.manage    |
| GET    | /roles                     | roles.view        |
| GET    | /permissions               | roles.view        |

Permission denials are audited as `authz.denied`. Website provisioning,
reconfiguration and deletion return `202` with a `job` object; poll
`GET /jobs/{id}` for real progress (`queued → running → completed/failed`).

## Agent protocol

| Method | Path               | Auth                                      |
| ------ | ------------------ | ----------------------------------------- |
| POST   | /servers/register  | `X-Agent-Key` header (single-use registration token) |
| POST   | /servers/heartbeat | `Authorization: Bearer <agent-token>`     |
| POST   | /servers/telemetry | `Authorization: Bearer <agent-token>`     |

Registration consumes the token atomically (single-use, expiring, revocable)
and returns `{ server, agent_token, ops_token }` once. `agent_token`
authenticates agent→panel calls; `ops_token` authenticates panel→agent calls
against the agent's management endpoint (`ops_addr` advertised via
registration/heartbeat). The panel never returns these tokens again.

### Telemetry ingestion (Phase 3)

`POST /servers/telemetry` accepts `{"samples":[...]}` — at most **60 samples
per batch**, each list field hard-bounded (24 disks, 32 interfaces, 20
processes, 32 services). Responses: `202` with
`{"accepted":n,"duplicates":n,"rejected":n}`. Enforcement:

* Unknown/revoked agents → `401` (same bearer-token store as heartbeats).
* Oversized bodies → rejected by the global body limit.
* Duplicate `(server_id, sequence)` pairs are no-ops (`duplicates`).
* Agent timestamps are stored for drift diagnostics only; **ordering uses
  `server_received_at`**, and future timestamps beyond a 10-minute skew are
  clamped to receive time.

## Agent management endpoint (panel → agent)

The agent serves `/agent/v1/*` on its listen address (default `:9200`), Bearer
`ops_token` on every call, strict request size limits, validated inputs:

| Method | Path                     | Purpose                                   |
| ------ | ------------------------ | ----------------------------------------- |
| GET    | /agent/v1/ping           | liveness + version                        |
| GET    | /agent/v1/system/info    | OS/arch/hostname snapshot                 |
| GET    | /agent/v1/nginx/status   | installed/version/running/layout          |
| POST   | /agent/v1/nginx/deploy-site | write → validate → atomic deploy (rollback on invalid config) |
| POST   | /agent/v1/nginx/remove-site  | remove managed site config        |
| POST   | /agent/v1/nginx/set-enabled  | enable/disable without rewriting  |
| POST   | /agent/v1/nginx/reload   | graceful reload                           |
| GET    | /agent/v1/php/versions   | discovered PHP runtimes                   |
| POST   | /agent/v1/php/pool       | ensure/remove per-site FPM pool or FastCGI listener |
| POST   | /agent/v1/fs/mkdir       | create directories inside the sites root  |
| POST   | /agent/v1/fs/write       | atomic file write inside the sites root   |
| POST   | /agent/v1/fs/remove      | remove file/dir inside the sites root     |
| POST   | /agent/v1/fs/user        | ensure shared site user (Linux)           |
| GET    | /agent/v1/logs/read      | bounded tail of allowed log files         |

There is no command-execution, file-read or arbitrary-path operation; every
path is resolved and proven to stay inside the sites root (or an allowlisted
log directory).

## Monitoring & telemetry (Phase 3)

### Metrics ranges

All historical endpoints accept `range=1h|6h|24h|7d|30d` (default `24h`);
anything else is a `400 VALIDATION_ERROR` and no query can exceed 30 days.

| Endpoint | Source | Response shape |
| -------- | ------ | -------------- |
| `/metrics/current` | newest raw sample + last-4 smoothed health | `{has_data, latest, health, thresholds, monitoring_capabilities}` |
| `/metrics/history` | raw (≤6h) → hourly (≤7d) → daily (30d) | `{range, interval_seconds, source, points[{t, cpu_usage, memory_usage, load_*, swap_usage, max_disk_usage}]}` (≤240 points) |
| `/metrics/network` | raw counters, rates derived centrally | `{range_seconds, interfaces:{name:{points[{t, rx_mbps, tx_mbps}]}}}` |
| `/metrics/disk`    | latest inventory + max-usage history | `{current[], history}` |
| `/metrics/services` | newest service snapshot | `{has_data, observed_at, services[]}` |
| `/metrics/processes` | newest bounded top-CPU/top-memory list | `{has_data, observed_at, processes[]}` |
| `/monitoring/fleet` | last-4 average per server | `{servers[{health, cpu_usage, memory_usage, max_disk_usage, uptime_hours}]}` |

Health states: `healthy | warning | critical | unknown | offline` with
settings-tunable thresholds (`monitoring.threshold_{cpu,memory,disk}_{warn,crit}`,
defaults 80/95, 80/95, 80/90). A single noisy sample never changes a state —
the evaluation averages the last four samples.

`monitoring_capabilities` reports `ok` only for metrics that actually
produced data in the latest sample, `unsupported` otherwise, and
`error:<reason>` when a collector failed — never a fabricated ✓.

### Alerts

* `GET /alerts?status=&server_id=` — newest 200 with rule, severity, status
  (`triggered | acknowledged | resolved`), metric value and timestamps.
* `GET /alerts/rules` / `PATCH /alerts/rules/{id}` — threshold, duration
  (seconds) and enable toggles (edit requires `settings.manage`).
* `POST /alerts/{id}/acknowledge` — only from `triggered`.
* Seeded rules: CPU >90% 5m (critical), Memory >90% 5m (critical), Disk >90%
  10m (warning), Server offline (critical), Service stopped (warning).
* **No alert storms**: a partial unique index allows exactly ONE active alert
  per (rule, server); re-evaluation refreshes `last_breached_at` instead of
  inserting. A breach requires ALL samples in the window to exceed the
  threshold AND the window to span ≥80% of the configured duration with ≥2
  samples. Recovery resolves the alert automatically.

### Retention & aggregation

Raw samples: 7 days · hourly aggregates: 30 days · daily aggregates: 1 year
(settings keys `monitoring.retention_{raw,hourly,daily}_days`). A background
maintenance worker (hourly, non-overlapping) folds complete hours/days into
`server_metric_hourly` / `server_metric_daily` (min/avg/max CPU & memory,
max disk usage per mount) and prunes expired data — never inside request
handlers.

### Panel-internal observability (foundation)

`GET /system/internal-metrics` (`settings.manage`) exposes the internal
registry: API request counters by route, response-class counts, average
latency, plus gauges reserved for job queue depth and telemetry rates. This
is an operator-facing foundation, not a customer dashboard feature.

---

## Phase 4 — SSL / ACME certificates

### Endpoints

| Method | Path | Permission | Notes |
| ------ | ---- | ---------- | ----- |
| GET | `/websites/{id}/certificate` | websites.view | Returns `{certificate}` or `null` |
| POST | `/websites/{id}/certificate` | websites.config.manage | Accepts `{auto_renew}`, returns `{job}` |
| DELETE | `/websites/{id}/certificate` | websites.config.manage | Tears down cert + reloads nginx |

### Issuance flow

1. Operator clicks **Issue certificate** on the website detail page.
2. Panel enqueues an `issue_ssl` job (the ACME order can take 10–30s).
3. The job handler reads `ssl.acme_mode` from settings (`production`, `staging` or `mock`).
4. **Production / staging mode**: the agent uses `golang.org/x/crypto/acme` to perform the
   full ACME HTTP-01 challenge: places challenge files in `<webroot>/.well-known/acme-challenge/`,
   accepts the challenge, waits for authorization, creates the order, and downloads the
   certificate chain. The agent persists the ACME account key under `<dataDir>/acme/`.
5. **Mock mode**: the agent generates a self-signed RSA-2048 certificate with a 90-day
   lifespan (no external network). Used for development panels without public DNS.
6. Certificate material is stored at `<nginxDir>/conf/ssl/<siteSlug>/fullchain.pem` and
   `privkey.pem`. The site's nginx configuration is regenerated with `listen 443 ssl`, `http2 on`,
   `ssl_certificate` directives, and an HTTP→HTTPS redirect. Nginx is then validated and reloaded.

### Auto-renewal

The maintenance worker queries `website_certificates` for certificates with
`auto_renew = true` and `expires_at < now() + ssl.auto_renew_days` (default 30 days)
and re-issues them through the same `issue_ssl` job flow.

---

## Phase 5 — Notification channels & operator settings

### Notification channels

| Method | Path | Permission | Notes |
| ------ | ---- | ---------- | ----- |
| GET | `/notifications/channels` | settings.view | Returns `{channels}`, config secrets redacted |
| POST | `/notifications/channels` | settings.manage | Create a channel (`webhook`, `slack`, `discord`, `email`) |
| PATCH | `/notifications/channels/{id}` | settings.manage | Update name, config, severity, enabled |
| DELETE | `/notifications/channels/{id}` | settings.manage | Remove channel |
| POST | `/notifications/channels/{id}/test` | settings.manage | Send a test message through the channel |

Channel types and their `config` fields:

| Type | Required config | Notes |
| ---- | -------------- | ----- |
| `webhook` | `webhook_url` | Generic JSON POST (works with Slack/Discord webhook URLs) |
| `slack` | `webhook_url` | Same as webhook with `{"text":"..."}` payload |
| `discord` | `webhook_url` | Same as webhook with `{"content":"..."}` payload |
| `email` | `smtp_host`, `smtp_port`, `from`, `to` | STARTTLS, optional `smtp_username`/`smtp_password` |

Each channel has a `severity` threshold (`warning` | `critical`). Warning channels
receive both warning and critical alerts; critical channels receive only critical ones.

### Alert → notification flow

When an alert is triggered, acknowledged or resolved, the alert evaluator enqueues a
`notify_alert` job per enabled channel whose severity threshold is met. The job handler
delivers the payload (event, severity, server, rule, message, metric value, threshold)
and records the delivery in the audit log. Delivery failures are retried with the runner's
standard backoff.

### Operator settings

| Method | Path | Permission | Notes |
| ------ | ---- | ---------- | ----- |
| GET | `/settings` | settings.view | Returns `{settings}` — all editable keys and their current values |
| PATCH | `/settings` | settings.manage | Accepts `{settings: {key: value, ...}}` — only known keys are accepted |

Editable keys include ACME mode/email, monitoring thresholds, and retention days.
Unknown keys are rejected with `VALIDATION_ERROR`.

### Development webhook echo

When the panel is running in `development` mode, a webhook echo endpoint is available
at `http://localhost:8080/dev/webhook`. POST to it records the payload in memory; GET
returns all received records. Configure a channel with `webhook_url` pointing at this
endpoint to test notification delivery without external services.

---

## Phase 6 — Managed databases (MySQL + PostgreSQL)

### Endpoints

| Method | Path | Permission | Notes |
| ------ | ---- | ---------- | ----- |
| GET | `/databases?server_id=&website_id=` | databases.view | Lists databases with users |
| POST | `/databases` | databases.create | `{server_id, engine, name, website_id?}` → `{database, job}` |
| GET | `/databases/{id}` | databases.view | Single database + users |
| DELETE | `/databases/{id}` | databases.delete | Drops DB on server (job) → `{job}` |
| POST | `/databases/{id}/users` | databases.users.manage | `{username}` → `{user, password}` (password shown once) |
| DELETE | `/databases/{id}/users/{userId}` | databases.users.manage | Drop user |
| POST | `/databases/{id}/users/{userId}/password` | databases.users.manage | Rotate password → `{password}` (once) |
| GET | `/servers/{id}/db-engines` | server.view | `{mysql:{configured,available,version}, postgres:{...}}` |

### How it works

- The agent connects to the local MySQL/MariaDB and PostgreSQL servers using
  **admin credentials configured via flags/env** (`-mysql-user`/`-mysql-password`,
  `-pg-user`/`-pg-password`, etc.). An engine with no credentials reports
  `configured: false` and is unavailable for creation.
- All DDL runs through **Go database drivers** (`go-sql-driver/mysql`, `pgx`) —
  never a shell. Identifiers are validated against a strict charset
  (`^[a-z][a-z0-9_]{0,62}$` for databases, `^[a-z][a-z0-9_]{0,31}$` for users)
  and quoted per engine; generated passwords use a symbol-free alphabet so they
  never need escaping.
- Database creation runs as a `provision_database` job; deletion as
  `delete_database`. **Passwords are never stored** — generated once and shown
  once; rotation issues a new one.
- MySQL users are created for both `localhost` and `127.0.0.1` and granted the
  database; PostgreSQL roles are granted the database and set as owner (PG15+
  schema access).

### Agent ops endpoints (panel → agent)

`GET /agent/v1/db/engines`, `POST /agent/v1/db/{create,drop}`,
`POST /agent/v1/db/user/{create,drop,password}` — all bearer-auth, validated.

---

## Phase 7 — Software Manager

| Method | Path | Permission | Notes |
| ------ | ---- | ---------- | ----- |
| GET | `/servers/{id}/software` | server.view | Live-detected components + host OS/pkg-manager |
| POST | `/servers/{id}/software/install` | server.manage | `{name}` → `{job}` (runs as a job) |
| POST | `/servers/{id}/software/remove` | server.manage | `{name}` → `{job}` |
| POST | `/servers/{id}/software/service` | server.manage | `{name, action}` start/stop/restart/enable/disable/status |

Components are detected live from the agent (no stale cache). Install/remove run
as `install_software` / `remove_software` jobs. The agent executes only a fixed
allowlist of binaries with provider-defined argv — never a request string. The
`epicpanel` CLI (`software list|install|remove|service`, `status`, `doctor`)
reuses the same engine.
