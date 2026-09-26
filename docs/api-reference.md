# EpicPanel API Reference (Admin Automation Guide)

EpicPanel is API-first (Invariant 6 in `EPICPANEL.md`): every operation the
frontend can do is possible through the versioned API. This guide explains how
to **control the entire panel — servers, websites, databases, domains, DNS,
SSL, backups, cron, FTP, packages, billing, users — programmatically**.

- Canonical prefix: `/api/v1` (the `/v1` spelling is equivalent — both serve
  the same routes).
- Machine-readable contract: `GET /v1/openapi.json` (OpenAPI 3.0.3, no auth;
  168 paths / 215 operations).
- Errors are JSON: `{"error": {"code": "...", "message": "..."}}`.
- Rate limits: 10/min (burst 20) on `/v1/auth/*`, 300/min (burst 600) on the
  rest, per client IP (per org+token for token traffic).

---

## 1. Principal types

| Principal | Header | Reach | Scopes | Notes |
|---|---|---|---|---|
| Browser session | cookie `epicpanel_session` (or `Authorization: Bearer <session>`) | user's own role | n/a | CSRF header `X-EpicPanel: 1` required on mutations |
| **Org API token** | `Authorization: Bearer epk_...` | one organization | explicit, deny-by-default | never inherits platform-admin (ADR-027/043) |
| **Platform admin API key** | `Authorization: Bearer epa_...` | **all organizations + admin surface** | explicit, deny-by-default | the API counterpart of an admin session |
| Agent token | `Authorization: Bearer agt_...` | agent channel only | n/a | data-plane principals |

All bearer traffic is CSRF-exempt. Both token/key types are SHA-256 hashed at
rest, carry optional expiry, are revocable, and are audited on
create/revoke/use (`last_used_at`).

### 1.1 Short URLs & your active organization

You never need to know an organization id. Every org-scoped route registered
as `/v1/organizations/{org_id}/X` is **also served at the short form
`/v1/X`** — the active organization resolves server-side, in this priority:

1. **Org tokens (`epk_`)**: always their bound organization (the header
   below is ignored — confinement cannot be escaped through aliases).
2. **`X-EpicPanel-Org: <org_id>` header** when present (admins and platform
   keys targeting a specific org).
3. Otherwise the caller's **primary organization** (earliest membership).

Every account is created inside its own auto-provisioned personal
organization (at signup and via `POST /v1/admin/users`), so for the common
case `/v1/websites`, `/v1/databases`, … simply work:

```bash
curl -H "$EPK" https://panel.example.com/api/v1/websites
curl -X POST -H "$EPK" -H "Content-Type: application/json" \
  https://panel.example.com/api/v1/databases -d '{"name":"appdb","engine":"mariadb"}'
```

Both spellings remain valid everywhere — the canonical `{org_id}` paths are
unchanged and are what the OpenAPI spec enumerates.

### 1.2 Platform admin API keys (`epa_`) — full control

An `epa_` key acts **as the platform admin**: it can reach every org-scoped
route for any organization, plus the admin-only surface
(`/v1/adminview/*`, `/v1/admin/*`, `/v1/jobs`, `/v1/settings`). Two scope
families apply:

- **Admin surface** (`/v1/adminview/*`, `/v1/admin/*`, `/v1/jobs`,
  `/v1/settings`, `/v1/audit-logs`): needs `admin:read` (GET) or
  `admin:write` (mutations).
- **Org routes** (`/v1/organizations/{org}/...`): needs the matching resource
  scope (e.g. `websites:write` to create a site, `databases:write` for a
  database) for the org named in the path — any org.

Guardrails:

- **Create/revoke keys is session-only** (`POST/DELETE /v1/admin/api-keys`
  reject keys) so a leaked key can never mint more privileges or revoke other
  keys to cover its tracks. Listing (`GET /v1/admin/api-keys`) is allowed with
  `admin:read`.
- The wildcard scope `"*"` expands to every valid scope at creation (full-
  control key; the stored list stays explicit and visible).
- The web terminal stays session-only by design; `epa_` keys are refused
  there. The WebSocket event stream (`/v1/ws`) is org-scoped — use a session
  or `epk_` token per org for event feeds.

#### Creating a full-control key

```bash
# As a logged-in platform admin (session cookie):
curl -X POST https://panel.example.com/api/v1/admin/api-keys \
  -H "Content-Type: application/json" -b cookies.txt \
  -d '{"name": "terraform-bot", "scopes": ["*"], "expires_in_days": 90}'
# -> {"admin_api_key": {...}, "raw_token": "epa_...", "note": "store this key now; ..."}
```

