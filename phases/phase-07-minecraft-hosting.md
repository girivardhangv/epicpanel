# PHASE 7 — Minecraft Hosting (first-class workload)

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 746–868 (Phase 7 spec + Agent Prompt)
> 3. `phases/README.md` + Phase 3 handoff (metrics protocol minecraft envelope) + Phase 9 handoff if resource engine ran first (recommended order puts Phase 9 BEFORE this phase)
> 4. Code: `backend/internal/{agent,servers,jobs,isolation}`, `backend/cmd/agent` — container ops patterns
>
> Depends on: Phases 2–4, 9 (limits), 3 (metrics protocol) · Blocks: 10, 11, 13
> ⚠ Recommended execution order (doc "Development Order"): run `phase-09-resource-limit-engine.md` BEFORE this file.

## Mission
Minecraft as its OWN workload type — verbatim: **"Minecraft should be treated as a different workload type, not as a web-hosting account with extra fields."** Isolated containers; **the Node Agent is authoritative for runtime state**.

## Architecture — VERBATIM from master doc
```
Minecraft Product
       │
       ▼
Minecraft Instance
       │
       ├── Container
       ├── Java runtime
       ├── Server JAR
       ├── World
       ├── Config
       ├── Ports
       ├── CPU limit
       ├── RAM limit
       ├── Disk limit
       └── Network limits
```
Support eventually (verbatim): Vanilla · Paper · Purpur · Fabric · Forge · NeoForge
"Potential versions should be determined by what's actually supported/tested, not hardcoded forever."

Controls (verbatim list): Start · Stop · Restart · Kill · Console · Files · Backups · Restore · Properties · Versions · Mods · Plugins · Players · World · Schedules · Startup command · Java version · Metrics
Especially (metrics): TPS · MSPT · Players · CPU · RAM · Disk · Network

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> Implement Minecraft hosting as a first-class workload type.
>
> Do NOT model Minecraft as ordinary web hosting.
>
> Create a Minecraft service abstraction.
>
> Use isolated containers/workloads for Minecraft instances.
>
> Implement:
> - instance creation
> - start
> - stop
> - restart
> - kill
> - console
> - command execution through a controlled console interface
> - server versions
> - Java runtime selection
> - startup configuration
> - memory limits
> - CPU limits
> - disk limits
> - network limits
> - port allocation
> - filesystem management
> - backups
> - restore
> - scheduled tasks
> - player information
> - plugins/mods management foundation
>
> Support a provider architecture for server types such as:
> - Vanilla
> - Paper
> - Purpur
> - Fabric
> - Forge/NeoForge
>
> Do not hardcode provider-specific logic into the core service.
>
> Implement container-level resource enforcement.
>
> Collect live metrics including:
> - CPU
> - memory
> - network
> - disk
> - uptime
> - process status
> - TPS
> - MSPT
> - player count
>
> The Node Agent must be authoritative for runtime state.
>
> The control plane should reflect actual node state rather than assuming an operation succeeded merely because an API request was accepted.
>
> Handle crashes and unexpected process termination.
>
> Implement safe startup/shutdown behavior and automatic state reconciliation.

## Work Items
- [ ] `internal/minecraft`: Product/Instance entities, service abstraction, state machine (installing→stopped→starting→running→stopping→crashed), reconciliation loop against agent-reported truth
- [ ] Agent side: container lifecycle (Docker), JAR/version discovery (fetched from provider APIs at runtime, cached — not hardcoded), world/file ops, port allocation (via `internal/ports`), console I/O stream (ring buffer + WS), controlled command channel (whitelist: console commands only, never shell)
- [ ] Provider interface: `MinecraftProvider` (Vanilla/Paper/Purpur/Fabric/Forge/NeoForge) — version listing, download, install layout, startup command defaults; core service contains zero provider-specific branches
- [ ] TPS/MSPT/players collection: agent-side (JMX or RCON `list`/metrics mod foundation); flows through Phase 3 minecraft envelope with LIVE/STALE/OFFLINE
- [ ] Limits: consume Phase 9 engine (CPU/RAM/disk/net/PID) — container-level enforcement, same source as displayed metrics
- [ ] Backups/restore: world-scoped ops (full engine Phase 11 — implement agent ops + seams now)
- [ ] Schedules (restart/command) via jobs; crash detection + restart policy; safe shutdown (save-all → kick → stop with timeout → kill)
- [ ] UI: customer Minecraft panel (console view, files, properties editor, versions, schedules, metrics cards) + WHM instance management

## Deliverables
`internal/minecraft` + agent minecraft ops + provider plugins + customer/admin screens + migrations.

## Definition of Done
Create→start→console command→crash (kill -9 java)→detected→restart→restore backup passes end-to-end; UI state always matches agent truth (API-accepted ≠ success); TPS/MSPT/players live with freshness badges; adding a new provider touches only provider package.

## Session Handoff — FILL BEFORE ENDING SESSION
- Providers implemented vs pending: (fill)
- Console/command protocol notes: (fill)
- Reconciliation edge cases found: (fill)
- Update `phases/README.md` status row for Phase 7 → DONE
