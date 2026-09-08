# PHASE 15 — Performance & Scale → Production Release

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1177–1221 (Phase 15 + the two flow diagrams) and 1223–1300 (Development Order + priority note)
> 3. `docs/architecture-report.md` § performance risks (Phase 1) + ALL phase handoff notes in `phases/*.md`
> 4. Code: whole repo; `install.sh`; `EPICPANEL.md`
>
> Depends on: Phases 1–14 · Final phase

## Mission
Scale to 1 → 10 → 100 → 1000+ nodes **without redesigning the application**, then ship v1.

## The two flows — VERBATIM from master doc (this is the acceptance law)
Never do this:
```
HTTP request → SSH into server → execute command → wait → return
```
Instead:
```
API → Job → Queue → Node Agent → Execution → Event → WebSocket → UI
```

## Work Items
### Scale verification
- [ ] Sweep every endpoint for sync SSH/exec/command-in-request-path (Phase 1 list + new code from 4–13) → convert to job/queue/event flow
- [ ] Control plane statelessness: sessions/state in Postgres/Redis; run 2 API instances behind LB; WS fan-out via Redis pub/sub — verify no sticky-session hacks
- [ ] Metrics pipeline load test: simulate 100 then 1000 nodes (agent simulator replaying Phase 3 protocol); measure ingest CPU, WS fan-out latency, DB write batching; fix bottlenecks found
- [ ] DB: Phase 1 inefficient-query list → index/pagination/rewrite pass; pgx pool sizing; historical rollup/pruning jobs tuned
- [ ] Queue: worker pool sizing, backpressure, priority lanes (provisioning vs metrics vs backups); SKIP LOCKED throughput verified
- [ ] Agent at scale: one node with 200+ workloads — collection stays accurate under load (Phase 3 accuracy test re-run at fleet scale)
### Production release
- [ ] Packaging: systemd units for `epicpanel-api` + `epicpanel-agent` (doc stack includes systemd); `install.sh` hardened + tested on clean VM
- [ ] Config/secrets provisioning story documented; migration/upgrade path (backup-before-migrate)
- [ ] Chaos tests: node offline mid-provision, agent reconnect (sequence resume), API restart during WS stream, DB failover drill
- [ ] Freshness guarantee re-proven: CPU spike → UI <2s LIVE; agent killed → STALE→OFFLINE; **never stale-as-live**
- [ ] Security sign-off (Phase 12 checklist final), backups restore drill evidence (Phase 11), alert runbooks (`docs/runbooks/`)
- [ ] Docs: operator guide, customer guide, `/v1` API reference (openapi exists — regenerate)
- [ ] Version tag v1.0.0, release notes, rollback plan

## Deliverables
Scale test report, systemd/installer artifacts, runbooks, release checklist, v1.0 tag.

## Definition of Done
Target-node-count simulation passes with monitoring accurate (no stale-as-live) and API p99 within budget; full lifecycle (provision→suspend→terminate + backup→restore) passes end-to-end for web + Minecraft + Discord under load; every infra op provably flows API→Job→Queue→Agent→Event→WS→UI.

## Session Handoff — FILL BEFORE ENDING SESSION
- Scale numbers achieved vs target: (fill)
- Remaining known limits (documented honestly): (fill)
- Release decision: (fill)
- Update `phases/README.md` status row for Phase 15 → DONE