The raw key is returned **exactly once**.

```bash
export EPK="Authorization: Bearer epa_.........."

# Anything an admin can see, the key can see:
curl -H "$EPK" https://panel.example.com/api/v1/adminview/overview
curl -H "$EPK" https://panel.example.com/api/v1/adminview/accounts
curl -H "$EPK" https://panel.example.com/api/v1/jobs
curl -H "$EPK" https://panel.example.com/api/v1/admin/users

# ...and anything an admin can do, in ANY organization:
curl -X POST -H "$EPK" -H "Content-Type: application/json" \
  https://panel.example.com/api/v1/admin/users \
  -d '{"email": "newcust@example.test", "password": "...", "name": "New Cust"}'
```

### 1.2 Org API tokens (`epk_`) — least privilege for customer automation

Created by an org admin (`POST /v1/organizations/{org_id}/api-tokens`),
confined to that org regardless of the creator's other memberships, and
scope-gated per route (`x-scope` in the OpenAPI spec). Scope groups:

`org`, `websites`, `servers`, `runtimes`, `databases`, `domains`,
`deployments`, `backups`, `monitoring:read`, `alerts`, `billing`,
`audit:read` — each with `:read` (GET) and `:write` (mutations) except where
noted. Files, crons, FTP accounts, SSH keys, PHP settings, applications and
WordPress fall under `websites:*`; redirects and DNS under `domains:*`.

---

## 2. Asynchronous operations: the job pattern

Mutating infrastructure operations return **202 with a job id** and complete
through the job queue (agent executes, control plane converges state):

```
POST /api/v1/organizations/{org}/websites   -> 202 {"website": {...}, "job_id": "..."}
GET  /api/v1/organizations/{org}/websites/{id}/jobs   -> job history
GET  /api/v1/jobs                                      -> platform-wide recent + dead-letter (admin)
```

Job lifecycle: `pending -> claimed -> running -> finished | failed`
(retry ≤ 3 with backoff). Poll the jobs endpoints or subscribe to
`GET /api/v1/ws` for live events.

---

## 3. End-to-end automation walkthrough (all with one `epa_` key)

```bash
P=https://panel.example.com/api/v1
H="-H $EPK -H Content-Type:application/json"

# 1. Register a server, then enroll the agent on that box with the one-time token
curl $H -X POST $P/organizations/$ORG/servers -d '{"name": "web-01"}'
#    -> {"server": {...}, "registration_token": "reg_..."}  (24h TTL, rotatable)
#    on the server:  epicpanel-agent enroll --url https://panel... --token reg_...
curl $H -X POST $P/organizations/$ORG/servers/$SRV/runtimes -d '{"type":"php","version":"8.3"}'

# 2. Create a website (202 + provision job; omit server_id for auto-placement)
curl $H -X POST $P/organizations/$ORG/websites \
  -d '{"name":"client-site","runtime":"php","runtime_version":"8.3","web_server":"nginx","primary_domain":"client.example.com"}'

# 3. Attach a database
curl $H -X POST $P/organizations/$ORG/databases \
  -d '{"name":"appdb","engine":"mariadb","server_id":"'$SRV'","website_id":"'$SITE'"}'
curl $H $P/organizations/$ORG/databases/$DB/credentials   # audited reveal

# 4. Domains, SSL, DNS zone
curl $H -X POST $P/organizations/$ORG/websites/$SITE/domains -d '{"domain":"www.client.example.com","kind":"alias"}'
curl $H -X POST $P/organizations/$ORG/domains/$DOM/ssl -d '{"mode":"letsencrypt"}'
curl $H -X POST $P/organizations/$ORG/websites/$SITE/dns-zone -d '{"domain":"client.example.com","ttl":3600}'
curl $H -X POST $P/organizations/$ORG/dns-zones/$ZONE/records -d '{"name":"www","type":"CNAME","value":"client.example.com","ttl":3600}'
curl $H -X POST $P/organizations/$ORG/dns-zones/$ZONE/publish

# 5. Deploy code + files + cron + FTP
curl $H -X PATCH $P/organizations/$ORG/websites/$SITE/deployment-config \
  -d '{"repo_url":"https://github.com/acme/site","branch":"main","deploy_token":"ghp_...","web_dir":"public"}'

Private repos (GitHub/GitLab): set deploy_token to a personal access token
with contents-read scope — it is encrypted at rest, injected into the clone
URL agent-side only, and never returned by any API. Laravel-style apps:
web_dir is the running directory INSIDE the release ("public" for Laravel,
"web" for legacy Symfony, "" = release root); the vhost docroot resolves
through the release symlink, so deploys keep swapping atomically. A deploy
fails before activation if web_dir does not exist in the release.
curl $H -X POST $P/organizations/$ORG/websites/$SITE/deploy
curl $H -X POST $P/organizations/$ORG/websites/$SITE/crons -d '{"schedule":"*/15 * * * *","command":"php cron.php"}'
curl $H -X POST $P/organizations/$ORG/websites/$SITE/ftp-accounts -d '{"protocol":"sftp","label":"deployer","password":"...","home_subdir":"public"}'

# 6. Backups (manual or scheduled, restore any time)
curl $H -X POST $P/organizations/$ORG/websites/$SITE/backups
curl $H -X PATCH $P/organizations/$ORG/websites/$SITE/backup-config -d '{"schedule":"daily","retention":14}'
curl $H -X POST $P/organizations/$ORG/backups/$BACKUP/restore

# 7. Platform administration (formerly session-only, now API-complete)
curl $H -X POST $P/admin/organizations/$ORG/package -d '{"package_id":"'$PKG'"}'
curl $H $P/adminview/accounts                       # cross-org account table
curl $H -X POST $P/adminview/accounts/$SITE/suspend
curl $H -X POST $P/adminview/jobs/$JOB/retry        # rescue a dead-lettered job
curl $H -X POST $P/admin/packages -d '{"name":"Pro","kind":"shared","max_websites":25,"memory_limit_mb":256,"cpu_cores":2,"price_monthly_cents":1900}'

# 8. Observe everything
curl $H $P/adminview/overview
curl $H $P/admin/observability/nodes
curl $H $P/audit-logs
```

