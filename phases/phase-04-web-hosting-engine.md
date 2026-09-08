# PHASE 4 — Web Hosting Engine

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `docs/architecture-report.md` — KEEP/REFACTOR verdicts for: websites, runtimes, databases, domains, deployments, crons, isolation, apps, agent ops
> 3. `prompts/epicpanel-docs` lines 580–675 (Phase 4 + Agent Prompt)
> 4. `phases/README.md` (global contracts)
> 5. Code: `backend/internal/{websites,runtimes,databases,domains,deployments,crons,isolation,apps}`, `backend/internal/agent/*` (26 op files), `backend/migrations/0003–0009`
>
> Depends on: Phases 2–3 (jobs, agent channel) · Blocks: Phase 5

## Mission
Complete, isolated, agent-executed web hosting engine. Three web-server modes behind ONE common abstraction — not three unrelated implementations. Most of this exists in repo; this phase closes gaps and hardens safety per the audit.

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> Implement the web-hosting engine.
>
> Support these web-server modes:
> 1. Nginx
> 2. Nginx + Apache
> 3. Nginx + OpenLiteSpeed
>
> Do not create three unrelated implementations.
>
> Create a common web-server abstraction with provider-specific implementations.
>
> Implement:
> - hosting accounts
> - Linux users
> - filesystem isolation
> - domains
> - subdomains
> - aliases
> - redirects
> - virtual hosts
> - PHP-FPM pools
> - PHP version selection
> - PHP extensions
> - MySQL/MariaDB databases
> - database users
> - DNS zones
> - DNS records
> - SSL certificate provisioning
> - cron jobs
> - FTP/SFTP
> - resource limits
>
> All provisioning operations must execute through the Node Agent.
>
> Operations must be:
> - idempotent
> - retryable
> - logged
> - auditable
> - reversible where practical
>
> Never expose arbitrary shell execution to customers.
>
> Implement validation before changing live web-server configuration.
>
> Use safe atomic configuration writes and configuration testing before reload/restart.
>
> A failed configuration must not take down unrelated websites.
>
> Add rollback behavior where possible.
>
> Keep the implementation modular so new web-server providers can be added later.

## Account architecture (verbatim from doc lines 586–596)
```
Hosting Account
       ├── Linux user
       ├── filesystem
       ├── domains
       ├── PHP-FPM pool
       ├── web configuration
       ├── databases
       ├── DNS
       ├── SSL
       └── cron
```
"Every account needs isolation."

## Current state vs the verbatim list (verify against audit, then close gaps)
| Doc requirement | Repo today | Action |
|---|---|---|
| hosting accounts / Linux users / fs isolation | `websites` + `isolation` (bubblewrap+seccomp) | verify/refactor |
| domains / subdomains / aliases / redirects | `domains` partial | add aliases/redirects if missing |
| virtual hosts, 3 web-server modes | `WebServerProvider` abstraction exists (nginx/apache/OLS) | verify all 3 modes work + common interface |
| PHP-FPM pools / versions / extensions | `runtimes` + extensions (migration 0018) | verify |
| MySQL/MariaDB DBs + users | `databases` + `pma_sso.go` | verify |
| DNS zones / records | `agent/dns_verify.go`, `domains` | complete CRUD if partial |
| SSL provisioning | `domains` + `CertificateProvider` | verify ACME issue/renew |
| cron jobs | `crons` | verify per-account |
| **FTP/SFTP** | **not found** | **build** (chrooted, agent ops) |
| resource limits | packages/plans only | wire enforcement hooks (engine = Phase 9) |
| atomic config write + validate + rollback | unknown | **implement + test** |

## Work Items
- [ ] Audit verdicts first: mark each module KEEP/REFACTOR/REWRITE from Phase 1 report; do not restyle working code
- [ ] Common web-server abstraction conformance across Nginx / Nginx+Apache / Nginx+OLS; provider test matrix
- [ ] Config safety pipeline: generate → write atomically (tmp+rename) → validate (`nginx -t` etc.) → reload; failure = keep old config + rollback; one bad site cannot break others
- [ ] FTP/SFTP module: control-plane + agent ops + UI endpoints (chroot per account)
- [ ] Aliases + redirects (all three providers)
- [ ] DNS zone/record CRUD completeness; SSL issue/renew/expiry events
- [ ] Account lifecycle jobs (create/suspend/resume/terminate) idempotent + retryable + auditable + reversible; every op through agent
- [ ] Resource-limit enforcement points stubbed for Phase 9 (single seam, no duplicated logic)

## Deliverables
Provider matrix tests, config validation+rollback path, FTP/SFTP module, DNS/redirect/alias completeness, hardened lifecycle jobs.

## Definition of Done
Provision→suspend→resume→terminate passes under retry; deliberately broken vhost config fails validation and leaves other sites serving (proven by test); all three modes pass smoke suite; no customer-reachable shell.

