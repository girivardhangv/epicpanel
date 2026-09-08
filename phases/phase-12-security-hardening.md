# PHASE 12 — Security Hardening

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1071–1094 (Phase 12 — "Mandatory" checklist is the contract)
> 3. `docs/architecture-report.md` § security risks (Phase 1) + handoffs from Phases 2 (2FA/RBAC), 3 (agent auth), 8 (secret scrub), 9 (limits as control)
> 4. Code: `backend/internal/{auth,httpapi,secretbox,isolation,organizations,terminal}`, `frontend/src/context/AuthContext.tsx`
>
> Depends on: Phases 2–11 (hardens everything built so far) · Blocks: 15

## Mission
Close the mandatory checklist across control plane, agent, and UI. Verbatim rule: **"For customers: never expose arbitrary server shell access through the panel."**

## Mandatory checklist — VERBATIM from master doc
```
2FA | RBAC | API keys | audit logs | rate limiting | session security
CSRF protection where applicable | secure cookies | secret encryption
agent authentication | TLS | firewall integration | container isolation
filesystem isolation | resource limits | command restrictions
```

## Work Items (verify → fix → prove; most exist partially)
- [ ] 2FA: TOTP enroll/verify/recovery codes (Phase 2 foundation) — enforceable per role/instance-wide
- [ ] RBAC: authz matrix test suite — every route, every role, cross-tenant access must 404; permission strings (Phase 2) actually checked, not just roles
- [ ] API keys/service accounts: scope + expiry + rotation; agent tokens (`agt_`) hardened
- [ ] Audit logs: coverage sweep — every mutation writes an audit row (test asserts on route table)
- [ ] Rate limiting: per-route-class (auth stricter); WS connection limits
- [ ] Session security: rotation on privilege change, idle expiry, revocation on password change; secure cookies (HttpOnly/Secure/SameSite)
- [ ] CSRF: on cookie-authed mutations (token auth exempt — document which is which)
- [ ] Secret encryption: `secretbox` everywhere secrets at rest; **automated secret-leak scan** of logs + API responses (Phase 8 test extended platform-wide)
- [ ] Agent authentication: mTLS or signed request envelopes; replay protection (sequence/timestamp)
- [ ] TLS: terminate properly (control plane + agent channel); no plaintext fallback
- [ ] Firewall integration: per-node agent ops (nftables/ufw provider) + WHM controls
- [ ] Container + filesystem isolation: re-test escapes (bubblewrap/seccomp from `isolation`); Docker socket never reachable from customer paths
- [ ] Resource limits as security control: Phase 9 enforcement verified under abuse load
- [ ] Command restrictions: sweep `terminal`/`sshkeys`/`apps`/agent op registry — enumerate every customer-reachable exec path; each must be typed+allowlisted; remove or admin-gate the rest
- [ ] Dependency + secret hygiene: `govulncheck`, npm audit, no secrets in repo

## Deliverables
Hardening changes + authz matrix tests + secret-leak scan + signed-off checklist doc (`docs/security-checklist.md`).

## Definition of Done
Every mandatory line above is either ✅ (with test evidence) or ⚠ with explicit owner-approved exception; vuln scans clean or triaged.

## Session Handoff — FILL BEFORE ENDING SESSION
- Checklist final status table: (fill)
- Exceptions granted (who approved): (fill)
- Update `phases/README.md` status row for Phase 12 → DONE
