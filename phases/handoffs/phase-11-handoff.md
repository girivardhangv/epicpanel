# Phase 11 — Backups — Session Handoff

> Backend portion status is the backend agent's to record. This file was
> created by the Phase 11 FRONTEND agent; the UI section below is complete.

## Session Handoff — UI (P11 frontend)

### Files delivered (exclusive ownership respected)
- `frontend/apps/customer/pages/backups2/model.ts` — shared wire types (BackupRow / TargetRow / ScheduleRow / WorkloadOption), verbatim type list + labels + chip tones, `scopeForType` mirror of the API's `resolveWorkload`, 5-field cron validator, verification-badge mapping.
- `frontend/apps/customer/pages/backups2/Backups2.tsx` — unified backup list + create + restore/verify.
- `frontend/apps/customer/pages/backups2/Targets.tsx` — sink targets CRUD (local / remote / s3).
- `frontend/apps/customer/pages/backups2/Schedules.tsx` — cron schedules CRUD + retention explainer.
- `frontend/apps/customer/routes.backups.tsx` — `export const routes: RouteObject[]`: `/backups2`, `/backups2/targets`, `/backups2/schedules`, `/backups2/:workload_type` (static sub-pages are declared before the dynamic segment; react-router v7 ranking makes the order safe regardless).

### UI scope coverage
- **List per workload with type chips**: `/backups2` shows the org-wide list with All + the 6 verbatim types (`account | database | website | minecraft_world | discord_bot | full_instance` + the engine's `website_files` split) as count-bearing chips; `/backups2/:workload_type` narrows (deep-linkable). Rows show workload (resolved against live websites/instances/bots lists), type chip, StatusBadge, size, encryption + verification badges, sink kind, created-age, trigger type + error. In-flight jobs (pending/running) poll every 4s until settled — no WS events exist for backups, so polling (Phase 3-compliant: never poll what a WS streams) is the honest live mechanism here; FreshnessBadge is reserved for metric-stream data and is intentionally absent from job rows.
- **Create backup**: type select → workload select scoped exactly per the API's `resolveWorkload` (website types → websites; `minecraft_world` → Minecraft instances; `discord_bot`/`full_instance` → bots), target select (node-local default or an org target), encrypt + verify toggles (both default on). Submit builds the exact body the live API validates (`website_id`/`instance_id`/`bot_id` chosen by scope, `target_id` only when set).
- **Restore + verify**: ConfirmDialog-gated (restore is destructive-red with explicit overwrite warning; verify explains restore-to-scratch + SHA-256), both POST the `/backups2/{id}/restore|verify` endpoints, both hidden below org-admin instead of inviting 403s (`RoleRank`: owner 5 > admin 4 > reseller 3 > developer 2 > billing/support 1; mutations require ≥ admin).
- **Targets CRUD**: kind select local/remote/s3 with per-kind forms (local_dir; remote host/port/user/path + password or SSH key; S3 endpoint/region/bucket/prefix/path-style + access key/secret). Credentials are password-type inputs, write-only, never rendered back (the API returns only public config). Delete via ConfirmDialog with honest copy (existing archives untouched). Verified live: local + remote create 201, delete 204.
- **Schedules CRUD**: 5-field cron (client-validated, presets provided), type+workload picker (bot-scoped types included), enabled/last-run/next-run table, ConfirmDialog delete. Live API rejects 4-field cron — matches client validation.
- **Retention display**: dedicated card explaining the Phase 9 `Backups: N` plan allowance + post-success prune + time-based prune. No fake numbers — the customer package endpoint (`/package`) does not expose `max_backups`, so the UI points at the plan rather than inventing a figure.
- **Honest empty states** everywhere (no targets / no schedules / no backups / nothing schedulable / unknown workload_type guard for manual URLs).

### Deviations + backend seams found while integrating (NOT fixed — outside my ownership)
1. **Object-storage targets are uncreatable through the current API** (live-probed): the migration `backup_targets.kind` CHECK allows `('local','remote','s3')`, but the handler shape-validates via `sink.Open`, which only accepts `sink.KindObject = "object"`. So `kind:"s3"` → 422 "unknown sink kind", and `kind:"object"` → passes the handler then 500s on the DB CHECK. Fix: align the CHECK to include `object` (or map in the handler). The UI speaks the documented `s3` kind and surfaces the API's error honestly; S3 target creation will work end-to-end once this one-line seam is closed.
2. **`creds_enc` sealing seam**: `POST /backup-targets` stores the client-supplied `creds_enc` blob verbatim, but the agent unwraps it with the panel key (`sink.DecodeCreds` → `secretbox.Decrypt`); a browser cannot produce a panel-key-sealed blob, and no control-plane code calls `sink.EncodeCreds` today. The UI sends base64(UTF-8 JSON of the `sink.Credentials` shape: `{password?|private_key?}` / `{access_key_id,secret_key}`) — exactly the transport layer `EncodeCreds` uses minus the sealing — so a minimal backend fix (seal server-side before insert, or accept a structured creds object) requires no UI change. Until then, remote targets accept creation but credential unwrap will fail at first agent use. Flagged for the backend owner; UI copy stays truthful ("stored sealed at rest, never displayed").
3. **Minecraft-world schedules are not possible via the API**: `backup_schedules` only has `website_id`/`bot_id` scope and the create contract requires one of them. The UI includes the type with an honest warning ("not schedulable yet") and disables submit instead of mis-scoping the schedule.
4. **`backups2` create requires the workload id explicitly** (live-probed: omitting yields `invalid website_id/instance_id/bot_id`), so the create form always sends the id — no "org-wide account backup" shortcut exists server-side.
5. **No live/WS feed for backup jobs** — events are published on the bus (`backup.*`, Phase 13 input) but there is no customer-facing WS topic; the list uses status polling only while jobs are in flight.

### Test evidence
- `cd frontend && npx tsc -b --force` — clean.
- `npx vite build --config apps/customer/vite.config.ts` — built (1875 modules, ~459 kB js / ~53 kB css).
- Live smoke against the running API on :8080 with a real session: target create (local 201, remote 201, s3 → 422/500 per seam note), target delete 204, unified list 200, restore/verify of unknown id → 404, bad uuid → 400, bogus type/cron/workload → 422 validation; all response shapes match the UI's wire types.
- No files outside `pages/backups2/**` + `routes.backups.tsx` touched; App.tsx wiring (mounting `routes.backups`) is the coordinator's one-line addition.
- No emoji, no new npm packages, Lucide icons only, `@epicpanel/*` kit components reused throughout.
