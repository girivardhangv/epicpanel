# Bandwidth Accounting — Global Nginx Accounting Stream (ADR-064)

Billing-grade per-site bandwidth accounting for EpicPanel. Supersedes the
customer access-log component of ADR-063's HTTP accounting (nftables direct
egress is unchanged); everything else about the lifecycle (suspension
reasons, quota APIs, resume guard, history) keeps working unchanged.

## The three pipelines (do not mix them)

| Pipeline | Source | Cadence | Purpose |
|---|---|---|---|
| Live traffic | per-site access logs → metrics stream | ~60s windows | graphs, attack detection, live rates |
| **Bandwidth accounting** | **`/var/log/epicpanel/bandwidth.log`** | **5-min tally, hourly report** | **monthly billed usage, quota** |
| Direct process egress | nftables per-uid OUTPUT counters | hourly enforce | site processes bypassing nginx |

Final monthly formula (unchanged from ADR-063):

```
monthly_bandwidth = monthly_global_nginx_http (request + response bytes)
                  + monthly_direct_process_egress (nft per-uid counters)
```

## Architecture

### Nginx side (platform-controlled, customer-proof)

- `/etc/nginx/conf.d/epicpanel-bandwidth.conf` (root-owned, written by the
  agent): defines `log_format epicpanel_bandwidth
  '$epicpanel_site_id $time_iso8601 $request_length $bytes_sent
  "$http_user_agent"'` and the http-level `access_log
  /var/log/epicpanel/bandwidth.log`.
- Every EpicPanel-generated vhost (nginx provider renderer) carries, in each
  server block: `set $epicpanel_site_id "<uuid>";` and
  `access_log /var/log/epicpanel/bandwidth.log epicpanel_bandwidth;`. The
  server-level access_log is REQUIRED because an nginx server-level
  access_log replaces the http-level one — without it the vhost would drop
  out of the billing stream. Redirect-only blocks, ACME blocks and lifecycle
  stubs (suspended/terminated/quota) carry the same lines.
- Customers cannot disable any of this: vhost files are agent-generated
  (`DO NOT EDIT`), the only customer-controlled nginx input is the
  allowlist-validated rewrite snippet (the sanitizer rejects `access_log`,
  `set` of unknown variables, and any `$epicpanel_*` reference), the global
  conf is root-owned in conf.d, and the log lives under root:adm `0750`
  `/var/log/epicpanel` — outside every site tree. Deleting a customer
  access log (`/srv/epicpanel/websites/<id>/logs/...`) has ZERO effect on
  billing.
- `/etc/logrotate.d/epicpanel-bandwidth`: daily, rotate 14, compress +
  **delaycompress** (keeps `bandwidth.log.1` plain so the accountant can
  drain it after rename rotation), postrotate sends nginx USR1.

### Agent side (one worker per node: `internal/agent/bwtail.go`)

`BWAccountant` runs in the agent process:

1. **Tally every 5 minutes** (`EPICPANEL_AGENT_BW_TALLY_INTERVAL` overrides,
   min 1s for tests) + one pass at boot: delta-read the accounting log,
   parse `<site-uuid> <time> <req-bytes> <resp-bytes> "ua"`, fold
   `req+resp` into the per-site month-to-date accumulator. Malformed lines,
   `-` site ids (default vhost) and platform self-traffic (`EpicPanel-` UA)
   are skipped. Bounded memory: 64 KB buffered streaming, never the file.
2. **Crash-safe ordering**: the accumulator and the `inode+offset`
   checkpoint land in ONE atomic `bw_state.json` write (`tmp+rename`, with a
   `.bak` generation). A crash before the write replays the range with no
   double-count (the accumulator that would have been inflated never
   survived); a crash after it never re-reads. The checkpoint never
   advances before the bytes are durable.
3. **Idempotency / no additive `+=` across the wire**: the agent reports the
   CUMULATIVE month-to-date via the existing hourly `enforce_limits` job;
   the control plane keeps the `GREATEST(existing, incoming)` high-water in
   `workload_resource_usage`. Replays cannot inflate billing.
4. **Rotation**: inode change → drain `bandwidth.log.1` (then `.2`) from
   the stored offset before switching to the new file. Truncation in place
   → reset to 0 (pre-truncation bytes logged as lost — the copytruncate
   caveat). Compressed generations are only read by recalc.
5. **Month attribution**: each record is attributed by ITS OWN timestamp —
   September records are never charged to October, even when processed
   after the boundary. (A record written in September but first processed
   in October is not billed to September either — the ≤ tally-interval tail
   gap at month end is the one documented, bounded undercount.)
6. **Upgrade continuity**: on first boot of the accountant, the accumulator
   is seeded from the persisted `sites` map of `bw_state.json` (the old
   access-log month-to-date) and the checkpoint seeks to EOF — the billed
   month carries over instead of resetting.
7. **Report path** (unchanged cadence): hourly `enforce_limits` composes
   `nft rx+tx + HTTP month-to-date` → `Usage["bandwidth"]` → control-plane
   fanout upserts `workload_resource_usage` (GREATEST). Enforcement stays
   periodic; `bandwidth_exhausted` suspension stays idempotent; the resume
   guard, quota API and suspension-page metadata keep reading the same row.

