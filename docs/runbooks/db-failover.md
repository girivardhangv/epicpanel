# Runbook: Database Failover (panel Postgres)

Alert names: DB connectivity alerts from Phase 13 (connection errors on
the API's health path, healthz 503), jobs/events alerts caused by write
failures.

## Symptoms

- `curl /healthz` → `503 {"status":"degraded"}` (pool ping fails).
- API journal: repeated pool/psql errors (`SQLSTATE 57P03`,
  `connection refused`, `terminating connection`).
- Panel UI: login fails, jobs feed freezes, but the API process stays up
  (Restart=always hides nothing here).
- Live metrics ingest keeps draining into the in-memory live store; the
  history writer buffers (bounded, drop-counted) — see scale report §6.

## Diagnosis

```bash
systemctl status postgresql
pg_lsclusters
tail -50 /var/log/postgresql/postgresql-*.log
psql -d epicpanel -tAc "SELECT pg_is_in_recovery()"   # promoted replica? -> f
ss -tlnp | grep 5432
```

Distinguish:

- Local cluster down (crash, OOM, bad config) → restart locally.
- HA pair: primary lost → promote the replica (automatic with Patroni/
  repmgr; manual: `pg_ctl promote`), then re-point the API DSN/VIP.
- Connection storm / saturation: check `max_connections` vs
  `pg_stat_activity`; the API's pool size is modest by design.

## Remediation

Dev / single-node:

```bash
systemctl restart postgresql
# the API self-heals; no API restart required (pgx pool reconnects on
# the next query — verified by deploy/chaos/db-failover.sh)
```

HA pair (primary lost):

1. Confirm the old primary is really out (`systemctl stop postgresql` on
   it; fence it — split-brain is worse than downtime).
2. Promote the replica: `pg_ctl promote -D /var/lib/postgresql/<ver>/main`
   (or your cluster manager's promote command).
3. Re-point clients: update `EPICPANEL_DATABASE_URL` (or move the VIP /
   DNS). Keep `sslmode` consistent.
4. `systemctl restart epicpanel-api` ONLY if the DSN changed in the env
   file (env is read at start). Rolling restart replica-by-replica if
   you run 2× API.
5. Re-seed the old primary as a replica when it returns; do NOT let it
   rejoin as primary.

## Verification

```bash
curl -s "$API/healthz"                                    # {"status":"ok"}
psql -d epicpanel -tAc "SELECT pg_is_in_recovery()"       # f on the new primary
psql -d epicpanel -tAc "SELECT count(*) FROM sessions WHERE expires_at > now()"
journalctl -u epicpanel-api --since '-5 min' | grep -ciE "sqlstate|connection refused"
# expect 0 after recovery; sessions/jobs/events intact (durable in PG)
```

Drill: `deploy/chaos/db-failover.sh` (stops/restarts the local cluster,
asserts degraded-but-alive API, self-heal without restart, durability).

## Data-safety notes

- Sessions, jobs, events, backups metadata are all in Postgres; a
  failover loses only in-flight statements (transactions retry at the
  application backoff level).
- The metrics history writer drops samples under prolonged outage rather
  than blocking ingest (live path first) — historical graphs will show
  the gap; that is by design and documented in the scale report.
- Take a fresh `pg_dump` after failover before any risky operation —
  replication slots/rewind state can be messy post-promotion.
