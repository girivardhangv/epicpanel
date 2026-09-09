# Runbook: Backup Failed

Alert names: `backup.failed` (job terminal failure event), backup
threshold/state rules from the Phase 13 alert engine; the jobs feed shows
`create_backup` / `restore_backup` rows with `status=failed`.

## Symptoms

- Alert feed: backup failure alert for a website/bot/instance.
- Jobs feed: `create_backup` failed with `attempts = max_attempts`.
- Panel Backups page: last successful backup older than schedule.
- Phase 11 invariant: a failed job NEVER leaves a "verified" backup row —
  any row without a passing verify is suspect.

## Diagnosis

```bash
psql -d epicpanel -c "SELECT id,server_id,payload,error,attempts,finished_at
                      FROM jobs WHERE type IN ('create_backup','restore_backup')
                      AND status='failed' ORDER BY finished_at DESC LIMIT 10"
ssh <node> df -h                    # destination full is the #1 cause
journalctl -u epicpanel-agent --since '-1 hour' | grep -iE "backup|dump"
```

Common causes, in order of observed frequency:

| Cause | Signature | Fix |
|---|---|---|
| Destination full | agent log `no space left on device`, disk alert | free space (runbook disk-full.md), retry |
| Source DB dump failed | `pg_dump`/`mysqldump` auth or version error | rotate DB creds in panel, pin client version |
| Target unreachable (S3/SSH) | timeouts, TLS/DNS errors in agent log | check target creds (`backup_targets`), network path |
| Verify hash mismatch | `backup_verify` failure after transfer | disk/network corruption — delete the bad artifact, retry, then investigate the path |
| Node offline mid-run | lease expired + reaper requeue then terminal fail | runbook node-offline.md |

## Remediation

1. Fix the underlying cause from the table above.
2. Re-run: panel → Backups → run now (enqueues a fresh `create_backup`
   with a new idempotency key), or
   `POST /v1/organizations/{org}/websites/{id}/backups` with a session.
3. If the failure was on restore: STOP. Do not retry blind into a
   customer site. Confirm the target site is still intact, pick the next
   older verified backup, and run the restore during the customer's
   window. Restores are lease-protected jobs; a crashed restore self-
   requeues (it never leaves a partial restore marked complete).
4. Check the schedule still makes sense: a site grown beyond its window
   will fail repeatedly near the quota — raise the schedule target size
   or prune depth (`backup_prunes`).

## Verification

```bash
psql -d epicpanel -c "SELECT id,status,finished_at FROM jobs WHERE type='create_backup'
                      ORDER BY created_at DESC LIMIT 3"          # success rows
psql -d epicpanel -c "SELECT name,verified,size_bytes,created_at FROM backups
                      WHERE website_id='<id>' ORDER BY created_at DESC LIMIT 3"
```

Restore-drill evidence (Phase 11 acceptance) lives in the phase log; the
quarterly drill re-runs `backup → restore → diff` on a scratch site.
