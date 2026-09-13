import { useCallback, useEffect, useState } from 'react'
import { ListChecks, RotateCcw, Ban, Skull } from 'lucide-react'
import { Card, EmptyState, PageTitle, StatCard, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { ConfirmDialog } from '@epicpanel/forms'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { ConsoleJob } from '../adminview'

type View = 'all' | 'pending' | 'running' | 'failed' | 'dead'

/**
 * Jobs console: list + filter + retry + cancel + dead-letter view. Retry and
 * cancel are server-side authorized (platform-admin session) and audited.
 */
export function JobsPage() {
  const [rows, setRows] = useState<ConsoleJob[] | null>(null)
  const [type, setType] = useState('')
  const [view, setView] = useState<View>('all')
  const [confirm, setConfirm] = useState<{ job: ConsoleJob; retry: boolean } | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (view === 'dead') {
      const r = await adminview.deadLetter()
      setRows(r.jobs ?? [])
      return
    }
    const q: Record<string, string> = {}
    if (view !== 'all') q.status = view
    if (type) q.type = type
    const r = await adminview.jobs(q)
    setRows(r.jobs ?? [])
  }, [view, type])

  useEffect(() => {
    void load()
  }, [load])

  const act = async () => {
    if (!confirm) return
    setBusy(true)
    try {
      if (confirm.retry) {
        await adminview.retryJob(confirm.job.id)
        pushToast('success', `Job ${confirm.job.type} requeued`)
      } else {
        await adminview.cancelJob(confirm.job.id)
        pushToast('success', `Job ${confirm.job.type} cancelled`)
      }
      setConfirm(null)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Action failed')
    } finally {
      setBusy(false)
    }
  }

  const counts = {
    failed: view === 'failed' ? (rows?.length ?? 0) : 0,
  }

  const columns: Column<ConsoleJob>[] = [
    {
      key: 'job', header: 'Job', render: (r) => (
        <div>
          <div className="table-primary font-mono">{r.type}</div>
          <div className="table-secondary">{r.id.slice(0, 8)}</div>
        </div>
      ),
      filter: (r) => `${r.type} ${r.id}`,
    },
    {
      key: 'target', header: 'Target', render: (r) => (
        <div>
          <div className="table-primary">{r.website || '—'}</div>
          <div className="table-secondary">{r.server}</div>
        </div>
      ),
      filter: (r) => `${r.website} ${r.server}`,
    },
    { key: 'status', header: 'Status', render: (r) => <JobStatus job={r} /> },
    {
      key: 'attempts', header: 'Attempts', render: (r) => (
        <span className="text-muted">{r.attempts}/{r.max_attempts}</span>
      ),
    },
    {
      key: 'error', header: 'Error', render: (r) => (
        <span className="block max-w-[280px] truncate text-[9.5px] text-danger" title={r.error}>{r.error || '—'}</span>
      ),
    },
    { key: 'created', header: 'Created', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
    {
      key: 'actions', header: '', render: (r) => (
        <div className="flex justify-end gap-[5px]">
          {(r.status === 'failed' || r.status === 'success') && (
            <button className="icon-btn" title="Retry job" aria-label="Retry job" onClick={() => setConfirm({ job: r, retry: true })}>
              <RotateCcw size={13} />
            </button>
          )}
          {r.status === 'pending' && (
            <button className="icon-btn" title="Cancel job" aria-label="Cancel job" onClick={() => setConfirm({ job: r, retry: false })}>
              <Ban size={13} />
            </button>
          )}
        </div>
      ),
    },
  ]

  const views: { key: View; label: string }[] = [
    { key: 'all', label: 'All' },
    { key: 'pending', label: 'Pending' },
    { key: 'running', label: 'Running' },
    { key: 'failed', label: 'Failed' },
    { key: 'dead', label: 'Dead letter' },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Jobs" subtitle="Queue console: filter, retry and cancel platform jobs." />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Terminal failures" value={counts.failed} tone={counts.failed > 0 ? 'red' : 'blue'} icon={<ListChecks size={15} strokeWidth={1.8} />} />
        <StatCard label="View" value={views.find((v) => v.key === view)?.label ?? ''} icon={<ListChecks size={15} strokeWidth={1.8} />} />
        <StatCard label="Dead letter" value={view === 'dead' ? (rows?.length ?? 0) : ''} tone="amber" icon={<Skull size={15} strokeWidth={1.8} />} />
        <StatCard label="Rows shown" value={rows?.length ?? ''} icon={<ListChecks size={15} strokeWidth={1.8} />} />
      </div>

      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.type} ${r.server} ${r.website} ${r.error}`}
          filterSlot={
            <>
              <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={view} onChange={(e) => setView(e.target.value as View)}>
                {views.map((v) => <option key={v.key} value={v.key}>{v.label}</option>)}
              </select>
              <input
                className="input h-[34px] w-[170px] text-[11px]"
                placeholder="Filter by job type..."
                value={type}
                onChange={(e) => setType(e.target.value)}
              />
            </>
          }
          minWidth={940}
          empty={
            <EmptyState
              icon={<ListChecks size={20} />}
              title={view === 'dead' ? 'Dead letter is empty' : 'No jobs'}
              subtitle={view === 'dead' ? 'Terminal failures land here for triage and retry.' : 'Jobs appear as the platform works.'}
            />
          }
        />
      </Card>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={act}
        busy={busy}
        title={confirm?.retry ? 'Retry job' : 'Cancel job'}
        message={
          confirm
            ? confirm.retry
              ? `Requeue ${confirm.job.type} on ${confirm.job.server}? The job will run with a fresh attempt budget; the failure history stays on record.`
              : `Cancel ${confirm.job.type} on ${confirm.job.server}? Only pending jobs can be cancelled — the agent has not claimed this one yet.`
            : ''
        }
        confirmLabel={confirm?.retry ? 'Retry' : 'Cancel job'}
        danger={!confirm?.retry}
      />
    </div>
  )
}

function JobStatus({ job }: { job: ConsoleJob }) {
  const map: Record<string, string> = {
    pending: 'badge-warn',
    running: 'badge-warn',
    success: 'badge-ok',
    failed: 'badge-off',
  }
  return (
    <span className={map[job.status] ?? 'badge-neutral'}>
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {job.status}
    </span>
  )
}
