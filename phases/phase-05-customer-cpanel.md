# PHASE 5 — Customer cPanel

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 676–710 (Phase 5 spec) + lines 117–139 (packages/apps monorepo rule) + line 710 principle
> 3. `prompts/ui-ref.html` — customer screens: `screen-user-overview` ("Welcome back"), domains, DNS zone, email accounts, file manager, databases, backups, security, support; design tokens in `:root` (sidebar `#0c1526`, primary `#2563eb`, radius 14px, Inter)
> 4. `docs/architecture-report.md` + Phase 3/4 handoff notes (in those files)
> 5. Code: `frontend/src/{pages,components,lib,context}`
>
> Depends on: Phases 3–4 · Blocks: Phase 14

## Mission
The customer experience. Principle (verbatim): **"The customer shouldn't need to understand Linux."** Simple UX. Two experiences (WHM + cPanel) sharing ONE design system — "Don't build two completely separate frontend systems."

## Feature list — VERBATIM from master doc (Phase 5)
```
Dashboard
Websites | Domains | Subdomains | SSL
File Manager | FTP
Databases | phpMyAdmin
Email
DNS
PHP | Cron Jobs
Backups
Metrics
Security
Account
```

## Work Items
- [ ] Monorepo split per doc: `packages/{ui,icons,charts,tables,forms,design-system}` + `apps/{admin,customer}` — extract shared components from existing 22 pages; customer app consumes packages (admin lands Phase 6)
- [ ] Dashboard: resource cards (CPU/RAM/disk/bandwidth) using Phase 3 WS stream + **LIVE/STALE/OFFLINE freshness badges with age** (format: `updated 240ms ago` / `last update 18.4s ago`); quick actions; activity timeline
- [ ] Websites / Domains / Subdomains / Aliases / Redirects screens (Phase 4 APIs)
- [ ] SSL screen: status, issue, renew, expiry countdown
- [ ] File Manager (exists — align to ui-ref) + **FTP accounts screen** (Phase 4 module)
- [ ] Databases + phpMyAdmin launch (SSO exists: `pma_sso.go`)
- [ ] **Email accounts screen** — listed in doc; backend module is MISSING → flag to user, build UI against defined API contract only if backend lands, otherwise defer with note in handoff
- [ ] DNS zone editor (records CRUD)
- [ ] PHP selector (version + extensions); Cron jobs (exists)
- [ ] Backups (create/restore/download); Metrics history charts (historical store — separate from live cards, per Phase 3 rule "Do NOT use the historical database as your source for the live dashboard")
- [ ] Security: 2FA (Phase 2), sessions, API keys; Account settings
- [ ] UX kit per doc Phase 14 list (build once, admin reuses): responsive sidebar, breadcrumbs, global notifications, tables with filtering, confirmation dialogs, status badges, contextual actions
- [ ] Remove/hide raw Terminal & SSH keys from customer nav (admin-only; verify server-side authz too, not just UI)

## UI parity rule
Match `prompts/ui-ref.html` customer screens (layout, density, tokens). Icons: lucide (already in repo). Charts: lightweight lib consistent with Phase 2 stack note (Recharts or existing). **No emoji. No unnecessary animations.**

## Deliverables
`apps/customer` on shared packages; all screens above wired to `/v1` + WS.

## Definition of Done
A non-technical user completes: create site → upload file → create DB → issue SSL → restore backup — without any terminal. Every live value shows freshness state. No duplicated component code between customer/admin paths.

