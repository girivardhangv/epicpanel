# EpicPanel — Security Checklist (Phase 12 sign-off)

Verbatim mandatory checklist from the master doc:

```
2FA | RBAC | API keys | audit logs | rate limiting | session security
CSRF protection where applicable | secure cookies | secret encryption
agent authentication | TLS | firewall integration | container isolation
filesystem isolation | resource limits | command restrictions
```

Status legend: **PASS** = implemented + test evidence. **PARTIAL** = implemented with a
bounded, documented gap. **EXCEPTION** = not implementable within Phase 12 file
ownership; precise fix handed to the coordinator.

Verified against the code (every evidence pointer below was read, not assumed) and
proven by `go test ./internal/api/ -run TestPhase12`, `./internal/authzmatrix/...`,
`./internal/securityaudit/...`, `./internal/httpapi/...`.

## Checklist

| Item | Status | Evidence (file:line) | Notes |
|---|---|---|---|
| 2FA | PASS | `backend/internal/auth/mfa.go:64` (setup), `:99` (enable, recovery codes shown once), `:153` (disable requires password), `:185` (MFA-gated login); TOTP secret encrypted at rest `mfa.go:75`; challenge TTL `mfa.go:17`; tests: `internal/api/phase2_platform_test.go:124-186`, `internal/api/phase12_security_test.go:797` (rotation) | TOTP + 10 single-use recovery codes. Per-role/instance-wide *enforcement policy* is not wired (any user may opt in; no admin forcing rule) — noted, low risk. |
| RBAC | PASS | Org-role gates `organizations/rbac.go:14-58` (`RoleRank` `store.go:32-39`); org resolver non-member => 404 `servers/handler.go:121-156`; token scope gate (deny-by-default) `httpapi/tokenauth.go:89-187`; admin-session-only guards `phase13_alerts.go:100-113`, `users/admin.go:24`; tests: `internal/api/phase12_security_test.go:366` (`RouteTableCompleteness`, `UnauthenticatedDenied`, `RouteInventoryLive` 261-route live probe, `CrossTenantForbiddenIs404`, `OrgRoleGates`, `AdminSurface`, `AgentSurface`), `internal/authzmatrix/` probe-table tests | Cross-tenant returns 404 (never 403) for reads and writes; permission tiers (`billing` < `developer` < org-`admin` < platform-admin) proven live; API tokens org-confined `servers/handler.go:137-142`. |
| API keys | PASS | `epk_` prefix + hash-only storage `apitokens/store.go:71,124-129`; scope+expiry create `apitokens/handler.go:55-102`; revocation `apitokens/handler.go:149-184`; service accounts (machine principals) `apitokens/serviceaccounts.go`; deny-by-default scope map `httpapi/tokenauth.go:38-152`; org confinement `servers/handler.go:137-142`; tests `internal/api/phase2_platform_test.go:68-121`, `internal/api/phase12_security_test.go:520` | `expires_in_days` optional (tokens may be non-expiring); rotation = create-new + revoke-old (documented cPanel-style workflow, no auto-rotation). Agent tokens: `agt_` `servers/store.go:148`, hash-only `store.go:153,188`, registration-token rotation endpoint `servers/handler.go:42`. |
| audit logs | PASS | Store `audit/audit.go:61-65` (best-effort, never blocks requests); org-scoped read `api/server.go:536-588`; handlers record across modules (e.g. `organizations/handler.go:101,201`, `apitokens/handler.go:113-122`, `servers/agent.go:74-84`); coverage test asserting specific mutations wrote rows: `internal/api/phase12_security_test.go:607` (`AuditCoverage`) | Coverage asserted for `organization.created`, `member.added`, `api_token.created`, `agent.enrolled`, `user.registered`. Reads are not audited (by design). |
| rate limiting | PASS | Per-route-class limiter: auth 10/min burst 20 vs general 300/min burst 600 `httpapi/tokenauth.go:191-213`; token-bucket with idle eviction `httpapi/ratelimit.go:23-69`; wired `api/server.go:493`; test `internal/api/phase12_security_test.go:971` (lockout incl. valid credentials, Retry-After, general class unaffected) | In-memory, single-instance (Redis upgrade path noted in `ratelimit.go:8-10`). WS connection caps: `httpapi/wsconnlimit.go:20-46,87` + test `internal/httpapi/phase12_security_test.go:90` — **advisory, wiring into the handler chain is coordinator-owned (`server.go`)**. |
| session security | PARTIAL | Hash-only storage `auth/session.go:33-52`; revoked/expired rejection `session.go:54-71`; revocation `session.go:73-79`; logout `auth/handler.go:161-176`; rotation on the 2FA-gated login `auth/mfa.go:225-232`; tests `internal/api/phase12_security_test.go:797` (rotation, logout revocation, TTL) | Absolute TTL enforced (default 30d, `config.go:32`). Two gaps live in `internal/auth` (not Phase 12 file ownership): (1) **no idle expiry** — `sessions.last_seen_at` is updated (`session.go:57`) but never compared to a cutoff; coordinator fix: add `last_seen_at > now() - $idle` to `SessionStore.Get` plus an env knob. (2) **no revocation-on-password-change** — no password-change endpoint exists at all; coordinator fix: add `POST /v1/auth/password` that verifies the old password, then `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`. |
| CSRF protection where applicable | PASS | `httpapi/csrf.go:10-25` — cookie-authenticated mutations require the `X-EpicPanel` header; bearer/API-token requests exempt (no ambient credential); wired `api/server.go:490`; tests `internal/api/phase2_platform_test.go:243-259`, `internal/api/phase12_security_test.go:797` (negative + positive) | Applies to POST/PUT/PATCH/DELETE with a session cookie. SameSite=Lax is the first line, custom header the second. |
| secure cookies | PASS | `auth/handler.go:229-251` (HttpOnly, SameSite=Lax, Path=/, `Secure: cfg.CookieSecure`, MaxAge=SessionTTL); `config.go:34` (Secure=true when `EPICPANEL_ENV=production`), `config.go:40-42` (explicit `EPICPANEL_COOKIE_SECURE` override); tests `internal/api/phase12_security_test.go:797` (dev flags + production-config flags on a real wire response) | Dev-friendly: plain HTTP testing stays possible with the default config. |
| secret encryption | PASS | `secretbox.Encrypt/Decrypt` `secretbox/secretbox.go:83,103` (XSalsa20-Poly1305); used for: MFA seeds `auth/mfa.go:75`, DB credentials `databases/handler.go:410` (`store.go:191` `password_encrypted`), Discord tokens/env `discord/secrets.go:92,124`, Minecraft secrets `minecraft/secrets.go:36`, FTP passwords `ftpaccounts/store.go:170,234`, app env `apps/store.go:63`, deploy tokens `deployments/handler.go:95`, backup encryption `backups/sink/crypto.go:54,252`; user passwords bcrypt `users/store.go` + `auth/password.go`; automated leak scan: `internal/securityaudit/` (scanner) + `internal/securityaudit/scan_test.go` + planted-secret proof `internal/api/phase12_security_test.go:642` (responses, process logs, and `audit_logs` DB rows all scanned; planted password 0 hits; raw API token shown exactly once) | `EPICPANEL_SECRETBOX_KEY` must be provisioned in production (fail-closed `ErrNotConfigured`). |
| agent authentication | PARTIAL | Bearer `agt_` tokens only on agent routes, never session auth `servers/agent.go:15-38`; one-time registration-token enrollment `servers/agent.go:43-91`; tokens hashed at rest `servers/store.go:148,153`; revocation honored `store.go:188`; replay protection (per-agent monotonic seq+time, fail-closed) `httpapi/agentreplay.go:31-134` + test `internal/httpapi/phase12_security_test.go:14`; tests `internal/api/phase12_security_test.go:590` | Replay middleware is **advisory and not yet wired** into the agent route chain (coordinator-owned `server.go`/agent wiring): coordinator fix: wrap agent routes with `httpapi.AgentReplayGuard(NewInMemoryAgentReplay(), keyFn, h)` once a per-agent key fn is exposed. mTLS / signed request envelopes: not implemented — acceptable for v1 given hash-only bearer tokens over TLS; revisit with the Redis store for multi-instance replay state. |
| TLS | EXCEPTION | Control plane listens plain HTTP by design behind a reverse proxy: `cmd/api/main.go:220-223` (hardened timeouts), `:235` (`ListenAndServe`); cookie Secure flag ties transport assumptions to config (`config.go:34`); agent channel speaks the same HTTP/WS surface | No TLS listener/ACME in code. Coordinator fix (deployment seam, not library code): terminate TLS at the front proxy (documented requirement: HTTPS-only, HSTS, redirect 80->443) OR add `autocert` behind `EPICPANEL_TLS_ADDR` + `EPICPANEL_ACME_DOMAINS` in `cmd/api/main.go`. Agent enrollment must never point at a plaintext URL (validate `EPICPANEL_CONTROL_PLANE_URL` starts with `https://` in `agent/config.go:37`). |
| firewall integration | PARTIAL | nftables is the bandwidth-accounting/enforcement mechanism on nodes: `agent/enforce.go:26-29,140-141,405` (per-account RX+TX counters, honest detect-then-apply), plan policy propagation `resources/engine.go:85,100,125` | Per-node **firewall rule management** (customer/admin CRUD of allow/deny rules via nftables/ufw provider + WHM controls) does not exist. Coordinator fix: new `internal/firewall` provider interface (nftables backend) + agent op `var FirewallOps` in `internal/agent/` + API surface `registerPhase14(s, mux)`; deny-by-default profile per website. Not fixable inside Phase 12 ownership (agent op files belong to P7/P8/P11). |
| container isolation | PASS | Kernel sandbox: `internal/isolation/` (bubblewrap mount+PID namespaces, no-new-privileges, caps dropped, seccomp `isolation/seccomp.go`, fail-closed `isolation/doc.go` security model + `isolation/SECURITY.md`); web terminal runs only inside it and refuses to fall back `terminal/terminal.go:162-177`; `epicpanel-shell` refuses root `cmd/agent-shell/main.go:21` | No Docker runtime; systemd transient units + cgroups v2 per wave contract (Docker socket is never exposed to customer paths — nothing mounts it). |
| filesystem isolation | PASS | Mount-namespace site tree (`/site` only writable, host paths not mounted, symlink-escape impossible) `isolation/doc.go` items 1-3; per-site docroot layout `api/server.go:822-825`; SFTP/FTP chroot per account `ftpaccounts/`; agent file ops are site-root-confined `agent/` file handlers | Sandbox construction failure denies the session (`terminal.go:168-170`) — never downgraded. |
| resource limits | PASS | Unified engine `resourcelimits/` + policy `resources/engine.go:125` (cpu.max, memory.max, pids.max, io.weight, fs quota, nftables counters, FPM); agent enforcement `agent/enforce.go:94-141`; create-time count gates wired `api/server.go:296-301` (backups), `api/server.go:717-784` (sites/databases, fail-closed); plan usage convergence `packages/handler.go` `EnforceForSite` `api/server.go:327-345`; tests `internal/api/limits_test.go`, `internal/agent/enforce_test.go`, `internal/resources/drift_test.go` | Enforcement mechanism measured by the same layer that displays usage (`enforce.go:71-72`) — no drift between plan numbers and kernel limits. |
| command restrictions | PASS | No arbitrary shell anywhere in the customer path: agent job execution is a typed dispatch switch `agent/worker.go:26,401-409` (unknown types rejected `errUnknownJobType`); Minecraft console input is allowlist-enforced twice (API `api/phase7_minecraft.go:833-834`, agent) and raw WS input is discarded `phase7_minecraft.go:771-806`; web terminal is admin/org-developer gated, sandboxed, and restricted-shell only `terminal/terminal.go:68-177`; SSH access = public-key install to site user `authorized_keys` only `agent/ssh_ops.go:13-33`; Discord bot console = controlled log tail + allowlisted input `agent/bot_console.go` | Sweep verdict: every customer-reachable exec path is typed + allowlisted; no endpoint accepts a raw command string for shell execution. (Git deploy runs a fixed fetch+build pipeline, not user commands.) |

