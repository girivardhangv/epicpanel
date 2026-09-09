# Phase 12 — Session Handoff

## Checklist final status table
Full table with file:line evidence: `docs/security-checklist.md`.
Headline: **11 PASS · 3 PARTIAL · 2 EXCEPTION** (owner-approved below).

- PASS: 2FA, RBAC (261-route authz matrix, cross-tenant 404 proofs), API keys, audit logs
  (route-table-wide audit coverage test), rate limiting classes, CSRF, secret encryption
  (platform-wide leak scan + planted-secret proofs), command restrictions sweep,
  container/filesystem isolation (isolation tests), resource limits (Phase 9 tests re-run).
- PARTIAL (implemented + wired, documented limits):
  - Session security: no idle-expiry timer / password-change revocation sweep (fix
    specified in checklist §session; auth store change needed).
  - Agent authentication: replay guard is WIRED (server.go chain) but advisory
    (absent headers pass) — strict mode flip after all agents emit headers; mTLS deferred.
  - Rate limiting: WS conn limiter built (per-principal/IP) — mount point documented,
    not wired (needs Hub handle hook; low risk).
- EXCEPTION (owner-approved, user sign-off 2026-09-09):
  - TLS: no TLS listener in v1 (reverse-proxy termination documented; autocert path specified).
  - Firewall integration: nftables counters exist (Phase 9); rule-management CRUD deferred.

## Exceptions granted (who approved)
Repository owner (user) approved shipping v1 with the two EXCEPTION rows and three
PARTIAL rows as documented — recorded here and in ADR-058.

## Coordinator actions taken
- AgentReplayGuard wired into the request chain (server.go, agentKeyFromRequest).
- /metrics route-class counters wired via requestMetricsMiddleware (verified ticking).
- WS limiter: left as documented mount point (see PARTIAL note).
