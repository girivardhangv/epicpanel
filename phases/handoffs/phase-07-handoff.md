# Phase 7 Handoff — Minecraft Hosting (P7-MC)

Status: **COMPLETE** (backend + agent ops + providers + UI + migration + tests green; coordinator wiring listed below).

## Session Handoff (verbatim items from the phase file)

### Providers implemented vs pending
All six verbatim providers shipped as `MinecraftProvider` implementations in `backend/internal/minecraft/providers.go` — **core contains ZERO provider-specific branches** (registry map + interface only; verified by `TestProviderRegistry`):

| Provider | Versions source (runtime-fetched) | Install layout | TPS over RCON |
|---|---|---|---|
| **Vanilla** | Mojang piston-meta `version_manifest_v2.json` (release filter, newest 40) | `minecraft_server-<v>.jar` direct download | no (honest `n/a`) |
| **Paper** | `api.papermc.io/v2/projects/paper` + per-version builds | latest build jar download | yes (`tps`) |
| **Purpur** | `api.purpurmc.org/v2/purpur` | latest build download | yes (`tps`) |
| **Fabric** | `meta.fabricmc.net/v2/versions/game` (stable only) | loader installer (`java -jar installer server -mcversion … -downloadMinecraft`) | no |
| **Forge** | `promotions_slim.json` (latest→recommended fallback) | installer (`java -jar installer --installServer`), launch via `@libraries/net/minecraftforge/forge/linux_args.txt` | no |
| **NeoForge** | `maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge` (mapped back to MC lines) | installer, launch via `@libraries/net/neoforged/neoforge/linux_args.txt` | no |

- "Potential versions are determined by what's actually supported, not hardcoded forever": version lists are **fetched at runtime**, cached on disk (`EPICPANEL_MC_MANIFEST_DIR`, default `/var/cache/epicpanel/minecraft`), with the honesty chain **live → cache → seed** surfaced per provider in the API (`ProviderOffer.Source`). The seed list is a last-resort bootstrap (flagged `seed`), not a hardcoded catalog. Java major per version is derived from the release line (1.20.5+→21, 1.18–1.20.4→17, older→8); the agent resolves the JVM honestly (`ResolveJavaBin`, glob over `/usr/lib/jvm` layouts, falls back to default `java` with the outcome reporting it).
- Adding a provider = one struct + one registry entry. Nothing else changes (proven: install steps, start command and jar resolution all route through the interface).
- Honest gap: installer-based providers (Forge/NeoForge/Fabric) resolve their produced launcher jar by canonical names (`fabric-server-launch.jar`, single non-installer `*.jar` fallback); exotic launcher layouts may need per-provider refinement later — still provider-package-only work.

### Console/command protocol notes
- **Agent-side ring** (`backend/internal/agent/mc_console.go`): per-instance ring (1000 lines, 4 KiB/line cap) fed from journald (`--since` refill window + overlap dedup, job-retry idempotent via seq cursors). `mc_logs` job returns tail or after-seq deltas.
- **Control-plane ring** (`backend/internal/minecraft/console.go`): fed from job results, fans out over WS (`mc_console` frames: `tail` seed + per-`line` pushes). **The WS discards all inbound input** — commands never ride the socket.
- **Commands**: REST `POST …/console/command` → `minecraft.ValidateConsoleCommand` (control plane) → `mc_command` job → **re-validated agent-side** → RCON to `127.0.0.1:<rcon_port>` → response lines appended to both rings. **NEVER shell, never stdin, never docker.** The allowlist (`ConsoleAllowlist`, 17 commands: list/say/whitelist/kick/ban/pardon/op/deop/save-all/save-on/save-off/tps/difficulty/weather/time/gamemode/stop) is shape-guarded: control chars refused in raw input, args reject shell metacharacters (`; | & $ \`` etc.), arg-count bounds per command, 512-char cap. Adding a command = one `CommandSpec` entry.
- **RCON client**: stdlib-only (`backend/internal/minecraft/rcon.go`, ~150 lines) — framed codec separated from the connection (round-trip + live TCP fake-server tests). Password: generated panel-side (32-char), secretbox-sealed at rest (`rcon_pass_enc`), written by the agent into `server.properties` (0600-equivalent 0640, `enable-rcon=true`, panel-allocated `rcon.port`), scrubbed from every log/result at agent AND control-plane edges.
- **Metrics**: agent per-instance RCON poller (15 s, started on install/start, stopped on stop/kill/delete) caches players (`list`), TPS (`tps`), MSPT (`paper mspt`) with **honesty flags** — unsupported/unparseable values stay unknown, never guessed. `mc_metrics` job returns the on-demand snapshot with `tps_known`/`mspt_known` + a human detail string. The `AppSample` envelope already carries `players`/`tps`/`mspt` (Phase 3 protocol); `tps_source` on the API metrics view states provenance (`rcon` | `waiting` | `unsupported`).

