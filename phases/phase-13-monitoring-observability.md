# PHASE 13 — Monitoring & Observability (alerts)

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1096–1142 (Phase 13 — overview tree + alert list are the contract)
> 3. `phases/README.md` + Phase 3 handoff (stream + node status), Phase 6 handoff (WHM seams), Phase 10/11 handoffs (provisioning/backup events)
> 4. Code: `backend/internal/monitoring`, `frontend/src/pages` (WHM views)
>
> Depends on: Phases 3, 6, 9, 10, 11 · Blocks: 15

## Mission
Admin global observability + alerting on top of the Phase 3 live pipeline. Alert evaluation must be async — never inside request handlers (Phase 3 rule).

## Overview tree — VERBATIM from master doc
```
Nodes     ├── CPU ├── RAM ├── Disk ├── Network └── Health
Services  ├── Nginx ├── Apache ├── OLS ├── PHP-FPM ├── MariaDB └── Docker
Customers ├── CPU ├── RAM ├── Disk └── Bandwidth
Minecraft ├── TPS ├── MSPT └── Players
Discord   ├── CPU ├── RAM └── Uptime
```
Alerts (verbatim):
```
CPU > threshold | RAM > threshold | Disk > threshold | Node offline
Service down | Backup failed | Provisioning failed | SSL expiration
Container crashed
```

## Work Items
- [ ] Alert rule engine: threshold rules (per node/account/plan class), state-based (offline, service down, container crashed, backup failed, provisioning failed), time-based (SSL expiration windows); hysteresis + dedup + resolve lifecycle
- [ ] Evaluation on stream events + periodic sweep jobs — NOT in API request path
- [ ] Event sources wired: Phase 3 node status, Phase 10 state-machine FAILED, Phase 11 backup results, SSL expiry from `domains`, container exit codes from 7/8
- [ ] Notification channels: in-app (global notifications, Phase 5/6 kit) at minimum; email + webhook foundation
- [ ] WHM: alert feed (active/ack/resolve), rule config UI, per-node/service drill-downs matching the verbatim tree
- [ ] Panel self-telemetry: OpenTelemetry traces + Prometheus `/metrics` endpoint for the control plane itself + structured logs correlation IDs (from `httpapi` request IDs)

## Deliverables
Alert engine + rules config + WHM observability screens + self-telemetry.

## Definition of Done
Inject node-offline / disk>threshold / failed-backup / container-crash events → deduped alert appears in WHM <5s and clears on recovery; alert evaluation adds zero latency to API p99; panel exports its own metrics.

## Session Handoff — FILL BEFORE ENDING SESSION
- Rule types implemented vs pending: (fill)
- Notification channels live: (fill)
- Update `phases/README.md` status row for Phase 13 → DONE