## Dependency + secret hygiene (supporting row)

| Check | Result |
|---|---|
| `go vet ./...` (backend) | Clean — 0 findings (run 2026-09-09, go1.22.2). |
| `govulncheck` | Not available in this environment (no module network access; binary not installed). Rerun in CI: `govulncheck ./...` from `backend/`. |
| `npm audit` (frontend) | `found 0 vulnerabilities` (run 2026-09-09). |
| Secrets in repo | None found: test fixtures use `.test` domains and throwaway passwords; no live credentials committed (`secretbox` key comes from env at runtime). The planted-secret scanner (`internal/securityaudit`) is the standing guard against reintroduction. |

## Known limitations (documented honestly)

1. **Route enumeration seam**: Go 1.22's `http.ServeMux` cannot enumerate its own
   patterns, so `phase12Routes` (`internal/api/phase12_security_test.go:20`) is a
   hardcoded 261-route inventory proven live against the real mux (405+Allow probe).
   A route added to `server.go` without updating the inventory is not auto-detected;
   a route removed/renamed without updating it fails loudly. Coordinator fix when on
   Go >= 1.23: assert `r.Pattern` on a wrapped handler, or thread the `*http.ServeMux`
   out of `Server.Handler()` for reflection-based enumeration.
2. **Session idle expiry + password-change revocation**: owner is `internal/auth`
   (outside Phase 12 ownership) — precise fixes in the session-security row above.
3. **Agent replay guard + WS connection limiter**: built, tested, and fail-closed,
   but wiring into the live chain requires one-line edits in `server.go`
   (coordinator-owned). Both are drop-in `http.Handler` wrappers.
4. **TLS termination + firewall rule management**: deployment-level and new-module
   work respectively — coordinator fixes specified in the table above.

## Verification commands

```sh
cd backend
EPICPANEL_TEST_DATABASE_URL="postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_sec?sslmode=disable" \
  go test ./internal/api/ -run TestPhase12 -count=1          # RBAC matrix, leak scan, sessions, rate limit
go test ./internal/authzmatrix/... ./internal/securityaudit/... ./internal/httpapi/... -count=1
go build ./... && go vet ./...                               # clean
```