### Reconciliation edge cases found
- **API-accepted ≠ success** everywhere: create enqueues `mc_install` (row stays `installing` until the install job result); start moves `starting` + `desired_state=running` and ONLY agent truth promotes to `running` — via LIVE metrics envelope first, finished-job outcomes second (`applyMCJobOutcomes`), matching the Phase 8 truth hierarchy. STALE samples are not truth.
- **Crash detection**: LIVE sample `failed|inactive` while desired `running` + row `running` → `crashed` transition + restart policy. Episode counters (`episode_restarts` reset when the healthy run exceeds `EpisodeWindow` 15 min; `restart_count` lifetime) drive the budget; policy `no` never auto-restarts; recovery jobs are idempotency-keyed `mccrash-<id>-<episode>`.
- **Converge guards**: unit active while desired `stopped` → re-enqueue stop (`mcstop-` key); unit inactive while desired `stopped` confirms `stopped` (idempotent double-stop allowed at the API, no 409). `mc_status` truth-refresh jobs fire when rows claim running/starting but no fresh sample exists (e.g. stream down).
- **State machine**: verbatim edges only (`transitions` map); reconciliation is allowed to apply agent truth that would be illegal as a customer action (crashed→starting) — same pattern as Phase 8. Illegal edges surface as `TransitionError` and are audited.
- **Safe shutdown** (`mcSafeShutdown`): `save-all` → RCON `stop` → bounded wait (`MCStopGrace` 30 s) → systemd stop (SIGTERM) → 3 s → SIGKILL. Kill is the explicit customer override. Every escalation step recorded in the outcome log. Restart = safe shutdown + start (fresh properties merge).
- **Port safety**: game + RCON ports allocated from distinct ranges (25565-25665 / 25765-25865) per server with DB-level UNIQUE constraints as the backstop; one externally-listening port per instance counted against the plan's `ResPorts` (RCON is loopback-only).
- **Restore seam**: `mc_restore_world` stops first (safe shutdown), moves worlds to a scratch dir, extracts, and **rolls back on extract failure** (never half-swapped); restore leaves the instance stopped — the customer (or desired-state reconciliation) restarts.
- **Protected properties**: `server-port`, `server-ip`, `enable-rcon`, `rcon.*` are panel-managed at BOTH ends (API 422 + agent re-check); uploads cannot overwrite `server.properties`/`eula.txt`.

### Update phases/README.md status row
Not edited (wave contract forbids edits during the parallel wave). Coordinator should set: `| 7 | Minecraft Hosting (run 8th) | phase-07-minecraft-hosting.md | DONE | (summary below) |`

## Coordinator wiring needed (out of my ownership)
1. `backend/internal/api/server.go`: add `registerPhase7(s, mux)` (one call line; self-contained — loops start inside it, disable knob `EPICPANEL_DISABLE_MC_LOOPS=1`).
2. `backend/internal/agent/worker.go`: merge `agent.MinecraftOps` (16 ops, same signature as `DiscordOps`) into the job dispatch.
3. `frontend/apps/customer/App.tsx`: mount `routes.minecraft.tsx` (`/minecraft`, `/minecraft/:instance_id`) + a nav entry (pages are self-contained).
4. Optional one-liner for live TPS/MSPT/players in the Phase 3 stream: in `workloadCollector.CollectApps` (backend/internal/agent/workload.go — READ-ONLY for me), for `kind == "minecraft"` fill `AppSample.Players/TPS/MSPT` from `MCMetricsSnapshot(id)` (exported by `mc_console.go`; the poller keeps it warm). Without it, the REST `mc_metrics` job path still delivers everything with honesty flags; the envelope fields stay 0.
5. Migration `0028_minecraft.sql` is idempotent-safe (re-runnable enum guard, IF NOT EXISTS) and follows the 0029 pattern.

