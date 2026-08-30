# EpicPanel

A production-minded hosting control panel foundation for managing **Linux and Windows** servers.

```text
React + TypeScript  ──HTTPS──►  Go API  ──►  Go Server Agent
                                │                │
                             PostgreSQL    ┌────┴────┐
                                           ▼         ▼
                                         Linux     Windows
```

This repository is a monorepo:

| Path        | Contents                                                        |
| ----------- | --------------------------------------------------------------- |
| `frontend/` | React 19 + TypeScript + Vite + Tailwind v4 panel UI             |
| `backend/`  | Go REST API (chi + pgx), installer, licensing, RBAC, audit      |
| `agent/`    | Cross-platform agent (Linux/Windows · amd64/arm64)              |
| `shared/`   | Contracts shared between components (see `docs/api.md`)         |
| `scripts/`  | Development helpers incl. a **dev-only** licensing stub server  |
| `docs/`     | Architecture decisions, API reference, operating notes          |

The first release deliberately implements **only the foundation**: first-run
installer, license activation, authentication, RBAC, audit logging, agent
registration and the dashboard shell. Feature modules come later — see
[Known limitations](#known-limitations).

---

## Quick start (development)

Prerequisites: **Go ≥ 1.26**, **Node ≥ 24**, **PostgreSQL ≥ 13**, optionally Docker.

### 1. Provision PostgreSQL

```bash
docker run -d --name epicpanel-db \
  -e POSTGRES_PASSWORD=devpassword \
  -e POSTGRES_DB=epicpanel \
  -p 5432:5432 postgres:16-alpine
```

The database must exist before the panel starts; migrations run at boot.

### 2. Start the API

```powershell
cd backend
$env:EPICPANEL_DATABASE_DSN = "postgres://postgres:devpassword@127.0.0.1:5432/epicpanel?sslmode=disable"
$env:EPICPANEL_SERVER_ENVIRONMENT = "development"
$env:EPICPANEL_LICENSE_API_URL = "http://127.0.0.1:9911"   # dev licensing stub (see below)
go run ./cmd/panel
```

The listener defaults to `:8080`.

### 3. Start the dev licensing stub

```bash
cd scripts/licensing-server && go run main.go   # listens on :9911
```

Any key of length ≥ 8 activates (`pro-…` keys map to the Pro plan). This stub
exists purely to exercise the real HTTP contract during development; production
panels point `EPICPANEL_LICENSE_API_URL` at your real licensing service.
There is no hard-coded bypass inside the panel itself.

### 4. Start the frontend

```bash
cd frontend
npm install
npm run dev          # http://localhost:5173, proxies /api → :8080
```

### 5. Walk the installer

Open <http://localhost:5173> — the backend reports `installed: false`, so you
land in the wizard:

1. Requirements — live host probes (OS, CPU, RAM, disk)
2. License — activate against the configured licensing server
3. Database — verify the configured connection (or stage an override)
4. Configuration — panel name & timezone
5. Administrator — first user gets `super_admin` through RBAC
6. Security — password policy, lockout thresholds, session lifetime
7. Finish — locks the installer permanently (server-side)

Then log in on `/login` with the credentials from step 5.

### 6. Enroll an agent

On any Linux or Windows machine (the panel host works too):

```bash
# grab the registration key in the UI: Servers → Connect a server
epicpanel-agentd -url http://panel-host:8080 -key "<registration-key>" -label "web-01"
```

Credentials are stored under `.epicpanel-agent/credentials.json` (0600). The
agent heartbeats inventory (CPU/mem/disk/hostname) every 60 s by default.

## Running the tests

```bash
# Backend unit tests (no DB required)
cd backend && go test ./...

# Agent tests + cross-compilation targets
cd agent && go test ./internal/platform/
GOOS=linux go build ./... ; GOOS=windows go build ./...   # amd64/arm64

# Frontend type-check, build and component tests
cd frontend && npm run build && npm test
```

## Production notes

* Build the SPA (`npm run build`) and point `EPICPANEL_DIST_DIR` at `dist/` so
  the Go binary serves it; or terminate TLS separately and keep static assets
  on a CDN/reverse proxy.
* In production the panel **requires** `EPICPANEL_DATABASE_DSN`, sets
  `Secure` cookies automatically, serves JSON logs, and never returns stack
  traces (requests carry IDs that correlate with server logs).
* Secrets are provided via environment/config file only. The file the panel
  writes (`data/panel.json`) is created with `0600`.

## Known limitations

Honest gaps in this foundation release:

1. No website/DNS/email/file/backup modules yet (navigation shows them as planned).
2. Sessions use an in-memory per-IP rate limiter; horizontal deployments need a shared store.
3. Licensing performs no offline signed-token verification yet — grace mode is time-window based.
4. The agent reports inventory snapshots only; streaming metrics land with the monitoring module.
5. Frontend test coverage covers primitives (`Button`, `Input`, API error envelope), not full e2e flows; Playwright integration is planned next phase.
6. IIS/other web servers are architecturally slotted but not implemented (Nginx abstraction only).
7. Login page link-out for password reset exists in architecture (audit/lockout ready) but the SMTP flow ships later.

## Recommended next module

**User & role management API/UI** — the RBAC tables, permission seeds and enforcement middleware already exist, so adding `POST /users`, role editing and screens gives administrators day-one operational value before deeper hosting features (websites) begin. See `docs/architecture.md`.
