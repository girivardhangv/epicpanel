import { useEffect, useState } from 'react'
import { History, ShieldAlert } from 'lucide-react'
import { Card, CardHeader, EmptyState, PageTitle, StatusBadge, SkeletonRows, StatCard } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { fmtBytes, timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminBackup, Overview } from '../adminview'

/**
 * Backups admin view: real rows from the existing backups module, with the
 * Phase 11 scope stated honestly (remote targets + verification land there).
 */
export function BackupsPage() {
  const [rows, setRows] = useState<AdminBackup[] | null>(null)
  const [ov, setOv] = useState<Overview['backups'] | null>(null)

  useEffect(() => {
    Promise.all([adminview.backups(), adminview.overview()])
      .then(([b, o]) => {
        setRows(b.backups ?? [])
        setOv(o.backups)
      })
      .catch(() => setRows([]))
  }, [])

  const columns: Column<AdminBackup>[] = [
    {
      key: 'site', header: 'Account', render: (r) => (
        <div>
          <div className="table-primary">{r.website_name}</div>
          <div className="table-secondary">{r.org_name}</div>
        </div>
      ),
      filter: (r) => `${r.website_name} ${r.org_name}`,
    },
    { key: 'type', header: 'Type', render: (r) => <span className="badge-neutral capitalize">{r.type}</span> },
    { key: 'trigger', header: 'Trigger', render: (r) => <span className="capitalize text-sub">{r.trigger_type}</span> },
    { key: 'size', header: 'Size', render: (r) => <span className="text-muted">{r.size_bytes ? fmtBytes(r.size_bytes) : '—'}</span> },
    { key: 'status', header: 'Status', render: (r) => r.error ? <StatusBadge status={r.status} /> : <StatusBadge status={r.status} /> },
    {
      key: 'error', header: 'Error', render: (r) => (
        <span className="block max-w-[240px] truncate text-[9.5px] text-danger" title={r.error}>{r.error || '—'}</span>
      ),
    },
    { key: 'created', header: 'Created', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Backups" subtitle="Snapshots and restore points across customers." />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Total snapshots" value={ov ? ov.total : ''} loading={!ov} icon={<History size={15} strokeWidth={1.8} />} />
        <StatCard label="Last 7 days" value={ov ? ov.last_7d : ''} tone="green" loading={!ov} icon={<History size={15} strokeWidth={1.8} />} />
        <StatCard label="Failures (7d)" value={ov ? ov.failed_7d : ''} tone={ov && ov.failed_7d > 0 ? 'red' : 'blue'} loading={!ov} icon={<ShieldAlert size={15} strokeWidth={1.8} />} />
        <StatCard label="Retention engine" value="Phase 11" tone="amber" icon={<History size={15} strokeWidth={1.8} />} />
      </div>

      <Card className="mb-4">
        <CardHeader title="Phase 11 scope" subtitle="What this view intentionally does not fake" />
        <p className="text-[11.5px] leading-relaxed text-sub">
          Backup sink drivers (local / remote / object storage), per-type restore jobs and retention
          pruning land with Phase 11. This view lists the existing backup records honestly; schedule
          configuration stays on each site's cPanel.
        </p>
      </Card>

      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.website_name} ${r.org_name} ${r.type} ${r.status}`}
          minWidth={860}
          empty={<EmptyState icon={<History size={20} />} title="No backups yet" subtitle="Backups run from the customer panel or on schedule." />}
        />
      </Card>
    </div>
  )
}