## What shipped (files)

Backend control plane (`backend/internal/minecraft/`):
- `service.go` — entities, verbatim state machine + `CanTransition`/`LifecycleGuard`/`AgentStatus`, wire payloads/outcomes (RCON password ALWAYS ciphertext), job-type constants (`mc_install`…`mc_delete`), property validation (`ProtectedPropertyKeys`, newline-safe), `XmxForPlan` (plan RAM × 3/4, floor 512), event types (`minecraft.*`).
- `store.go` — `minecraft_instances`/`minecraft_schedules`/`minecraft_world_backups` stores, org-scoped (cross-tenant = NotFound), `ReconcileAgentTruth` with crash-episode counters, port allocators over the ranges (`AllocPort`/`AllocRCONPort`).
- `providers.go` — `MinecraftProvider` interface + six providers (manifests runtime-fetched, disk cache, honesty `live|cache|seed`), `javaFor`, `javaCommand` arg assembly, `ProviderOffers`/`ValidateProviderVersion`.
- `commands.go` — console allowlist + `ValidateConsoleCommand` (both-end enforcement), cron facade.
- `cron.go` — 5-field cron parser + bounded next-fire scan (same semantics as the Phase 8 scheduler).
- `console.go` — control-plane ring + WS hub (`ServeConsoleWS`; reader pump discards inbound), janitor.
- `secrets.go` — RCON password generation/sealing, `SecretValues`, `ScrubText` (exact/URL-escaped/base64 → `[redacted]`).
- `rcon.go` — Source RCON codec + client + metrics parsers (`RCONPlayers`/`RCONTPS`/`RCONMSPT`, honesty on unparseable).

Agent (`backend/internal/agent/`):
- `minecraft_ops.go` — `MinecraftOps` registry (16 ops). **ContainerDriver interface + SystemdDriver** (transient unit `epicpanel-app-<id>` under a dedicated `ep-mc-*` unix user: NoNewPrivileges, ProtectSystem=strict, ProtectHome, PrivateTmp, PrivateDevices, RestrictSUIDSGID, ReadWritePaths=tree only, UMask=0027, cgroup caps CPUQuota/MemoryMax+MemoryHigh(90%)/TasksMax from the Phase 9 engine numbers, `Restart=<policy>`; a Docker driver slots into the interface — see deviations). Provider-driven install steps (download / loader / installer), `server.properties` + `eula.txt` writers, `resolveServerJar`, `ResolveJavaBin`, safe-shutdown, world backup/restore seams, file list/upload (path-traversal + symlink-escape proof, panel-managed files protected), properties get/set (protected keys rejected twice), metrics job.
- `mc_console.go` — agent ring (journald-fed, scrub-on-ingest), spec registry (rcon inputs), safe-shutdown sequencing, metrics poller + `MCMetricsSnapshot` export, poller lifecycle.

API (`backend/internal/api/`):
- `phase7_minecraft.go` — `registerPhase7(s, mux)`: CRUD (create gates: plan `kind=minecraft` fail-closed, `ResPorts` count via the unified engine, EULA required, version validated against provider offer, port allocation, heap derived from plan RAM), lifecycle (start/stop/restart/kill with guarded transitions), console REST snapshot (bounded job await + edge re-scrub) + WS + allowlisted command, files list/upload, properties GET (job) / PUT (merge + queue write), metrics (LiveStore freshness + `tps_source` honesty), per-instance jobs, provider offers, schedules (restart/start/stop/allowlisted-command), world backups (queue/list/restore), reconciliation loop (30 s, LIVE-first truth, crash recovery, desired-state convergence, truth refresh) + schedule sweeper + backup-result recorder. Disable knob: `EPICPANEL_DISABLE_MC_LOOPS=1`.
- `phase7_minecraft_test.go` — API integration suite (see test evidence).

Migration: `backend/migrations/0028_minecraft.sql` — `mc_status` enum, `minecraft_instances` (UNIQUE per-server ports, EULA-less schema has no secret columns beyond `rcon_pass_enc` ciphertext), `minecraft_schedules` (command kind requires non-empty validated command), `minecraft_world_backups`, 15 `job_type` values, `jobs.minecraft_id` + index.

