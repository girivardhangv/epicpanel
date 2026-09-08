# PHASE 11 — Backups

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1045–1069 (Phase 11 spec — verbatim: "Don't make backups an afterthought.")
> 3. `phases/README.md` + Phase 7/8 handoffs (world/bot backup seams), Phase 10 handoff (backup-before-terminate)
> 4. Code: `backend/internal/backups`, `migrations/0009_backups.sql`, agent backup ops
>
> Depends on: Phases 3–4, 7, 8, 9 · Blocks: 13

## Mission
First-class backup/restore for EVERY workload type, with remote targets, retention, encryption, verification, scheduling.

## Scope — VERBATIM from master doc
Support:
```
Account backup | Database backup | Website backup
Minecraft world backup | Discord bot backup | Full instance backup
```
And:
```
Local | Remote | Object Storage
```
with:
```
retention | encryption | verification | restore | scheduled backups
```

## Work Items
- [ ] Backup driver interface: Local, Remote (ssh/rsync-style target — config only, no arbitrary shell from API), **Object Storage (S3-compatible)**; existing `backups` module refactored onto it
- [ ] Per-type backup jobs (all via agent, idempotent): account (fs+DBs+config bundle), database dump, website files, **Minecraft world (quiesce: save-off→copy→save-on or stop)**, **Discord bot (files + env, secrets encrypted or excluded)**, full instance (container fs snapshot)
- [ ] Encryption at rest: per-customer data key wrapped via `secretbox`/KMS pattern; encrypted before leaving node
- [ ] **Verification**: post-write checksum + periodic restore-to-scratch test job; unverified backup = alert (Phase 13)
- [ ] Retention policies per plan (Phase 9 `Backups: N` resource) + time-based pruning job
- [ ] Schedules: cron-driven via jobs; on-demand; pre-terminate automatic backup (Phase 10 hook)
- [ ] Restore flows per type: idempotent, reversible-where-practical, audited; Minecraft restore = stop→swap→start safely
- [ ] UI: customer backup list/create/restore/download per workload + WHM backup health (success/failure rates)

## Deliverables
Driver interface + per-type agent ops + encryption/verification + retention/scheduler + UI.

## Definition of Done
Scheduled + manual backup of each of the 6 types lands encrypted + verified on object storage; restore proven by test for each type (incl. Minecraft world into running instance); terminate flow always leaves a final backup.

## Session Handoff — FILL BEFORE ENDING SESSION
- Types done vs pending: (fill)
- Verification strategy implemented: (fill)
- Restore drill results: (fill)
- Update `phases/README.md` status row for Phase 11 → DONE