### Recalculation / reconciliation (admin-only)

- `POST /v1/organizations/{org}/websites/{id}/bandwidth/recalculate`
  `{from_day, to_day, apply}` (org admin or platform admin; audited).
  Enqueues a `recalc_bandwidth` agent job (migration 0053).
- The agent re-scans every `bandwidth.log*` generation (plain + gzip) and
  returns per-day `request/response/total` byte aggregates for the site.
- `apply=false` → pure reconciliation report (job result holds the raw-log
  truth; compare against the DB / daily buckets).
- `apply=true` → the fanout repairs `workload_resource_usage` for each
  covered month with `GREATEST(existing, recalculated)`: repair can raise
  usage but never lower the billed period. Daily history buckets keep their
  existing semantics (live windows, response-dominated) and are not touched.
- Recalc can only see records since the accounting log was born (or since
  the oldest retained rotation, ≤ 14 days of generations by default).

## Deployment (no downtime required)

Order matters; every step is safe to re-run.

1. **Database migration**: none needed for the accounting itself (the
   schema is unchanged). `0053_recalc_bandwidth.sql` adds the
   `recalc_bandwidth` job type — it applies automatically when the upgraded
   control plane starts (migrations run on API boot).
2. **Deploy the new agent binary** (`backend/build-release.sh` per the dev
   box pattern; user-driven restart of `epicpanel-agent`). On the first
   start the agent:
   - writes `/etc/nginx/conf.d/epicpanel-bandwidth.conf`,
     `/var/log/epicpanel/` (0750 root:adm) and
     `/etc/logrotate.d/epicpanel-bandwidth`;
   - seeds the HTTP accumulator from `bw_state.json` (billed month carries
     over) and seeks the accounting log to EOF.
   Until step 3's reload, no HTTP traffic is being logged to the new stream
   (the composition degrades to nft-only; the GREATEST high-water prevents
   any usage regression).
3. **Reload nginx** (`systemctl reload nginx` — safe, keep-alives are kept)
   or just let the agent converge: the first vhost Ensure/reconcile writes
   the conf first and reloads. The control plane now re-renders every ready
   site's vhost once at control-plane boot and daily afterwards
   (`resyncVhosts`), so the per-server accounting stamps appear without
   manual work.
4. **Validate**:
   ```bash
   nginx -t                                                # must pass
   ls -la /var/log/epicpanel/bandwidth.log                 # root:adm 0640
   curl -s http://<any-site>/ >/dev/null
   tail -1 /var/log/epicpanel/bandwidth.log                # "<uuid> <ts> .. .."
   ```
5. **Check accounting**: within a 5-minute tally + hourly enforce cycle, the
   site's bandwidth card / `GET .../bandwidth` shows usage from the new
   source.
6. **Permissions**: nothing to do — the agent (root) creates
   `/var/log/epicpanel` 0750 root:adm and the log is created by nginx master
   (root) 0640. Site users cannot reach the directory.
7. **Rollback**:
   - Remove `/etc/nginx/conf.d/epicpanel-bandwidth.conf` and
     `/etc/logrotate.d/epicpanel-bandwidth`, reload nginx, re-deploy the
     previous agent binary. Vhosts rendered by the new agent reference the
     format name, so REMOVE the conf only AFTER downgrading the agent (the
     old agent re-renders vhosts without the stamps on its next converge;
     `nginx -t` never fails with both in place — the conf is simply unused).
   - `workload_resource_usage` / `website_bandwidth_*` / `bw_state.json` are
     all preserved; the old agent resumes its access-log accumulator from
     the same `sites` map (a superset-compatible read of the new state).

## Test coverage map (spec §38)

- Global config + stamps in every server block: `TestRenderVhostAccountingStamp`,
  `TestRenderVhostRedirectBlocks`, `TestNginxEnsure*` (nginx -t order).
- Customer `access_log off` / spoof attempts: sanitizer unit tests
  (`lineHasSmuggledDirective` blocks the directive; `varRefRe` blocks
  `$epicpanel_*`) — unchanged, exercised via `TestRenderVhost*`.
- Parsing/attribution: `TestBWAccountantParseAndAttribution` (multi-site,
  multi-domain, self-traffic, garbage, GB-scale).
- Incremental + checkpoint + restart: `TestBWAccountantIncrementalAndRestart`,
  `TestBWAccountantPersistWritesState`.
- Replay idempotency: `TestBWAccountantReplayAfterLostState`.
- Rotation / double rotation / truncation: `TestBWAccountantRotation`,
  `TestBWAccountantDoubleRotation`, `TestBWAccountantTruncation`.
- Month rollover: `TestBWAccountantMonthRollover`.
- Deploy-order tolerance (no log yet): `TestBWAccountantNoLogYet`.
- Recalc/reconcile: `TestRecalcScan`, `TestBandwidthRecalcRouteAndRepair`
  (report-only untouched, GREATEST repair, never-lower).
- nft direct egress composition: existing `enforce_test.go` +
  `ensureBwChain` predicates (`TestBwChainDecisions`).
- Suspension reasons/resume guard: unchanged (`lifecycle_bandwidth_test.go`).
