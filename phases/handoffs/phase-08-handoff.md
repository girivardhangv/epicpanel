# Phase 8 Handoff — Discord Bot Hosting (P8-DC)

Status: **COMPLETE** (backend + agent ops + UI + migration + tests green; coordinator wiring listed below).

## Session Handoff (verbatim items from the phase file)

### Runtimes done vs pending (Java?)
- **Node.js** — DONE. `discord.NodeRuntime` (backend/internal/discord/runtime.go): versions 22/20/18 with default 22; deps via `npm ci` (lockfile) or `npm install`; startup `node <file>`; version validated at create/update.
- **Python** — DONE. `discord.PythonRuntime`: versions 3.12/3.11/3.10 with default 3.12; venv creation + `pip install -r requirements.txt`; startup `python <file>` (venv interpreter preferred when present).
- **Java** — FOUNDATION STUB (as specified: "Java listed in architecture diagram; foundation only"). `discord.JavaRuntime` implements the full `BotRuntime` interface, appears in `GET .../bots/runtime-offers` flagged "coming soon", and every schedule path returns a clear 422 ("java runtime is not yet available for bots (foundation only)"). Adding Java later = filling in that one provider; core code has zero runtime-specific branches (proven by `TestRuntimeProviders`).
- The abstraction is `BotRuntime` (interface) + a `runtimes` registry map — adding a runtime is a new provider + one registry entry, nothing else.

### Git deploy scope actually implemented
- Job `bot_deploy_git` (agent op `HandleBotDeployGit` in backend/internal/agent/discord_ops.go):
  - clone (fresh) or fetch/checkout/reset (existing `.git`), branch-aware (default `main`);
  - **timeout-bounded** (`DefaultBuildTimeout` 10 min, payload-overridable, hard-capped 15 min);
  - dependency install runs as part of install/deploy flows (`botDepsInstall`: npm ci / venv+pip), logged (scrubbed), never a shell from payloads;
  - access tokens: AES-GCM sealed at rest (`git_token_enc`), decrypted transiently agent-side and injected ONLY through the child-process env (`GIT_CONFIG url.<cred>…insteadOf=<clean>` rewrite) — never argv, never logs (`gitCredEnv`, `sanitizeRepoURL`, `TestGitCredEnv`, `TestSanitizeRepoURL`);
  - UI: create-with-repo (BotsList modal) + redeploy (BotDetail → Settings → "Deploy from git").
- Not implemented (honest gaps): no commit-status webhooks, no per-commit build matrix, no monorepo subpath selection. Foundation = clone/pull/build/redeploy.

### Secret scrubbing test location (mandatory test)
- **Unit/agent level**: `backend/internal/discord/discord_test.go` → `TestSecretScrub` (raw + URL-escaped + base64 leak forms; short-value false-positive guard); `TestEnvEncryptionRoundTrip`, `TestGitTokenSealing`, `TestMaskEnv`.
- **Agent level**: `backend/internal/agent/discord_ops_test.go` → `TestScrubLogNoPlaintextInOutcome` (job result log), `TestBotConsoleRingScrubAndDedup` (console ring scrub-on-ingest + retry dedup), `TestDecryptEnvEnc`.
- **API integration level**: `backend/internal/api/phase8_discord_test.go` → `TestBotEnvWriteOnly` (GET env returns keys only; PUT masked; start-job payload carries `env_enc` ciphertext only — plaintext asserted absent from job payload AND from the at-rest column), `TestBotCRUDAndPlanGate` (create response + install-job payload scanned for the secret), `TestBotConsoleTailScrub` (control-plane ring redacts even raw agent lines — defense in depth).
- Mechanism: every secret env value + git token flows through `discord.ScrubText` (exact / URL-escaped / base64 forms → `[redacted]`) at THREE layers: agent ring ingest, agent job-result logs, control-plane API edge.

### phases/README.md status row
Not edited (wave contract forbids edits during the parallel wave). Coordinator should set: `| 8 | Discord Bot Hosting (run 9th) | phase-08-discord-bot-hosting.md | DONE | Bots as first-class workload: ... (summary below) |`

## What shipped (files)

Backend control plane (`backend/internal/discord/`):
- `store.go` — `bot_instances`/`bot_schedules` stores, `BotRow` (ciphertext-safe internals), org-scoped queries (cross-tenant = NotFound), status transitions, `ReconcileAgentTruth` with crash-episode counters, env-blob swap (`SetEnvEnc`).
- `lifecycle.go` (in store.go) — verbatim state machine installing→stopped→starting→running→stopping→crashed (+failed/deleting/deleted), single guarded edge map + `TransitionError` for illegal moves.
- `runtime.go` — `BotRuntime` interface; Node/Python providers; Java stub; `VersionOffers`.
- `service.go` — wire payloads/outcomes (env ALWAYS ciphertext), job-type constants (`bot_install`…`bot_delete`), `ValidateCreate`/`SanitizeEnv`, `LifecycleGuard`.
- `secrets.go` — secretbox AES-GCM env/git-token sealing, key/value validation (newline-injection safe), `SecretValues`, `ScrubText`, `MaskEnv`.
- `cron.go` — 5-field cron parser + next-fire scan (bounded 1 year, DST-safe UTC), `CreateBot` flow.
- `console.go` — control-plane console ring + WS hub (`ServeConsoleWS`; reader pump DISCARDS all inbound input — no shell, no docker, no command channel).
- `backup.go` — Phase 11 seam: `BackupManifestFor` (bot tree included; `bot.env` excluded — regenerated secrets; venv/node_modules excluded; env rides as ciphertext blob only).

