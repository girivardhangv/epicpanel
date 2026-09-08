# Phase 6 Handoff — Admin WHM

Agent: P6-ADMIN · Date: 2026-09-08 · Baseline: `eacffe5`

## Views done vs stubbed (billing/support)

All 20 verbatim nav tools are implemented in `frontend/apps/admin` on the shared
Phase 5 packages (`@epicpanel/{core,ui,icons,charts,tables,forms,design-system}`) —
no second component system:

| View | State | Data source |
|---|---|---|
| Dashboard | DONE | `adminview` overview aggregate + **WS live stream only** for fleet consumption / node health grid / per-node capacity bars; LIVE/STALE/OFFLINE + age on every live value (`FreshnessBadge`) |
| Nodes | DONE (health grid + per-node drill-down `/nodes/:id`: host metrics, systemd service states, runtimes) | live frames + `GET /v1/adminview/servers` |
| Servers | DONE (enroll flow w/ one-time token + instructions, maintenance-mode toggle w/ confirm) | org-scoped Phase 2 endpoints (platform admin passes) + adminview |
| Customers | DONE (cross-org orgs + users + service-account/token counts) | `GET /v1/adminview/organizations`, `/v1/adminview/users` |
| Accounts | DONE (cross-org filter by customer/node/status/search, **bulk suspend/resume** with ConfirmDialog) | `GET /v1/adminview/accounts` + audited suspend/resume |
| Plans | DONE (list/create + assign-to-org) | existing `/v1/admin/packages` (Phase 2) |
| Domains | DONE (SSL state + expiry chips fleet-wide) | `GET /v1/adminview/domains` |
| DNS | DONE (read-only zone inventory; record editing stays per-site cPanel) | `GET /v1/adminview/dns-zones` |
| IP Addresses | DONE — **honest** (see below) | `GET /v1/adminview/ports` |
| Web Servers | DONE (live nginx/apache/OLS/php-fpm/mariadb/docker states per service card) | live frames |
| PHP | DONE (runtimes per node + live php-fpm states) | adminview + live frames |
| Databases | DONE (credentials never exposed) | `GET /v1/adminview/databases` |
| Backups | DONE (real rows; Phase 11 scope stated in-UI, nothing faked) | `GET /v1/adminview/backups` |
| Security | DONE (users + 2FA adoption + service accounts/API tokens + platform defaults) | `GET /v1/adminview/users` |
| Monitoring | DONE (cross-org alert feed + node state counts) — **Phase 13 seam** | `GET /v1/adminview/alerts` |
| Logs | DONE (fleet audit trail + CSV export) | existing `/v1/audit-logs` (no org filter = admin) |
| Jobs | DONE (console: list/filter by status+type, retry, cancel, **dead-letter view**) | `GET /v1/adminview/jobs`, retry/cancel endpoints |
| **Billing** | STUB (honest empty state: "Billing arrives with Phase 10") | — |
| **Support** | STUB (honest empty state: no ticketing module exists) | — |
| Settings | DONE (panel hostname GET/PATCH; enforced defaults shown read-only) | `/v1/settings`, `/v1/settings/hostname` |

