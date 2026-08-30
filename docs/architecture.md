# EpicPanel Architecture — Foundation Release

## Component boundaries

```
frontend (SPA)      backend (API)        agent (binary)
─────────────       ─────────────        ──────────────
Vite + React 19     chi router v5        platform/       ← OS abstraction boundary
TanStack Query      pgx v5 pool          system info     (linux/windows build-tagged)
React Router 7      internal/* services  client/         ← HTTP + credential store
Tailwind v4         embed migrations     agentd entry
```

* The browser talks **only** to `/api/v1/*` and static files. The agent talks to
  two endpoints (`register`, `heartbeat`). No shell execution is exposed anywhere.
* Business logic lives in feature packages (`auth`, `licensing`, `installer`,
  `servers`, `rbac`, `audit`, `settings`). Handlers only decode → call → encode.
* Every SQL statement is parameterized; the schema relies on DB constraints
  (unique/FK/CHECK) in addition to application validation.

## Decision records

### ADR-1 · Sessions are opaque, server-side and cookie-borne
Random 256-bit tokens (httpOnly, SameSite=Lax, Secure behind TLS). Only a SHA-256
digest of the token is stored. Sliding expiry with full token rotation on
`POST /auth/refresh`. Consequence: instant server-side revocation, no JWT secret
to rotate, and localStorage never touches auth state.

### ADR-2 · CSRF via per-session token
Each session row carries its own CSRF token delivered in a non-httpOnly cookie;
unsafe methods must echo it via `X-CSRF-Token` compared in constant time.
Installer endpoints apply CSRF only when a session cookie exists (install-time
browsers are anonymous but may carry a same-jar installed panel elsewhere).

### ADR-3 · Argon2id PHC hashes with transparent upgrade
Parameters default to m=19 MiB, t=2, p=1. `Verify` reports whether a stored hash
was made with weaker parameters so logins silently re-hash. Operators can raise
min-length/class policy through settings without code changes.

### ADR-4 · Brute force handled at three layers
1. Per-IP fixed-window limiter on login,
2. per-account failure counter with configurable lockout window,
3. uniform error/timing for unknown users (dummy Argon2 verify).

### ADR-5 · RBAC is the only authorization path
Permissions are seeded rows (`dashboard.view`, `server.manage`, …). Middleware
`RequirePermission(code)` resolves effective permissions through
users→roles→permissions joins on every request. No identity-specific branches
exist anywhere. Future resource-scoped permissions extend this table model.

### ADR-6 · Licensing is remote-bound with a pure grace policy
`LicenseService` interface: activate / validate / deactivate / refresh / status.
The licensing server URL is configuration; nothing about keys is hard-coded and
full keys are never stored locally (hint only, e.g. `EP…••••A21F`). `Policy`
evaluates validity as a pure function (unit-tested) so outage behaviour —
**grace mode**, never permanent lockout from short-lived network failures — is
explicit and auditable.

### ADR-7 · Installation state lives in PostgreSQL
Single-row `installations` table flips `pending → completed`; every installer
endpoint re-checks it server-side. After completion the endpoints refuse forever
(`INSTALLER_LOCKED`), independent of frontend routing.

### ADR-8 · Migrations are embedded and forward-only
SQL files under `backend/migrations/*.sql` are embedded (`migrations.FS()`) and
applied transactionally against `schema_migrations`. Schema changes are new
files, never edits of applied ones.

### ADR-9 · Cross-platform by compile, not runtime branching
Agent platform code lives in `platform_linux.go` / `platform_windows.go`
(+ darwin dev fallback). Panel requirement probes do the same. All four release
targets (linux amd64/arm64, windows amd64/arm64) compile from one tree — verified
in CI-style local runs.

### ADR-10 · Honest telemetry
The dashboard distinguishes **Real data / Loading / Unavailable / Not
configured**. No fabricated metrics are rendered anywhere; charts appear only
with real agent data in later phases.

## Error envelope & observability

```json
{ "error": { "code": "AUTH_INVALID_CREDENTIALS", "message": "Invalid credentials", "request_id": "…" } }
```

Internal failures collapse to `INTERNAL_ERROR` with the request ID surfaced;
the matching structured log line (slog) carries stack details server-side.
Every mutating action lands an audit row (actor, action, resource, IP, UA,
metadata) — credentials are excluded by construction (only deliberate actions
are logged).
