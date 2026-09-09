# Phase 13 Handoff — Monitoring & Observability (P13-AL)

## UI section (frontend portion of Phase 13)

Status: **COMPLETE** — alert feed, rule config UI and all observability drill-downs built on the Phase 13 backend API; `npx tsc -b --force` and `npx vite build --config apps/admin/vite.config.ts` both green.

### Files shipped (exclusively owned set)

- `frontend/apps/admin/routes.monitoring.tsx` (new) — react-router v7 fragment: `/monitoring` (hub) + `/monitoring/:section` (alerts | rules | nodes | services | customers | minecraft | discord; unknown values fall back to the hub). All screens are platform-admin-only server-side (`requireAdmin` in `phase13_alerts.go`); the WHM shell itself already blocks non-admin sessions.
- `frontend/apps/admin/pages/monitoring/` (new dir, per wave-contract ownership):
  - `data.ts` — verbatim wire types (`AlertRow`, `RuleRow`, `ObsNode/Service/Customer/Workload`) mirroring `backend/internal/alerts` JSON tags and the handler row structs; metric catalog (threshold: cpu/ram/disk/mc_tps/mc_mspt/mc_players/discord_cpu/discord_ram/discord_uptime; state: node_offline/service_down/container_crashed/backup_failed/provisioning_failed; time: ssl_expiration — mirrors `validMetric()`); scope/severity catalogs; `ruleTrigger()` humanizer (comparison + threshold + breach duration + recovery margin).
  - `client.ts` — typed API client over the shared `@epicpanel/core` fetch stack: `GET /v1/admin/alerts?state=…`, `POST /v1/admin/alerts/{id}/ack|resolve`, `GET/POST /v1/admin/alert-rules`, `PATCH/DELETE /v1/admin/alert-rules/{id}`, `GET /v1/admin/observability/{nodes,services,customers,workloads}`.
  - `bits.tsx` — `StateChip` (LIVE/STALE/OFFLINE + age on every live value), `SeverityChip`, `usePolled` (15 s refresh for LiveStore snapshot endpoints — live per-VALUE freshness never comes from the poll), `SectionShell` (breadcrumbs + title), `FailedNote`.
  - `tree.tsx` — the VERBATIM overview tree as drill-down nav: Nodes(CPU/RAM/Disk/Network/Health) · Services(Nginx/Apache/OLS/PHP-FPM/MariaDB/Docker) · Customers(CPU/RAM/Disk/Bandwidth) · Minecraft(TPS/MSPT/Players) · Discord(CPU/RAM/Uptime).
  - `Alerts.tsx` — alert feed with state filter tabs (active | ack | resolved | all → `?state=`), per-row Acknowledge/Resolve actions, occurrences + last-seen, severity/state chips. **WS-driven**: subscribes to the platform socket; `alert.raised` / `alert.resolved` bus events (fanned out by the WS hub `dispatch`) trigger an immediate refetch.
  - `Rules.tsx` — rule table (name/class/metric/scope/trigger/severity/updated) + create & edit modals + delete confirm. Create sends the full POST body; edit sends exactly what PATCH accepts (description, comparison, threshold, duration_seconds, recovery_margin, window_days, severity) and shows name/class/metric/scope read-only. Hysteresis is first-class in the form: breach duration + recovery margin for threshold rules, window days for SSL time rules.
  - `Nodes.tsx` — node cards with CPU/RAM bars, Disk, Network rx/tx and a Health footer; LIVE/STALE/OFFLINE badge per node with client-side ticking age.
  - `Services.tsx` — matrix: nodes × the six verbatim services, Active/Down/no-data chips (down is only claimed when the stream actually reported the unit inactive; OFFLINE nodes show "no data").
  - `Customers.tsx` — per-account CPU/RAM/Disk/Bandwidth table, names resolved against the adminview accounts list (real rows only; unresolved ids stay ids).
  - `Workloads.tsx` — Minecraft (TPS with health coloring, MSPT, players, CPU; TPS=0 renders "not reported" — the honesty flag) and Discord (CPU, Uptime, restart count; RAM renders "—" — see deviations).
  - `Monitoring.tsx` — hub (`MonitoringOutlet`): stat cards (active/ack counts, nodes online, customers), the overview tree, the alert feed; routes every `/monitoring/:section`.
- `frontend/apps/admin/pages/Monitoring.tsx` (replaced) — now a one-line re-export of `MonitoringOutlet as MonitoringPage`, so the pre-existing `/monitoring` entry in `routes.admin.tsx` (P6-owned, not edited) resolves to the new implementation.
- `frontend/apps/admin/App.tsx` — **single edit as contracted**: added `import { routes as monitoringRoutes } from './routes.monitoring'` and spread it into the route array (`[...monitoringRoutes, ...routes, ...billingRoutes]`). Monitoring routes are spread first so the `/monitoring` hub and `/monitoring/:section` entries win; the P6 placeholder entry (same component) stays compatible in any order.

### Honest deviations / backend notes for the coordinator

1. `GET /v1/admin/observability/nodes` never populates `disk_percent`, `net_rx_bps`, `net_tx_bps` (left zero in the handler). The UI fills Disk/Network from the joined WS frame (`useMetrics`, P6 fleet pattern) when a frame exists and renders "—" with a "not reported by the stream" note otherwise — never a fake 0.
2. `GET /v1/admin/observability/workloads` returns `ram_percent: 0` always (stream AppSample carries no memory limit). The Discord RAM column renders "—" until the backend can project it.
3. `PATCH /v1/admin/alert-rules/{id}` hardcodes `enabled=true` and ignores name/rule_class/metric/scope — the edit UI mirrors exactly that (no disable toggle; immutable fields shown read-only). If rule disabling is wanted later, the PATCH contract needs an `enabled` field first.
4. `GET /v1/admin/alerts` returns `organization_id` (no org name) — the Customer column shows a short id with full id on hover, or "Platform" when null.
5. `obsServices.up` is `false` for OFFLINE nodes (nil map) — the UI distinguishes "no data" from "Down" using `node_state` to stay honest.

### Verification

- `cd frontend && npx tsc -b --force` → exit 0.
- `npx vite build --config apps/admin/vite.config.ts` → built (410 kB js / 51 kB css).
- Wire shapes cross-checked line-by-line against `backend/internal/api/phase13_alerts.go` (handler structs + `alerts.Store` scans) and `backend/internal/alerts/alerts.go` (Rule/Alert JSON tags); endpoints confirmed mounted on the live :8080 server (401 without session = route present, admin-gated as designed).
- No new npm packages; no emoji; lucide icons only; empty states are honest (loading skeletons vs genuinely-empty vs request-failed are distinct).

### Suggested coordinator follow-ups (out of my ownership)

- Remove the now-duplicate `/monitoring` entry from `routes.admin.tsx` (P6 comment already anticipates the replacement) and the `dist/` churn from rebuilds if dist is meant to be untracked.
- Optionally surface `/metrics` (panel self-telemetry) as a WHM screen later — the endpoint is live; no UI contract was specified for it.