UX kit shared from Phase 5: AppSidebar (Ctrl+F tool search = the search palette
seam; ⌘K command palette is P14-UI's package addition), Toaster, ConfirmDialog,
DataTable, PageTitle, StatCard, FreshnessBadge, EmptyState. Zero emoji, Lucide
icons, Inter font, no animations beyond the shared fade/reduced-motion guard.

## Backend

- `backend/internal/adminview/` — fleet read model (`Service`): servers
  (+runtimes, counts), cross-org accounts (filters), organizations, domains,
  databases, backups, DNS zones, alerts, users+security posture, ports audit,
  live-frame seed, and the dashboard `Overview` (counts from PG; consumption,
  node health and service states ONLY from the in-memory LiveStore — the live
  path never reads the history DB).
- `backend/internal/api/phase6_adminview.go` — `func registerPhase6(s *Server,
  mux *http.ServeMux)`. **Coordinator must add the single call line in
  server.go** (mirrors registerPhase8's wiring pattern). All routes
  platform-admin-SESSION-only (401 anon / 403 non-admin / API tokens refused —
  same gate as `users.AdminHandler` + `/v1/jobs`).
- Mutations (all audited + bus events `admin.*`):
  - `POST /v1/adminview/accounts/{id}/suspend|resume` — reuses the Phase 4
    agent job contract exactly (`suspend_website`/`resume_website`, payload
    `{"website_id"}`, idempotency keys `suspend_<id>`/`resume_<id>`), guarded
    by website status (409 on illegal state, 404 unknown).
  - `POST /v1/adminview/jobs/{id}/retry` — `failed` → reset to pending
    (attempts cleared, history kept); `success` → fresh copy enqueued;
    pending/running → 409.
  - `POST /v1/adminview/jobs/{id}/cancel` — pending → terminal `failed` with
    `error='cancelled by administrator'`; running → 409 (lease-governed, said
    honestly rather than pretending to reach the agent).

## IP address management findings

`internal/ports` is NOT an IP manager: it defines two env-configurable backend
proxy-port ranges (Apache 6600-6999, OLS 7100-7499) + lowest-free allocator.
There is **no dedicated/shared IP pool table anywhere** (checked 0001-0029 +
Phase 9 0027). The IP Addresses view therefore shows: configured ranges with
utilization, per-site backend port allocations (websites.backend_port joined to
sites/orgs), enrolled node hostnames, and an explicit scope note that dedicated
IP pooling is unimplemented. Honest gap recorded; an IP pool module can slot
into the same agent verification seam later.

## Handed to Phase 13 (alert UI seams)

- `GET /v1/adminview/alerts?resolved=false|true` (cross-org feed with org
  names, severity, resource) — Phase 13's richer feed/drill-downs replace the
  page body in `apps/admin/pages/Monitoring.tsx`.
- Admin mutations publish `admin.account_suspend/resume`, `admin.job_retried`,
  `admin.job_cancelled` bus events — Phase 13 can subscribe to these topics.
- Dashboard "Needs attention" card and the `/monitoring` stat row are the
  intended landing zones for rule-engine alert counts.

## Deviations / coordinator notes

1. **README row not updated** — wave contract §Quality bar forbids editing
   `phases/README.md` (its line for Phase 6 still says TODO). Coordinator:
   flip to DONE with summary "apps/admin on shared packages; 18/20 views real,
   billing/support honest stubs; adminview read model + registerPhase6 (wire
   the call in server.go); jobs console w/ retry/cancel/dead-letter; IP view
   honest (port pools only, no IP pool module); 5 integration tests green."
2. **App.tsx ownership** — wave contract lists `frontend/apps/admin/App.tsx`
   as coordinator-owned, but the file did not exist and the P6 brief requires
   creating the scaffold (`App.tsx` listed verbatim). Created once from the
   brief, not touched since. When P10/P13 land: their `routes.billing.tsx` /
   `routes.monitoring.tsx` fragments replace the `/billing` and `/monitoring`
   entries in `routes.admin.tsx` (or App.tsx mounts them under the shell).
3. **`registerPhase6` is not yet called** in `server.go` (coordinator-owned) —
   it is compiled, vetted and integration-tested by building the route table
   directly (same pattern P8 used for registerPhase8).
4. **Test DB**: P6 was not assigned a suffix in the wave contract; created
   `epicpanel_test_adm` (owner `epicpanel`) and default the phase-6 tests to
   it. Override with `EPICPANEL_TEST_DATABASE_URL` as usual.
5. Frontend `tsconfig.tsbuildinfo` regenerated by running `tsc -b` (build
   artifact only).

## Test evidence

- `go build ./... && go vet ./...` — clean (whole backend, incl. in-flight P7/P10 files).
- `go test ./internal/api -run TestPhase6 -count=1` with
  `EPICPANEL_TEST_DATABASE_URL=...epicpanel_test_adm` — **5/5 PASS**
  (`phase6_adminview_test.go`): authz matrix (401/403 across all 14 read
  routes + 3 mutations), overview counts/distribution/ports, cross-org account
  filters, suspend/resume idempotency + audit rows + illegal-state 409s + 404,
  jobs retry/cancel/dead-letter lifecycle, secondary views (domains/databases/
  zones/backups/alerts/organizations/users) + honest ports note.
- `npx tsc --noEmit -p apps/admin/tsconfig.json` — clean.
- `npx tsc -b` (root, all apps+packages) — clean.
- `npx vite build --config apps/admin/vite.config.ts` — green:
  `dist/assets/index-*.js 352.40 kB (gzip 103.68 kB)`, port 5175.
- `npm run build:customer` — still green (shared packages untouched).
- Emoji sweep over all new files — clean.
