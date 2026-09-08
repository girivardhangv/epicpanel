# PHASE 9 — Resource & Limit Engine (unified) ⚑ run BEFORE Phases 7–8

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 950–1001 (Phase 9 spec — no separate agent prompt; spec IS the requirement) + lines 1296–1300 (enforcement/display same-source rule)
> 3. `phases/README.md` + Phase 3 handoff (metrics source) + Phase 4 handoff (limit seams)
> 4. Code: `backend/internal/{packages,isolation,servers/agent.go}`, agent cgroup/quota capabilities
>
> Depends on: Phases 3–4 · Consumed by: 5, 6, 7, 8, 10, 13
> ⚠ Doc "Development Order" places this phase at position 7 — BEFORE Minecraft and Discord — so both workloads consume it instead of inventing their own limits.

## Mission
One unified resource system. Verbatim: **"The agent must enforce limits, not merely display them."** And (doc closing note): "displayed metrics and enforced limits must come from the same authoritative node-side measurement/control layer. Otherwise you can end up with a customer apparently under their 2 GB limit while the actual workload is consuming more."

## Model — VERBATIM from master doc
```
Resource
├── CPU
├── RAM
├── Disk
├── Bandwidth
├── Processes
├── Databases
├── Domains
├── Ports
└── Backups
```
Plans (verbatim): Starter · Pro · Business · Minecraft 2GB · Minecraft 4GB · Discord Basic · Discord Pro

Example (verbatim):
```
Minecraft 4GB
RAM:       4096 MB
CPU:       200%
Disk:      20 GB
Bandwidth: 2 TB
Ports:     1
Backups:   3
```
For web hosting (verbatim): CPU · RAM · Disk · I/O · Processes · PHP workers · Databases · Domains · Email · Bandwidth

## Work Items
- [ ] `internal/resources`: unified Resource type (name, quantity, unit, usage) + Plan→limits mapping (data-driven, covers web + minecraft + discord resource sets above); single API: `GetLimits(workload)` / `GetUsage(workload)` / `Enforce(workload)`
- [ ] Agent enforcement (the hard part — all node-side, same layer as Phase 3 collection):
  - CPU/RAM/PID: cgroups v2 (systemd slices or container limits)
  - Disk: quotas (xfs/ext4 project quota or container storage opt)
  - Bandwidth: iptables/nftables + tc counters per account/container; RX+TX accounting
  - I/O: blkio/weight controls where supported
  - PHP workers: pool `pm.max_children` bound to plan
  - Counts (Databases/Domains/Ports/Backups/Email): control-plane validation at create-time + agent-side reconciliation guard
- [ ] Enforcement ≠ display drift: usage numbers UI shows come from the SAME agent collector that feeds enforcement; add drift test
- [ ] Over-limit actions: throttle/kill/suspend hooks → jobs (Phase 10 consumes suspend hook); never silently corrupt workload state
- [ ] Migrate existing seams: `packages` limits (Phase 4), Minecraft/Discord limit calls (Phases 7–8) onto this engine — remove any duplicated limit logic
- [ ] WHM: plan editor with resource matrix; per-account usage vs limit bars (live)

## Deliverables
`internal/resources` engine + agent enforcement ops + plan seed data (verbatim plans above) + usage accounting jobs + drift tests.

## Definition of Done
Under synthetic load a 2GB-plan workload cannot exceed 2GB RAM or its bandwidth quota (measured independently of the panel); UI usage == enforcement source; adding a new plan changes only data, not code.

## Session Handoff — FILL BEFORE ENDING SESSION
- Enforcement mechanisms chosen per resource (cgroups version, quota fs, tc/nft):
  - **CPU / RAM / Processes**: cgroups **v2** (unified hierarchy, `/sys/fs/cgroup`), per-site
    slices under `epicpanel.slice/epicpanel-<unixuser>.slice` — `cpu.max` (percent-of-core →
    quota/100ms period), `memory.max` (exact plan RAM, floored 128 MiB; `memory.high` at 90%),
    `pids.max`. Process capture via `ps -u <user>` → `cgroup.procs` + the existing
    `epicpanel-cgroup-sync.timer` (minute re-capture of respawned FPM workers). Pre-existing
    Phase-4 `ApplyUserLimits` now delegates to the new `ApplyUserLimitsV2` core (which adds
    pids/io + honest unlimited handling: 0 → kernel "max").
  - **Disk**: detect-then-apply FS quotas — XFS **project quotas** via `xfs_quota`
    (prjquota mount flag; FNV-1a site-id → project id 1000+, `bhard` = plan disk MB), ext4
    **user quotas** via `setquota` (usrquota flag, uid-scoped). When no quota flags/tooling:
    reported `accounted` with the reason (usage walk always runs) — never fake-enforced.
  - **Bandwidth**: **nftables** per-account counters (`table inet epicpanel_limits`, chain
    `acct_<user>` at forward priority -300, RX+TX byte counters); outcome usage echo feeds
    `workload_resource_usage` (monthly period, GREATEST high-water upsert so nft reloads can
    never undercount). No tc: one mechanism, honestly accounted-only when nft is absent.
  - **I/O**: cgroup v2 `io.weight` (clamped 1..10000) where the controller is delegated.
  - **PHP workers**: FPM pool `pm.max_children` bound to plan via `resources.PoolLimitsFor`
    (children = RAM/32MB clamped 1..100; per-request `php_admin_value[memory_limit]` ceiling
    clamped 256M) — the Phase-4 `internal/limits` stub is now a thin shim over the engine;
    `patchPoolChildren` patches pm sizing atomically with fpm validate + restore-on-fail.
  - **Counts** (Databases/Domains/Ports/Backups/Email): control-plane create-time gates over
    the engine (`resourcelimits.CountGate`; backups gate wired into POST backups, databases
    gate migrated from legacy dbGate to engine with legacy error text preserved, fail-closed
    on lookup errors) + agent-side reconciliation guard in `enforce_limits` (reports missing
    site trees / carries plan caps; agent never mutates).
