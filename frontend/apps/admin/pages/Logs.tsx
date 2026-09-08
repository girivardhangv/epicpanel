import { useEffect, useState } from 'react'
import { ScrollText, Download } from 'lucide-react'
import { Card, EmptyState, PageTitle, Initials, SkeletonRows } from '@epicpanel/ui'
import { Toolbar, ToolbarSearch } from '@epicpanel/ui'
import { timeAgo } from '@epicpanel/core'
import { api } from '@epicpanel/core'

// Fleet-wide audit row (superset of the shared AuditEntry: admins also see
// the actor type and source IP recorded by the audit store).
interface AdminAuditRow {
  id: number
  actor_email?: string
  actor_type: string
  action: string
  resource_type: string
  resource_id: string
  result: string
  ip?: string
  created_at: string
}

const NOISE_PREFIXES = ['job.', 'server.metrics', 'domain.dns_check_requested']

function humanAction(action: string): string {
  return action
    .split(/[._]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/** Logs: the platform-wide audit trail (admin = all organizations). */
export function LogsPage() {
  const [logs, setLogs] = useState<AdminAuditRow[] | null>(null)
  const [query, setQuery] = useState('')

  useEffect(() => {
    // No organization_id param: platform admins see the fleet-wide trail.
    api.get<{ logs: AdminAuditRow[] }>('/v1/audit-logs?limit=200')
      .then((r) => setLogs(r.logs ?? []))
      .catch(() => setLogs([]))
  }, [])

  const visible = (logs ?? []).filter((a) => {
    if (NOISE_PREFIXES.some((p) => a.action.startsWith(p))) return false
    if (!query) return true
    const q = query.toLowerCase()
    return `${a.actor_email ?? ''} ${humanAction(a.action)} ${a.resource_type} ${a.resource_id}`.toLowerCase().includes(q)
  })

  const exportCsv = () => {
    const header = 'time,actor,action,resource_type,resource_id,result,ip'
    const lines = visible.map((a) =>
      [a.created_at, a.actor_email ?? a.actor_type, a.action, a.resource_type, a.resource_id, a.result, a.ip].join(','))
    const blob = new Blob([[header, ...lines].join('\n')], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'audit-log.csv'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Activity log"
        subtitle="A clear audit trail for important administrative actions."
        actions={<button className="btn-ghost" onClick={exportCsv}><Download size={13} /> Export</button>}
      />
      <Card className="overflow-hidden !p-0">
        <Toolbar>
          <ToolbarSearch value={query} onChange={setQuery} placeholder="Search actor, action or resource..." />
        </Toolbar>
        {logs === null ? (
          <div className="p-5"><SkeletonRows rows={6} height="h-12" /></div>
        ) : visible.length === 0 ? (
          <EmptyState icon={<ScrollText size={20} />} title="Nothing yet" subtitle="Administrative actions appear here as they happen." />
        ) : (
          <div className="divide-y divide-line">
            {visible.slice(0, 150).map((a) => (
              <div key={a.id} className="flex items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                <Initials text={a.actor_email ?? 'System'} size={30} rounded="rounded-full" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[11px] text-ink">
                    <span className="font-bold text-brand">{a.actor_email ?? 'System'}</span>
                    {' '}{humanAction(a.action)}
                  </div>
                  <div className="truncate text-[9.5px] text-muted">
                    {a.resource_type}{a.resource_id ? ` · ${a.resource_id.slice(0, 8)}` : ''}{a.ip ? ` · ${a.ip}` : ''}
                  </div>
                </div>
                {a.result === 'failure' && <span className="status-chip status-down">Failed</span>}
                <span className="shrink-0 text-[10px] text-muted">{timeAgo(a.created_at)}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}