Agent (`backend/internal/agent/`):
- `discord_ops.go` — `HandleBot*` ops + `DiscordOps` registry (install, deploy_git, upload, files, start, stop, restart, kill, status, logs, delete). Start builds a **sandboxed systemd transient unit** `epicpanel-app-<botID>` under a dedicated `ep-bot-*` unix user: NoNewPrivileges, ProtectSystem=strict, ProtectHome, PrivateTmp, PrivateDevices, RestrictSUIDSGID, ReadWritePaths=bot tree only, UMask=0027, plus cgroup caps CPUQuota/MemoryMax+MemoryHigh(90%)/TasksMax from the Phase 9 engine numbers, `Restart=<policy>` + RestartSec, env via **0600 EnvironmentFile** (decrypted once at start, never logged), optional `IPAddressAllow/Deny` network policy when requested.
- `bot_console.go` — agent-side per-bot ring buffer fed from journald (since-window refill + overlap dedup), scrub-on-ingest, `bot_logs` service, spec registry for scrub inputs.
- `discord_ops_test.go` — unit naming/user derivation, restart-policy hardening, path-traversal/symlink-escape rejection, encrypted env plumbing, scrub proofs, git credential env, outcome wire shapes.

API (`backend/internal/api/`):
- `phase8_discord.go` — `registerPhase8(s, mux)`: CRUD, lifecycle (start/stop/restart/kill), env (write-only/masked), deploy-git, file upload/list, logs + console REST snapshot (bounded job await, re-scrubbed at the edge) + console WS, metrics (LiveStore w/ freshness, kind=discord), per-bot job list, runtime offers, schedules CRUD (restart/start/stop only — arbitrary command scheduling refused). Phase 9 create-time gate: plan must be `kind=discord` and `max_websites` (the Discord-plan count column) caps bot count; fail-closed when the plan can't be resolved.
- `phase8_discord_loops.go` — control-plane loops (30s): reconciliation vs agent truth (LIVE metrics envelope first, finished bot jobs second), crash recovery honoring restart policy + episode budget (idempotency-keyed `botcrash-*` jobs), status truth refresh (`bot_status` when no live data), scheduled-restart sweeper (`botsched-*` keys). Disable knob for deterministic tests: `EPICPANEL_DISABLE_BOT_LOOPS=1`.

Migration: `backend/migrations/0029_discord_bots.sql` — `bot_instances`, `bot_schedules`, `jobs.bot_id` (+index), 11 new `job_type` values, re-runnable (`DO $$ … duplicate_object` for the enum, `IF NOT EXISTS` everywhere).

Frontend (`frontend/apps/customer/`):
- `routes.bots.tsx` — `export const routes: RouteObject[]` (coordinator mounts).
- `pages/bots/BotsList.tsx` — fleet table (status, live CPU/mem, restarts, FreshnessBadge), create modal (runtime/version offer picker, git bootstrap), delete confirm.
- `pages/bots/BotDetail.tsx` — status/metrics cards fed from the WS stream (`useMetrics` app envelope keyed by bot id; REST snapshot fallback — no polling), lifecycle controls (Start/Restart/Stop/Kill), Console (REST snapshot + read-only WS tail; input intentionally unsupported), Env editor (keys visible, values write-only password fields), Files (upload + list), Schedules, Settings (startup file/command, version, git deploy).
- WHM view: the bot endpoints are org-scoped and platform admins pass `ResolveOrg` for every org — P6-ADMIN can build the admin bot console directly on these routes (frontend/apps/admin is P6-owned).

## Coordinator wiring (one-time, between waves)
1. `backend/internal/api/server.go` — add inside `Handler()`: `registerPhase8(s, mux)` (after appH.Register). This file is coordinator-owned; I did NOT touch it.
2. `backend/internal/agent/worker.go` — merge dispatch (file coordinator-owned; worker.go untouched). Add:
   ```go
   for jt, fn := range agent.DiscordOps {
       // jt is e.g. "bot_start"; fn: func(ctx, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error)
       agent.RegisterBotOp(jt, fn) // or inline the map into the switch's dispatch table
   }
   ```
   The registry is `map[string]func(context.Context, *Executor, *Client, Config, json.RawMessage) (json.RawMessage, error)` (unnamed func type — assignable to a named `OpFunc` if the coordinator declares one). Handlers: `HandleBotInstall`, `HandleBotDeployGit`, `HandleBotUpload`, `HandleBotFiles`, `HandleBotStart`, `HandleBotStop`, `HandleBotRestart`, `HandleBotKill`, `HandleBotStatus`, `HandleBotLogs`, `HandleBotDelete`.
