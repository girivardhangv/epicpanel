# Runbook: Disk Full (node)

Alert names: threshold rules on `disk_percent` / `disk_used` (alert engine
evaluates live samples; default rules raise at the configured % threshold),
plus `limits.breach` events when per-account disk quotas are exceeded.

## Symptoms

- Alert feed: disk threshold alert for the node or a workload.
- Node's `disk_used_bytes` approaches `disk_total_bytes` in the fleet
  metrics projection.
- On the node: services fail to write (nginx/php-fpm errors, Postgres
  `PANIC: could not write to file`), backups fail mid-stream, logs freeze.
- Customer-visible: 500s, "disk quota exceeded" mail bounces.

## Diagnosis

```bash
ssh <node>
df -h; df -i                  # bytes and inodes — either can be "full"
du -xh --max-depth=2 /var | sort -h | tail -15
journalctl -u epicpanel-agent --since '-10 min' | grep -i disk
```

Panel side: `/v1/admin/observability/workloads` shows per-workload disk
against quota; `enforce.go` reports measured usage from the same cgroup
layer that enforces it.

## Remediation

1. Identify the top consumer (usually: site logs, mail queue, Postgres
   WAL, old backups, core dumps).
2. Free space immediately:
   - rotate/compress logs: `logrotate -f /etc/logrotate.d/epicpanel`
     (or the site's logrotate config),
   - prune old node-local backups after confirming the off-node copy
     exists (Phase 11 verify hashes),
   - `journalctl --vacuum-size=100M` on the node.
3. Stop the bleed: suspend the offending workload (panel → website →
   suspend; this enqueues `suspend_website` and applies cgroup/perm
   limits) rather than deleting customer data by hand.
4. If the root volume is genuinely too small: resize the volume, or move
   the heavy dir to a bigger mount and re-point (site docroot symlinks
   are supported by the web engine).
5. inode exhaustion: find the directory with millions of files
   (`df -i`, then `for d in */; do echo "$(find $d -xdev | wc -l) $d"; done | sort -n`).

## Verification

```bash
df -h <mount>                       # usage below threshold
curl -s "$API/v1/organizations/$ORG/servers/$ID/metrics" | jq '.metrics.node.disks'
# alert auto-resolves on the next healthy sweep, or resolve manually:
curl -X POST "$API/v1/admin/alerts/<alert_id>/resolve" -H 'X-EpicPanel: 1' -b "epicpanel_session=…"
```
