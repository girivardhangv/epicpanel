# EpicPanel Operator Guide

Audience: whoever runs the host(s) that run EpicPanel itself.
End-user (hosting customer) documentation lives in `docs/customer-guide.md`.

## 1. Install

One command (Debian 11+/Ubuntu 20.04+):

```bash
curl -fsSL https://raw.githubusercontent.com/epicbyte/epicpanel/main/install.sh | bash
# or, from a checkout:
sudo bash install.sh
```

What it does, in order: installs prerequisites (postgres, nginx) → creates
the `epicpanel` role/database → **backs up any existing panel DB to
`/var/backups/epicpanel/epicpanel-pre-migrate-<ts>.sql.gz` (upgrades only,
7 kept)** → generates `api.env` with a fresh 64-hex secret → downloads
`epicpanel-api` + `epicpanel-agent` (previous binaries kept as `.prev`
for rollback) → writes the systemd units → starts the API (this applies
migrations) → waits for `/healthz` → enrolls the local agent → prints the
one-time setup URL (valid 1 h, single use) to create the admin account.

Hardened unit files with sandboxing live in
`deploy/epicpanel-{api,agent}.service`; the installer writes the simple
baseline units. To adopt the hardened ones:

```bash
useradd -r -s /usr/sbin/nologin -d /nonexistent epicpanel-api
install -m 0644 deploy/epicpanel-api.service /etc/systemd/system/
install -m 0644 deploy/epicpanel-agent.service /etc/systemd/system/
chown root:epicpanel-api /etc/epicpanel/api.env && chmod 640 /etc/epicpanel/api.env
systemctl daemon-reload && systemctl restart epicpanel-api epicpanel-agent
```

(The API unit runs the API as `epicpanel-api` with `ProtectSystem=strict`
and minimal `ReadWritePaths`; the agent keeps root but with a capability
bounding set — the reasons are documented in the unit file comments.)

## 2. Upgrade (backup-before-migrate)

Upgrades are re-running the installer. The ordering is enforced by
`install.sh`:

1. `pg_dump | gzip` → `/var/backups/epicpanel/epicpanel-pre-migrate-…` —
   the installer refuses to continue if the dump fails.
2. New binaries installed (old kept as `.prev`).
3. API restart = migrations apply (`schema_migrations` tracks them).
4. healthz gate: only a fully-migrated API answers; a failed migration
   stops the rollout with the restore point printed.

Rollback: see the block printed at the end of every install, and
`docs/scale-report.md` §9. Short version: stop services, dropdb/createdb,
`gunzip -c <dump> | sudo -u postgres psql epicpanel`, swap `.prev`
binaries back, start services. Website data lives on nodes — this
restores the panel DB only.

## 3. Configuration

All `EPICPANEL_*` variables, with safe defaults and comments:
`deploy/epicpanel.env.example` (install to `/etc/epicpanel/api.env`,
mode 640, owner `root:epicpanel-api`). Reference for what each one does
in code: `backend/internal/config/config.go`.

Key operational ones:

| Variable | Effect |
|---|---|
| `EPICPANEL_HTTP_ADDR` | listen address; use `0.0.0.0:8080` behind nginx |
| `EPICPANEL_ENV=production` | enables Secure cookies |
| `EPICPANEL_CORS_ORIGINS` | exact origins for API + `GET /v1/ws` Origin check |
| `EPICPANEL_TRUSTED_PROXIES` | CIDRs allowed to set X-Forwarded-For |
| `EPICPANEL_REDIS_URL` | unset = Postgres LISTEN/NOTIFY events; set = Redis pub/sub (multi-replica fan-out) |
| `EPICPANEL_SESSION_TTL` | login session lifetime (default 30 d) |

Changing the env file requires `systemctl restart epicpanel-api`
(the file is read once at start).

## 4. Secrets provisioning

- `EPICPANEL_SECRET_KEY`: `openssl rand -hex 32`. Written by the installer
  on first run; never committed. Rotation invalidates sessions — plan a
  window, announce, rotate, done (users log back in).
- `EPICPANEL_DATABASE_URL`: password is generated to
  `/opt/epicpanel/db_password` (600) by the installer and rotated into
  Postgres on each run. To rotate manually:
  `ALTER ROLE epicpanel WITH PASSWORD '…'` then update the env file.
- Agent enrollment tokens: mint in the panel (Servers → registration
  token) or `POST /v1/organizations/{org}/servers/{id}/registration-token`;
  they are single-purpose and expiring. Agent tokens (`agt_…`) are stored
  hashed server-side.
- Backups of secrets: `/etc/epicpanel/api.env` and
  `/opt/epicpanel/db_password` must be backed up out-of-band (they are
  NOT in the pg_dump).

## 4b. API automation (admin keys)

Everything the panel does is scriptable. Principal types: browser session,
`epk_` org tokens (org-confined, scoped), and `epa_` **platform admin API
keys** — the machine counterpart of your admin login (any organization plus
the `/v1/admin*` surface, always scope-gated).

```bash
# create a full-control key (admin session; raw shown once):
curl -X POST https://panel/api/v1/admin/api-keys -b cookies.txt \
  -H "Content-Type: application/json" -d '{"name":"automation","scopes":["*"]}'
# then:  Authorization: Bearer epa_...
```

Rules worth knowing: keys/tokens are hashed at rest and revocable
(`/api-keys` page in the admin UI); create/revoke of platform keys requires
an interactive admin session (a stolen key cannot mint keys); the OpenAPI
contract lives at `GET /api/v1/openapi.json`; the full walkthrough is
`docs/api-reference.md`.

## 5. Service management

```bash
systemctl status epicpanel-api epicpanel-agent
systemctl restart epicpanel-api        # applies migrations on start
journalctl -u epicpanel-api -f         # request log + stream lifecycle
journalctl -u epicpanel-agent -f       # agent ops log
curl -s 127.0.0.1:8080/healthz         # {"status":"ok"} or 503 degraded
curl -s 127.0.0.1:8080/metrics         # Prometheus text format
sudo epicpanel-api setup-token         # re-print the setup link
```

Log rotation: `deploy/logrotate-epicpanel.conf` (journald caps the
journal itself; the file covers file-transport + nginx vhost logs).

## 6. Backups and drills

- Panel DB: installer takes a pre-migrate dump on every upgrade.
  Also schedule a nightly `pg_dump` (cron) to the same directory; the
  runbooks assume the dump exists.
- Node data: backups run through the panel (Phase 11) with verify
  hashes; see `docs/runbooks/backup-failed.md`.
- Drills (safe on a dev box): `deploy/chaos/*.sh` — run each at least
  once after install and after major upgrades; they print their own
  verification commands.

## 7. Monitoring

- `/healthz` for liveness (503 when the DB is unreachable — see
  `docs/runbooks/db-failover.md`).
- `/metrics` for Prometheus; alert names match
  `docs/runbooks/*` (node.offline, disk thresholds, backup/job failures,
  website.down from the HTTP checker).
- Alert feed inside the panel: `/v1/admin/alerts` (ack/resolve lifecycle,
  rules configurable at `/v1/admin/alert-rules`).

## 8. Multi-replica notes

Statelessness audit: `docs/scale-report.md` §4. REST is stateless
(sessions in Postgres). Set `EPICPANEL_REDIS_URL` on all replicas for
cross-replica WS event fan-out; live metrics projections are per-process
(REST falls back to the DB-backed view within 10 s). No sticky sessions
required.
