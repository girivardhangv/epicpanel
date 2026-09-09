# Runbook: Provision Failed

Alert names: `job.failed` events (`provision_website`,
`install_wordpress`, `provision`-class), `provisioning_failed` audit
trail, billing `billing.grace_started` when a paid order's provisioning
retries are exhausted.

## Symptoms

- Admin: jobs feed shows the provision job `failed` (dead-letter after
  `max_attempts` with exponential backoff between tries).
- Customer: order paid but no instance appears; or website card shows
  provisioning stuck in an error state.
- Events stream: repeated `job.claimed` without a terminal success.

## Diagnosis

```bash
psql -d epicpanel -c "SELECT id,type,status,attempts,error,visible_after
                      FROM jobs WHERE server_id='<id>' AND type='provision_website'
                      ORDER BY created_at DESC LIMIT 5"
journalctl -u epicpanel-agent --since '-30 min' | grep -iE "provision|error"
journalctl -u epicpanel-api   --since '-30 min' | grep -iE "enqueue|job"
```

Check, in order:

1. **Node state** — is the target node LIVE? A provision onto a stale
   node stalls until the lease expires and requeues (the drill
   `deploy/chaos/node-offline-mid-provision.sh` proves the recovery path).
2. **Agent log for the claimed attempt** — the job error string in
   `jobs.error` is the agent's report; typical causes: package manager
   lock held, DNS not propagated (domain verify step), cert issuance
   rate-limited, disk full, PHP version unavailable for the OS.
3. **Idempotency** — retries carry the same idempotency key
   (`EnqueueIdempotent`), so a half-built site is completed, never
   duplicated. If the payload drifted (customer changed plan mid-run),
   cancel the pending job and enqueue fresh.
4. **Maintenance mode** — `servers.maintenance_mode = true` blocks
   claims (`ClaimNext` filters it); lift it if the node was flagged.

## Remediation

1. Fix the root cause (free disk, unlock apt, wait out the LE rate
   limit, correct the domain).
2. Requeue: panel → node → Jobs → retry, or insert via the API with the
   same idempotency key. Backoff already spaces attempts
   (`visible_after`); the reaper covers crashed attempts.
3. Billing path: if a paid order is now provisioned, confirm
   `billing_orders` transitioned and the customer notification fired
   (`billing.*` events); if retries were exhausted and grace started,
   clear the grace state after manual provisioning.
4. Customer comms: provisioning SLA clock stops at first failure; send
   the delay notice from the admin order view.

## Verification

```bash
psql -d epicpanel -c "SELECT id,status,finished_at FROM jobs WHERE id='<job_id>'"
curl -s "$API/v1/ws"  # UI: website.created event lands; card turns ready
curl -s "$API/v1/organizations/$ORG/websites" | jq '.[] | {id,status}'
```
