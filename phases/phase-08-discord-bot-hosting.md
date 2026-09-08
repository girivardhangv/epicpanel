# PHASE 8 — Discord Bot Hosting (first-class workload)

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 869–949 (Phase 8 spec + Agent Prompt)
> 3. `phases/README.md` + Phase 3 handoff (discord metrics envelope) + Phase 9 handoff (limits) + Phase 7 handoff (container patterns)
> 4. Code: `backend/internal/{runtimes,isolation,agent}`, `backend/cmd/agent`
>
> Depends on: Phases 2–4, 9 · Blocks: 10, 11, 13
> ⚠ Recommended execution order: after `phase-09-resource-limit-engine.md` (and typically after Phase 7 — reuse container machinery).

## Mission
Discord bots as their OWN workload type (verbatim: "Again, treat this separately."). Isolated containerized instances; runtime abstraction; **customers never touch the Docker daemon — all container ops through the Node Agent**.

## Architecture — VERBATIM from master doc
```
Discord Product
       │
       ▼
Bot Instance
       │
       ├── Docker
       ├── Node.js
       ├── Python
       ├── Java
       ├── CPU
       ├── RAM
       ├── Disk
       └── Network
```
Customer should be able to (verbatim): upload bot · deploy · start · stop · restart · console/logs · environment variables · secrets · dependencies · startup command · Python version · Node version · Git deployment · automatic restart · scheduled restart · backups

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> Implement Discord bot hosting as a first-class workload.
>
> Support isolated containerized bot instances.
>
> Create a runtime abstraction supporting at minimum:
> - Node.js
> - Python
>
> Design the runtime system so additional runtimes can be added later.
>
> Implement:
> - bot creation
> - deployment
> - file management
> - start
> - stop
> - restart
> - crash recovery
> - console/log streaming
> - environment variables
> - secret management
> - startup command
> - dependency installation
> - Node.js version selection
> - Python version selection
> - Git deployment foundation
> - automatic restart
> - scheduled tasks
> - backups
>
> Enforce:
> - CPU limits
> - RAM limits
> - disk limits
> - process/PID limits
> - network controls where supported
>
> Metrics must be live and must come from the actual running workload.
>
> Do not allow customers to access the Docker daemon directly.
>
> All container operations must go through the Node Agent.
>
> Implement secure secret handling and never expose secrets in logs or API responses.

## Work Items
- [ ] `internal/discord`: Product/BotInstance entities, state machine, reconciliation against agent truth (same pattern as Phase 7)
- [ ] Runtime abstraction: `BotRuntime` interface — Node.js + Python at minimum (Java listed in architecture diagram; foundation only); version selection per runtime; image/base-layer strategy; adding a runtime = new provider only
- [ ] Deploy paths: file upload (agent fs ops), **Git deployment foundation** (repo URL + pull + rebuild as job), dependency install (`npm ci`/`pip install` in container, logged, timeout-bounded)
- [ ] Lifecycle: start/stop/restart/kill; **crash recovery** with restart policy + restart count (metric); scheduled restart via jobs
- [ ] Console/log streaming over Phase 3 WS (ring buffer + tail follow); controlled command input only — no shell, no docker socket
- [ ] Env vars + **secrets**: encrypted at rest (`secretbox`), injected at container start, **never rendered in logs or API responses** (scrub middleware test)
- [ ] Limits via Phase 9 engine: CPU/RAM/disk/PID, network controls where supported
- [ ] Metrics via Phase 3 discord envelope: CPU, RAM, network, process status, uptime, **restart count** — LIVE/STALE/OFFLINE
- [ ] Backups: bot instance files + env (secrets excluded from downloadable backups or encrypted) — seams for Phase 11
- [ ] UI: customer bot panel (create, deploy, console, env/secrets, files, versions, schedules, metrics) + WHM view

## Deliverables
`internal/discord` + agent bot ops + runtime providers + UI + migrations.

## Definition of Done
Upload→deploy→start→crash (throw unhandled)→auto-restart→logs show both runs works; secret value never appears in any log line or API payload (automated test); no path reaches docker.sock from customer role; metrics match actual container.

## Session Handoff — FILL BEFORE ENDING SESSION
- Runtimes done vs pending (Java?): (fill)
- Git deploy scope actually implemented: (fill)
- Secret scrubbing test location: (fill)
- Update `phases/README.md` status row for Phase 8 → DONE
