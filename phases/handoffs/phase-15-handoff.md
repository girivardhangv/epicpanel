# Phase 15 — Session Handoff

## Scale numbers achieved vs target
Live-box measurements (docs/scale-report.md has methodology + raw reports):
- 100 simulated nodes @ 5s cadence, 4 sites/frame: 1294 frames sent / 1193 acked /
  0 errors; ingest ≈ 1.8% of one core (922 CPU-µs / 1000 frames); API RSS 26→37 MB.
- WS fan-out agent→browser: p50 0.42 ms / p99 1.24 ms (n=1190, 0 unmatched frames).
- REST under load: fleet projection p99 18 ms; healthz p99 20 ms.
- 1000-node run: NOT executed (hardware-bound) — honest [est] ~15–20% of one core.
- Freshness chaos: LIVE→STALE ≤5 s, never stale-as-live; agent resume replay verified.

## Remaining known limits (documented honestly)
- 1000-node / 1000-browser load runs need a sized host (estimates marked in report).
- Redis pub/sub driver code-reviewed only (no Redis on box).
- node-offline chaos drill runs but terminal-outcome assertion was blocked by a
  PRE-EXISTING agent panic (workload.go:123, `fields[1:]` on truncated io.stat line)
  — FIXED by coordinator (guarded empty lines), agent rebuilt + redeployed.
- /metrics route-class attribution bug — FIXED by coordinator (requestMetricsMiddleware,
  verified ticking: agent class counts increment live).

## Release decision
READY for v1.0.0 with documented exceptions (security checklist ADR-058; 1000-node
[sest] estimates). Deploy artifacts in deploy/ (hardened units match the LIVE services
already running from them; env template; logrotate; 4 chaos drills — 3 executed green).
install.sh hardened: dependency pre-checks, backup-before-migrate (pg_dump gzip, 7 kept),
migration-before-start ordering, rollback plan printed.