## Session Handoff — FILL BEFORE ENDING SESSION
- Packages extracted: all six scaffolds fleshed out + one more (deviation noted below). `packages/core` (NEW, ADR-048-style: the doc's six-package list had no home for api/ws/metrics/auth, so the shared data layer got its own package) — api client + ApiError, all wire types (SnapshotFrame/NodeSample/SiteSample/...), WS singleton client w/ backoff, useMetrics/useFreshness/computeFreshness/formatAge + REST seeders (moved verbatim from `src/lib/metrics.ts`), AuthProvider/useAuth/ROLE_RANK (moved verbatim from `src/context/AuthContext.tsx`), Phase-4 typed API helpers (domainsApi/redirectsApi/ftpApi/dnsApi/lifecycleApi). `packages/ui` — Card/CardHeader/StatCard(MetricCard)/StatusBadge/EmptyState/SkeletonRows/ProgressBar/ProgressRing, UX kit (PageTitle, Breadcrumbs, Toolbar/ToolbarSearch, MiniItem, UsageCard, QuickAction, FeatureTile, Initials, RowActions, pushToast/Toaster), AppSidebar (responsive collapsible navy shell w/ Ctrl+F search + org-switcher slot, nav passed as props), FreshnessBadge (verbatim Phase-3 formats). `packages/forms` — Modal, ConfirmDialog (destructive+neutral), Field, FormRow, Select, ErrorNote, InfoNote. `packages/charts` — AreaChart (ApexCharts-look SVG w/ hover tooltip), ChartControls, Sparkline. `packages/tables` — generic DataTable<T> (client-side search + filterSlot + loading/empty states), TableSearch, TableWrap. `packages/icons` — lucide-react re-export + IconComponent type. `packages/design-system` — tokens (colors/radius/shadows mirroring tailwind.config.js + index.css), TONE palette, CHART_COLORS, LIVE/STALE threshold constants. Workspaces wired (`workspaces: [packages/*, apps/*]`), tsconfig paths + vite aliases for `@epicpanel/*`, tailwind content includes packages+apps. Legacy root app (`frontend/src`) still builds — it consumes the same packages via back-compat shim files (`src/lib/*`, `src/components/{ui,cards,ref,FreshnessBadge}.tsx`, `src/context/AuthContext.tsx` are now re-export shims), so there is ZERO duplicated component code. Verify: `npm run build` = tsc -b + root vite build + apps/customer vite build, all green.
- Screens done vs deferred (esp. Email): DONE — `/` Dashboard ("Welcome back": WS-live CPU/RAM/Disk/Bandwidth UsageCards each carrying a LIVE/STALE/OFFLINE FreshnessBadge with verbatim age format; account strip; quick actions; activity timeline; history chart 24h; All-features grid), `/websites` + `/websites/:id` (status, usage, domains+aliases w/ verify-DNS + remove, redirects w/ pause/resume/delete, add-domain modal), `/domains` (org-wide table: primary/subdomain/alias filter, SSL + DNS state, add/verify/remove), `/dns` + `/dns/:website_id` (zone stats, records CRUD, publish, site picker), `/files/:website_id` (ui-ref table: Name/Type/Size/Modified/Permissions; upload, new file/folder, rename, delete w/ ConfirmDialog, editor modal, download), `/ftp` (per-site accounts, FTP/SFTP, generated strong password + one-time reveal modal, rotate, delete; matches Phase-4 one-time-password model), `/databases` (stats row, table, create, reveal credentials, phpMyAdmin/Adminer SSO launch via existing `/pma-sso`), `/php` (per-server PHP versions w/ site usage, managed extensions as toggle chips + catalog install), `/crons` + `/crons/:website_id` (presets, pause/resume, delete, last-run), `/backups` (health bars, per-site snapshot lists, Back-Up-Now, restore w/ ConfirmDialog gated to admin/owner like the server, schedule+retention modal), `/metrics` (historical store: 24h raw + 7d/30d rollup charts for CPU/mem + network; "Live now" strip clearly separated, WS-only), `/ssl` (per-domain status, issue/renew, expiry countdown chips: expired/≤14d amber/green, verify DNS, auto-refresh while issuing), `/security` (2FA setup/enable/recovery-codes/disable; API keys create/revoke; sessions card honestly says session listing has no backend endpoint yet — not faked), `/account` (profile + quick links). DEFERRED: Email accounts screen — backend module does not exist (no mailboxes table/handler found in `backend/internal`); the work item explicitly forbids building UI against a defined contract without the backend, so it is omitted with a note here and in `apps/customer/README.md`. No fake email data is rendered. Also removed from customer nav per work item: Terminal & SSH keys.
- Shared components Phase 6 can reuse: everything in `packages/*` — notably AppSidebar (pass admin nav groups + account block), PageTitle/Breadcrumbs/Toolbar/DataTable (filterable list screens), StatCard/UsageCard/MiniItem/QuickAction/FeatureTile (dashboards), Modal/ConfirmDialog/Field/Select/ErrorNote (all forms), StatusBadge + FreshnessBadge (status + live metrics), Toaster/pushToast (global notifications), AreaChart/ChartControls (metrics screens), and the entire `@epicpanel/core` data layer (api client, auth context, WS hooks). TwoFactorCard (`apps/customer/security/TwoFactor.tsx`) is app-level but a drop-in for admin too. Dashboard "Live now" strip + header FreshnessBadge show the intended admin pattern.
- Backend files touched (minimal authz fix, work item 6): `backend/internal/terminal/terminal.go` — terminal WS route was gated at `organizations.RoleDeveloper`; raised to `RoleAdmin` so the customer UI hiding matches server enforcement (developer-role org members could previously open a shell). `backend/internal/sshkeys/handler.go` — List/Create were `RoleDeveloper` (Delete was already `RoleAdmin`); raised List/Create to `RoleAdmin` for the same reason. `go build ./...` green; no test references these routes (verified by grep across `*_test.go`); no migrations touched. FINDINGS otherwise clean: every other `/v1` route resolves org membership server-side via `resolveOrg`/`RequireMinimumRole` with 404-cloaking for non-members, platform-admin inheritance correctly excludes API tokens, and `/v1/ws` is RequireUser + org-scoped.
- Verify commands + results: `npm run build` (frontend) → `tsc -b` clean, root vite build ✓ (dist 804 kB), apps/customer vite build ✓ (dist 382 kB); `npx tsc --noEmit` → zero errors; `go build ./...` (backend) → OK; `go test ./internal/terminal/... ./internal/sshkeys/...` → no test files (no assertions existed for these routes); dev proxy confirmed: root `:5173` and customer `:5174` both serve 200 on `/` and proxy `/v1/openapi.json` → 200 (ws:true intact in both vite configs).
- ADR-worthy: (1) ADR-048 — Shared data layer lives in `packages/core` (7th package beyond the doc's six): api/ws/metrics/auth are app-agnostic and both experiences import them; keeps "ONE design system" true at the logic layer, not just visually. (2) Monorepo adoption without build tooling: packages are source TS consumed via workspaces + tsconfig paths + vite aliases (no bundler-for-packages step) — smallest change that leaves `npm run build` green for the legacy root app AND the new app. (3) Terminal/SSH-keys authz raised developer→admin server-side: UI hiding is never the security boundary; recorded exact file:behavior diff above. (4) Email screen deferred (backend missing) rather than faked; sessions list likewise labeled "not available yet" instead of stubbed.
- Update `phases/README.md` status row for Phase 5 → DONE