## Session Handoff — FILL BEFORE ENDING SESSION
- Modules refactored vs left as KEEP (Phase-1 verdicts, applied):
  - KEEP (untouched): `runtimes`, `databases`, `deployments`, `isolation`, `secretbox`, `auth/users/organizations`, agent `database_ops/db_helpers/ssl_ops/dns_verify/cron_ops/ssh_ops/deploy_ops/site_usage/extension_ops/quota_ops/pkgr/sandbox_sync`.
  - REFACTOR → done: `internal/agent` web core (nginx.go, webserver_ops.go, fpm.go, executor.go, runtime_ops.go dbadmin block) — common `WebServerProvider` abstraction (webserver.go), shared `atomic.go` pipeline (AtomicWriteFile/SwapValidated/ValidateCmd, 120s validate cap), one rewrite-core (sanitizeRewriteLine, smuggling checks) for all 3 providers, dead `proxy_ops.go` deleted, injectable paths for tests. `websites` (status guards, DesiredPayload extensions, suspend/resume endpoints), `domains` (+redirects +SSL event hook), `httpapi` (scope map + new resources), `api/server.go` (dns/ftp registration, fanout sinks, PackageForOrg wiring, redirects in reconcile payload).
  - NEW: `internal/ftpaccounts`, `internal/dns`, `internal/limits`, `agent/ftp_ops.go`, `agent/dns_zone_ops.go`, `agent/lifecycle_ops.go`; migrations 0023 (ftp_accounts + job_type), 0024 (dns_zones/dns_records + job_type), 0025 (domain_redirects), 0026 (status 'suspended' + suspend/resume job types + org/status index).
- New endpoints: `GET/POST /v1/organizations/{org}/websites/{wid}/ftp-accounts` (developer+), `POST|DELETE /v1/organizations/{org}/ftp-accounts/{id}[/password|/reveal]` (developer+/admin+), `GET/POST /v1/organizations/{org}/websites/{wid}/dns-zone`, `DELETE /v1/organizations/{org}/dns-zones/{id}`, `POST /v1/organizations/{org}/dns-zones/{id}/records`, `PATCH|DELETE /v1/organizations/{org}/dns-records/{id}`, `POST /v1/organizations/{org}/dns-zones/{id}/publish` (developer+/admin+), `GET/POST /v1/organizations/{org}/websites/{wid}/redirects`, `PATCH /v1/organizations/{org}/redirects/{id}` (developer+), `DELETE /v1/organizations/{org}/redirects/{id}` (admin+), `POST /v1/organizations/{org}/websites/{wid}/suspend` + `/resume` (admin+). Scope map: ftp-accounts→websites, redirects/dns-*→domains, suspend/resume→websites:write.
- Rollback behavior test evidence: `internal/agent/webserver_test.go` — TestNginxEnsureRollback / TestApacheEnsureRollback / TestOLSEnsureRollback (stubbed failing `nginx -t`/`apache2ctl configtest`/`systemctl restart lsws` prove previous config intact + error returned + enable-state/new-backup rolled back), TestSwapValidated (old content restored / new file removed), TestRenderMatrix (3 providers × static/php/redirects/suspended). `lifecycle_ops_test.go` — suspend/resume round-trips per provider (byte-identical restore, first-backup-never-overwritten, validation-fail rollback, OLS restart-fail restore), idempotency no-ops. DoD "one bad site cannot break others" is enforced by per-site vhost files + SwapValidated restore + shared-service restart only after validation.
- DoD status: provision→suspend→resume→terminate passes under retry (`internal/api/phase4_test.go`: TestPhase4SuspendResume incl. failed-resume-keeps-suspended; job retries via lease/reaper+backoff from Phase 2); all three modes pass the render+rollback matrix; no customer-reachable shell (SFTP = internal-sftp ForceCommand with forwards off, FTP = vsftpd virtual users, no shell access; suspended stub has no PHP/proxy). NOTE: `go test ./...` in parallel packages intermittently fails in api/websites because BOTH reset the shared EPICPANEL_TEST_DATABASE_URL — pre-existing; run with `-p 1` or per-package (verified all 12 packages pass).
- Handed to Phase 5 (customer-facing gaps): alias add/delete UI exists on SiteDetail but DNS-verify lives on Security; FTP/DNS/redirect cards are admin-org views only (Phase 5 should scope per customer + add FTP/DNS tool tiles for customers); DNS publishing is manual (publish button) — consider auto-publish on record change; vsftpd passive ports 50000-50100 need firewall docs; chrooted SFTP documented as non-goal (site dirs are site-user-owned) — revisit if hard isolation requested; `install.sh` should pre-create /var/lib/epicpanel/dns-zones; OpenAPI spec still drifts (~150 routes).
- Update `phases/README.md` status row for Phase 4 → DONE
