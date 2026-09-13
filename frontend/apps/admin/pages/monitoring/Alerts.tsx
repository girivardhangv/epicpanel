// Phase 13 alert feed: state tabs (active | ack | resolved | all) with
// acknowledge/resolve lifecycle actions. WS-driven: the hub broadcasts
// alert.raised / alert.resolved bus events to every platform-admin client;
// the feed refetches when one lands (plus a slow safety interval).
import { useCallback, useEffect, useState } from 'react'
import { CircleCheck, CheckCheck, Flag, ShieldAlert } from 'lucide-react'
import { Card, EmptyState, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { subscribe, timeAgo } from '@epicpanel/core'
import { monitoring } from './client'
import { shortId } from './data'
import type { AlertRow, AlertState } from './data'
import { AlertMetaLine, FailedNote, SectionShell, SeverityChip } from './bits'

const TABS: { key: AlertState; label: string }[] = [
  { key: 'active', label: 'Active' },
  { key: 'ack', label: 'Acknowledged' },
  { key: 'resolved', label: 'Resolved' },
  { key: 'all', label: 'All' },
]

export function AlertFeed() {
  const [state, setState] = useState<AlertState>('active')
  const [rows, setRows] = useState<AlertRow[] | null>(null)
  const [failed, setFailed] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)

  const load = useCallback(() => {
    monitoring
      .alerts(state)
      .then((r) => {
        setRows(r.alerts ?? [])
        setFailed(false)
      })
      .catch(() => setFailed(true))
  }, [state])

  useEffect(() => {
    setRows(null)
    load()
  }, [load])

  // Live updates: alert.raised / alert.resolved arrive on the platform WS.
  useEffect(
    () =>
      subscribe((msg) => {
        const t = msg && typeof msg === 'object' ? (msg as { type?: string }).type : undefined
        if (t === 'alert.raised' || t === 'alert.resolved') load()
      }),
    [load],
  )

  const act = async (alert: AlertRow, kind: 'ack' | 'resolve') => {
    setBusyId(alert.id)
    try {
      if (kind === 'ack') await monitoring.ackAlert(alert.id)
      else await monitoring.resolveAlert(alert.id)
      pushToast('success', kind === 'ack' ? 'Alert acknowledged' : 'Alert resolved')
      load()
    } catch (ex) {
      pushToast('error', ex instanceof Error ? ex.message : 'Action failed')
    } finally {
      setBusyId(null)
    }
  }

  const stateChip = (r: AlertRow) => {
    if (r.resolved_at) return <span className="badge-ok"><span className="h-1.5 w-1.5 rounded-full bg-current" />Resolved</span>
    if (r.acknowledged_at) return <span className="badge-neutral"><span className="h-1.5 w-1.5 rounded-full bg-current" />Acknowledged</span>
    return <span className="badge-off"><span className="h-1.5 w-1.5 rounded-full bg-current" />Active</span>
  }

  const columns: Column<AlertRow>[] = [
    {
      key: 'alert',
      header: 'Alert',
      render: (r) => (
        <div className="max-w-[380px]">
          <div className="table-primary">{r.message}</div>
          <div className="table-secondary font-mono">{r.type}</div>
          <AlertMetaLine created={r.created_at} occurrences={r.occurrences} lastSeen={r.last_seen_at} />
        </div>
      ),
      filter: (r) => `${r.message} ${r.type} ${r.resource_name}`,
    },
    {
      key: 'subject',
      header: 'Subject',
      render: (r) => (
        <div className="max-w-[180px]">
          <div className="table-primary truncate" title={r.resource_id}>{r.resource_name || r.resource_type || '—'}</div>
          <div className="table-secondary">{r.resource_type}{r.resource_id ? ` · ${shortId(r.resource_id)}` : ''}</div>
        </div>
      ),
      filter: (r) => `${r.resource_name} ${r.resource_type} ${r.resource_id}`,
    },
    {
      key: 'org',
      header: 'Customer',
      render: (r) => (r.organization_id ? <span className="font-mono text-sub" title={r.organization_id}>{shortId(r.organization_id)}</span> : <span className="text-muted">Platform</span>),
      filter: (r) => r.organization_id ?? 'platform',
    },
    { key: 'severity', header: 'Severity', render: (r) => <SeverityChip severity={r.severity} /> },
    { key: 'created', header: 'Raised', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
    { key: 'state', header: 'State', render: stateChip },
    {
      key: 'actions',
      header: '',
      render: (r) => (
        <div className="flex justify-end gap-[5px]">
          {!r.resolved_at && !r.acknowledged_at && (
            <button className="icon-btn" title="Acknowledge" aria-label="Acknowledge" disabled={busyId === r.id} onClick={() => act(r, 'ack')}>
              <CheckCheck size={13} />
            </button>
          )}
          {!r.resolved_at && (
            <button className="icon-btn" title="Resolve" aria-label="Resolve" disabled={busyId === r.id} onClick={() => act(r, 'resolve')}>
              <Flag size={13} />
            </button>
          )}
        </div>
      ),
    },
  ]

  return (
    <Card className="!p-0">
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        searchText={(r) => `${r.message} ${r.type} ${r.resource_name} ${r.organization_id ?? ''}`}
        loading={rows === null}
        filterSlot={
          <div className="flex items-center gap-1.5">
            {TABS.map((t) => (
              <button
                key={t.key}
                onClick={() => setState(t.key)}
                className={`rounded-[8px] border px-2.5 py-[6px] text-[10.5px] font-bold transition ${
                  state === t.key ? 'border-brand bg-brand-soft text-brand' : 'border-line bg-white text-[#566278] hover:shadow-card'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        }
        minWidth={900}
        empty={
          failed ? (
            <EmptyState icon={<ShieldAlert size={20} />} title="Alert feed unavailable" subtitle="The request failed. Retry from the refresh button." />
          ) : state === 'active' ? (
            <EmptyState icon={<CircleCheck size={20} />} title="No active alerts" subtitle="Every rule is quiet. New alerts appear here the moment they trip." />
          ) : state === 'all' ? (
            <EmptyState icon={<ShieldAlert size={20} />} title="No alerts recorded" subtitle="Alert history appears as rules fire." />
          ) : (
            <EmptyState icon={<ShieldAlert size={20} />} title={`No ${TABS.find((t) => t.key === state)?.label.toLowerCase() ?? ''} alerts`} subtitle="Nothing recorded in this state yet." />
          )}
        />
    </Card>
  )
}

/** Full-page section wrapper (route: /monitoring/alerts). */
export function AlertsSection() {
  return (
    <SectionShell crumb="Alerts" title="Alerts" subtitle="Platform alert feed — deduped per rule and subject, with acknowledge and resolve lifecycle.">
      <AlertFeed />
    </SectionShell>
  )
}