3. `frontend/apps/customer/App.tsx` — mount `routes` from `./routes.bots` inside the guarded shell + add a "Bots" nav item (Bot icon). Coordinator-owned; not touched.
4. Optional 2-line agent improvement (PROPOSAL, not applied — `workload.go` is platform code): in `appKindFor`, prefer a kind marker so bot units report `kind=discord` in the raw envelope. Bots are keyed by id everywhere in the Phase 8 API/UI (kind is always presented as "discord"), so this only affects raw protocol consumers:
   ```go
   // in appKindFor, first line:
   if b, err := os.ReadFile("/srv/epicpanel/bots/" + id + "/.epicpanel-kind"); err == nil {
       return strings.TrimSpace(string(b)) // written by HandleBotInstall
   }
   ```

## Deviations (with reasons)
1. **No Docker daemon** (wave contract environment fact). Isolation = systemd transient units + cgroups v2 + dedicated per-bot unix user + strong unit sandboxing (NoNewPrivileges/ProtectSystem=strict/PrivateTmp/PrivateDevices/RestrictSUIDSGID/ReadWritePaths). "All container operations through the Node Agent" holds: the customer API has no node/daemon path at all; every op is a job. A Docker `ContainerDriver` can slot behind `BotRuntime`+ops later without API changes.
2. **Disk limits are accounted-only for bots** (like non-quota sites in Phase 9): systemd units cannot carry fs quotas; usage (disk_used_mb) is live-sampled by the Phase 3 collector; CPU/RAM/PIDs are kernel-enforced (cpu.max/memory.max/pids.max via unit properties from the SAME engine numbers the UI displays). Network controls: opt-in systemd IPAddressAllow/Deny allowlist (documented, "where supported").
3. **Console tail** is agent-pull (CP-scheduled `bot_logs` jobs, cursor-based) streamed to browsers over the WS hub — the Phase 3 agentproto stream is metrics-only and has no command/data channel; live log push from agent would need a protocol extension (flagged for the metrics-protocol owner). Never polls the metrics WS; logs and metrics are different channels.
4. **Job payloads carry env as ciphertext** (`env_enc` base64 AES-GCM), decryptable only on the node holding the panel key — mirrors the existing WordPress `db_password_enc` convention. Effective file-upload size is bounded by the API 1 MB body cap (config files; git deploy is the primary path).
5. **Scheduled tasks are lifecycle-only** (restart/start/stop). A "scheduled custom command" would be arbitrary shell by another name and contradicts the no-shell rule; console input is likewise nonexistent (read-only console by design).

## Definition of Done — mapping
- *Upload→deploy→start→crash→auto-restart→logs show both runs*: control-plane path proven by `TestBotLifecycleAndReconciliation` (start → LIVE truth running → crash → crashed+counters → policy recovery job → running → stop → stopped; policy "no" leaves crashed). Node-side flow is implemented via the `bot_upload`/`bot_deploy_git`/`bot_start` ops; systemd's `Restart=on-failure` + NRestarts + the control-plane recovery loop together produce the both-runs log trail (journald keeps both runs; the ring buffer serves them). Real-node verification requires a live agent — the unit in this environment cannot run systemd transient units.
- *Secret value never appears in any log line or API payload (automated test)*: PASS — see test locations above.
- *No path reaches docker.sock from customer role*: PASS structurally — zero docker references in the entire bot path (grep-verifiable); ops are systemd+fs only; API surface has no exec/shell endpoint; schedules and console accept lifecycle verbs only (tested).
- *Metrics match actual container*: PASS by construction — metrics come from the Phase 3 collector sampling the bot's own systemd unit (NRestarts, ActiveState, /proc of MainPID) streamed via the app envelope with LIVE/STALE/OFFLINE + age.

## Test evidence
- `cd backend && go build ./... && go vet ./...` — clean.
- `EPICPANEL_TEST_DATABASE_URL=postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test_dc?sslmode=disable go test ./... -p 1 -count=1` — **15/15 packages ok, 0 FAIL** (includes `internal/discord` 10 unit tests, `internal/agent` bot-op tests, `internal/api` 6 integration suites on the _dc DB).
- Frontend: `npx tsc --noEmit` — clean; `npx vite build --config apps/customer/vite.config.ts` — green (customer bundle unchanged size class, ~382 kB).
- Integration DB: `epicpanel_test_dc` (wave-assigned); migration proven re-runnable (harness drops `schema_migrations` but leaves the enum/tables).
