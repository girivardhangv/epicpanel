import { useEffect, useMemo, useState } from 'react'
import { Activity, ShieldAlert, CircleCheck } from 'lucide-react'
import { Card, CardHeader, EmptyState, PageTitle, StatCard, SkeletonRows } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminAlert, Overview } from '../adminview'

/**
 * Monitoring: platform alert feed + node signals. The rule engine, hysteresis
 * and drill-down tree land with Phase 13 — this feed is the seam it inherits.
 */
export function MonitoringPage() {
  const [alerts, setAlerts] = useState<AdminAlert[] | null>(null)
  const [showResolved, setShowResolved] = useState(false)
  const [ov, setOv] = useState<Overview | null>(null)

  useEffect(() => {
    adminview.alerts(showResolved).then((r) => setAlerts(r.alerts ?? [])).catch(() => setAlerts([]))
  }, [showResolved])

  useEffect(() => {
    adminview.overview().then(setOv).catch(() => undefined)
  }, [])

  const open = useMemo(() => (alerts ?? []).filter((a) => !a.resolved_at), [alerts])

  const columns: Column<AdminAlert>[] = [
    {
      key: 'alert', header: 'Alert', render: (r) => (
        <div>
          <div className="table-primary">{r.message}</div>
          <div className="table-secondary font-mono">{r.type}{r.resource_name ? ` · ${r.resource_name}` : ''}</div>
        </div>
      ),
      filter: (r) => `${r.message} ${r.type} ${r.resource_name}`,
    },
    { key: 'org', header: 'Customer', render: (r) => <span className="text-sub">{r.org_name || 'Platform'}</span>, filter: (r) => r.org_name },
    {
      key: 'severity', header: 'Severity', render: (r) => (
        <span className={`status-chip ${r.severity === 'critical' ? 'status-down' : r.severity === 'warning' ? 'status-warning' : 'status-live'}`}>
          {r.severity}
        </span>
      ),
    },
    { key: 'created', header: 'Raised', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
    {
      key: 'resolved', header: 'State', render: (r) =>
        r.resolved_at ? <span className="badge-ok">Resolved</span> : <span className="badge-off">Open</span>,
    },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Monitoring" subtitle="Alert feed and platform signals." />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Open alerts" value={ov ? ov.alerts.unresolved : ''} tone={ov && ov.alerts.unresolved > 0 ? 'red' : 'green'} loading={!ov} icon={<ShieldAlert size={15} strokeWidth={1.8} />} />
        <StatCard label="Nodes online" value={ov ? `${ov.nodes.online}/${ov.nodes.total}` : ''} tone="green" loading={!ov} icon={<Activity size={15} strokeWidth={1.8} />} />
        <StatCard label="Nodes stale" value={ov ? ov.nodes.stale : ''} tone={ov && ov.nodes.stale > 0 ? 'amber' : 'blue'} loading={!ov} icon={<Activity size={15} strokeWidth={1.8} />} />
        <StatCard label="Nodes offline" value={ov ? ov.nodes.offline : ''} tone={ov && ov.nodes.offline > 0 ? 'red' : 'blue'} loading={!ov} icon={<Activity size={15} strokeWidth={1.8} />} />
      </div>

      {ov && ov.alerts.unresolved === 0 && (
        <Card className="mb-4">
          <EmptyState
            icon={<CircleCheck size={20} />}
            title="No open alerts"
            subtitle="Rule-based alerting (thresholds, state and time rules) lands with Phase 13; this feed stays the entry point."
          />
        </Card>
      )}

      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={alerts}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.message} ${r.type} ${r.org_name} ${r.resource_name}`}
          filterSlot={
            <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={showResolved ? 'all' : 'open'} onChange={(e) => setShowResolved(e.target.value === 'all')}>
              <option value="open">Open</option>
              <option value="all">All</option>
            </select>
          }
          minWidth={820}
          empty={
            <EmptyState
              icon={<ShieldAlert size={20} />}
              title={showResolved ? 'No alerts recorded' : 'No open alerts'}
              subtitle={showResolved ? 'Alert history appears as rules fire.' : 'Open alerts land here the moment rules trip.'}
            />
          }
        />
      </Card>
    </div>
  )
}