---

## 4. Endpoint map by area

Complete, authoritative list: `GET /v1/openapi.json` (each operation carries
an `x-scope` for org routes). Summary:

| Area | Highlights |
|---|---|
| Auth & MFA | register/login/logout/me, MFA setup/enable/disable/verify |
| Organizations | CRUD, members + roles |
| Servers & Agent | register/delete, registration-token rotation, metrics + history, capacity, maintenance, database-tools, agent enroll/heartbeat/stream/jobs |
| Websites | create (auto-placement), update, delete, suspend/resume, staging clone/promote, WordPress one-click, config/usage/rewrite rules, PHP settings, job history |
| File manager | list/read/write/create/rename/delete, upload, whole-site download |
| Domains / DNS / SSL | aliases, docroot, SSL modes (selfsigned/letsencrypt), verify-dns, redirects, zones + records + publish |
| Databases | create (mariadb/mysql/postgresql), drop, audited credential reveal, phpMyAdmin SSO |
| Deployments | config, deploy, rollback, history |
| Backups | manual/scheduled + retention, restore, unified-engine backups2, remote targets, schedules |
| Runtimes & Apps | install/remove runtimes, extensions, software detection; app process create/start/stop/restart/status/logs |
| Cron / FTP / SSH | per-site crontab sync, FTP accounts (+ password/reveal), SSH keys |
| Monitoring & Alerts | alerts, HTTP health checks, metrics history, admin alert-rules + ack/resolve, observability rollups |
| API access | org tokens, service accounts, platform admin keys (`/v1/admin/api-keys`) |
| Packages & Users | hosting packages CRUD + assignment, platform user management |
| Admin WHM console | cross-org views (accounts/servers/orgs/domains/databases/backups/dns/ports/alerts/users/jobs/live), account suspend/resume, job retry/cancel |
| Billing | org-side orders/invoices/subscriptions/payment-method; admin-side products/plans/invoices/settings/webhooks |
| Audit / Settings | audit trail, panel settings + hostname |

---

## 5. Security model quick reference

- **Deny-by-default scopes**: unmapped paths reject tokens outright;
  `x-scope` documents what each org route requires.
- **Org confinement**: `epk_` tokens are pinned to their issuing org — every
  org-resolution path enforces it (including organization detail/member
  routes). Cross-org access gets 404 (existence cloaking).
- **Admin surface isolation**: `admin:read`/`admin:write` scopes exist only on
  `epa_` keys — org tokens structurally cannot reach `/v1/admin*`.
- **Key hygiene**: raw keys/tokens shown once; only hashes stored; revocation
  is immediate; `last_used_at` surfaces unused keys; expiry supported; all
  lifecycle events audited.
- **Key creation/revocation requires an interactive admin session** — the one
  deliberate exception to "everything via API", in exchange for making a
  stolen key non-amplifying.

## 6. Admin UI

The admin app's **API Keys** page (Settings section) lists platform keys with
last-used/expiry state, creates keys (scope picker with the `*` shortcut) and
revokes them — the session-only rules above apply there too.