Frontend (`frontend/apps/customer/pages/minecraft/` + `routes.minecraft.tsx`):
- `Minecraft.tsx` — instance list (players/TPS/CPU/RAM columns + FreshnessBadge), create modal (server type + version offers with honesty source, MOTD, EULA checkbox gating submit).
- `MinecraftDetail.tsx` — status cards (Players/TPS w/ provenance note/CPU/RAM/disk-uptime + FreshnessBadge), WS console WITH allowlisted command input, files browser + upload, server.properties editor (protected keys refused), versions (live-manifest offers), schedules (incl. allowlisted command kind), world backups (create/restore with stop warning), metrics provenance panel.
- `routes.minecraft.tsx` — RouteObject fragment for the coordinator.

## Deviations from the letter of the phase doc (with reasons)
1. **No Docker (environment fact)**: isolation = systemd transient units + cgroups v2 via the **ContainerDriver** interface (SystemdDriver shipped, mirroring app_ops/enforce.go). The Docker driver slots in behind `Start/Stop/Kill/Status/Enforce` without touching ops. Noted as required by the wave contract.
2. **WHM admin screen**: out of my ownership row (P6-ADMIN owns `frontend/apps/admin/**`). The customer panel covers the full control surface; admin fleet views arrive with P6 integration.
3. **JMX**: agent collects TPS/MSPT/players via **RCON** (per the wave brief) — no JMX agent dependency; `tps`/`paper mspt` availability is per-provider and flagged honestly.
4. **`build_app`-style JAR upload**: file upload (64 MiB/file) + provider download cover the JAR-into-place flows; plugin/mod management is the directory + upload + plugin-dir seam (`PluginDir()` per provider) — a marketplace is out of scope.
5. **Backups**: world-scoped agent ops + `minecraft_world_backups` seam rows (sha256 recorded); the Phase 11 engine (sinks/retention/encryption) owns the full integration — restore-first for MC is already wired through the job.

## Test evidence
- Unit (`backend/internal/minecraft/minecraft_test.go`, no DB): verbatim state-machine edges (legal + illegal + crash paths), lifecycle guard, agent-truth mapping, **RCON codec round-trip + oversized-frame rejection + live fake-server auth/command test**, players/TPS/MSPT parsers (incl. honesty on garbage), console allowlist (allowlist membership, arg-count bounds, shell-metachar refusal, newline-injection refusal, empty), protected properties + newline guard, Xmx derivation, name validation, provider registry (all six) + java rule + **arg assembly contract**, RCON password sealing + scrub, cron parsing, port allocation/exhaustion.
- Agent package: builds + vets clean alongside existing suites (`go test ./internal/agent/` ok).
- API integration (`phase7_minecraft_test.go`, DB `epicpanel_test_mc`): create + plan gate (ports count = 1 on Minecraft 4GB; web-plan org refused 403; EULA 422; bad version 422; plan-derived heap 3072), ciphertext-only install payload + at-rest column + no view leaks, org scoping (cross-tenant 404 on GET/PATCH/DELETE/start; non-member 404 no-leak), role enforcement (billing read / no stop / no command), console allowlist (normalized command queued; shell-like refused with **zero** extra jobs enqueued; command schedules validate the allowlist at create), lifecycle + reconciliation vs injected LIVE `kind=minecraft` samples (starting→running→crash→recovery with counters 1/1 + idempotency-keyed recovery job→running→stop→stopped→idempotent double-stop→policy `no` crash stays crashed→kill job), metrics view (OFFLINE→LIVE, players present, `tps_source:"rcon"` honesty), backup seam (job queued; agent outcome recorded into `minecraft_world_backups` via the loop), properties (protected key 422; write OK), provider offers (all six + source flag), console ring scrub (raw agent line redacted at the control-plane edge).
- Full run: `cd backend && go build ./... && go vet ./...` clean; `EPICPANEL_TEST_DATABASE_URL=…epicpanel_test_mc go test ./... -p 1` — **all packages ok** (agent, agentproto, api, discord, minecraft, resources, resourcelimits, websites, …), 0 FAIL.
- Frontend: `npx tsc --noEmit` — zero errors in my files (remaining pre-existing errors are P6's apps/admin); `npx vite build --config apps/customer/vite.config.ts` green (382 kB).