- Resources enforced vs validated-only (honest list):
  - **Kernel-enforced**: CPU (cpu.max), RAM (memory.max+high), Processes (pids.max),
    I/O weight (io.weight) — when cgroup v2 is delegated; PHP workers (FPM pm.max_children)
    — process-level, not kernel.
  - **Enforced when node supports it, honestly accounted otherwise**: Disk (xfs project /
    ext4 user quota → `accounted`+reason without flags/tooling).
  - **Accounted-only (validated by policy)**: Bandwidth (nft counters; over-budget handled by
    control-plane suspend action, kernel shaping available as `throttle` action).
  - **Validated-only (create-time)**: Databases, Domains, Ports, Backups, Email counts.
  - Every mechanism outcome carries mode (`enforced`/`accounted`/`validated-only`/`skipped`)
    + mechanism + detail reason in the enforce_limits result — detect-then-apply, no silent
    pretending (proven by TestEnforceDegradesHonestlyWithoutCgroups etc.).
  - **Anti-drift guarantee**: ONE shared reader `readSliceLimits` for display (workload
    collector → LiveStore → WS, site_usage echo) AND enforcement verification — display
    cannot read different numbers because it cannot read different files
    (TestDriftDisplaySourceEqualsEnforcementSource; api-level
    TestPhase9EnforcePayloadMatchesDisplay proves payload == displayed caps).
- Over-limit policy defaults (resources.DefaultPolicy):
  - cpu/ram/io/processes → `none` (kernel already throttles/OOM-kills/pids-caps inside the
    slice — no duplicate control-plane action);
  - disk → `suspend` (no kernel backstop when quota unsupported);
  - bandwidth → `suspend` at the monthly RX+TX budget;
  - counts → never breach at runtime (create-time validated).
  Breaches flow: agent enforce outcome → control-plane fanout → `limits.breach` event (WS) +
  audit + **idempotent `suspend_website` job** (the Phase 10 hook; only `ready` sites are
  ever suspended — never silently corrupt state). `throttle` action re-asserts enforcement.
- Migration: **0027_resource_plans.sql** (only 0027 created) — hosting_packages +9 matrix
  columns (kind, max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports,
  max_backups, max_email_accounts), the **7 verbatim plans** seeded
  (Starter · Pro · Business · Minecraft 2GB · Minecraft 4GB · Discord Basic · Discord Pro;
  Minecraft 4GB verbatim: RAM 4096MB, CPU 200%, Disk 20GB, Bandwidth 2TB, Ports 1,
  Backups 3), `workload_resource_usage` (per-account period accounting), `enforce_limits`
  job type. Legacy rows (all-new-columns-at-default) are `LegacyRow`: their governed counts
  are exactly the legacy columns, so pre-Phase-9 packages keep their behavior until edited.
- Seams migrated (no duplicated limit logic): `internal/limits` = shim over
  `resources.PoolLimitsFor`; `dbGate` + backup gate resolve via the engine;
  `siteLimitsFor` (usage endpoint) reads engine limits — displayed memory cap is now the
  plan RAM itself (the old 4× FPM headroom is gone; pool sizing bounds the aggregate).
  Plan assignment (POST /admin/organizations/{id}/package) converges sites via
  `enforce_limits` (idempotent key `enforce_limits_<site>`). Existing endpoint response
  shapes unchanged (frontend contract preserved).
- Verification: `go build ./...` + `go vet ./...` clean; `go test ./... -p 1` with
  EPICPANEL_TEST_DATABASE_URL = **14 packages ok, 0 FAIL** (integration suite incl. new
  Phase 9 tests: plan seed matrix, enforce payload == display, backup count gate 3/3+403).
  Unit tests cover engine mapping/clamping/counts, migration mirror, drift (engine + agent
  layer), detect-degrade (cgroup/fs-quota/nft), FPM pool patch, breach→suspend, guard.
  Linux-kernel-specific paths exercised with injected cgroup roots/mounts (CI has no
  cgroup v2 delegation); real-node behavior is the same code path with the live root.
- Handed to Phases 7/8: minecraft/discord plan rows exist (`kind` column) — consume
  `Engine.GetLimits/EnforcePlan` with `WorkloadKind minecraft|discord`; app units inherit
  slice limits via the site user. Handed to Phase 10: suspend hook (over-limit), plan rows
  with prices, `limits.breach` events. Handed to Phase 5/6 (UI): `Limits.GetUsage(workload,
  Usage)` + `Resource.PercentUsed()` for usage-vs-limit bars; plan matrix is pure data
  (WHM plan editor = hosting_packages row edit, no code change — TestAddPlanIsDataOnly).
- Update `phases/README.md` status row for Phase 9 → DONE (orchestrator: see summary above)

