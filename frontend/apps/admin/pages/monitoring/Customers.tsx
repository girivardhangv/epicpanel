// Customers drill-down — verbatim tree branch: CPU / RAM / Disk / Bandwidth.
// Rows come from GET /v1/admin/observability/customers (per hosting account
// samples from the LiveStore). Names are resolved against the adminview
// accounts list (real data only — accounts without a row keep their ids).
import { Users, RefreshCw } from 'lucide-react'
import { fmtBytes } from '@epicpanel/core'
import { Card, EmptyState } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { adminview } from '../../adminview'
import type { AdminAccount } from '../../adminview'
import { monitoring } from './client'
import type { ObsCustomer } from './data'
import { liveState, shortId } from './data'
import { FailedNote, SectionShell, StateChip, usePolled } from './bits'
import type { UIState } from './bits'

interface CustomerRow extends ObsCustomer {
  name: string
  org_name: string
  server_name: string
}

export function CustomersSection() {
  const { data, failed, reload } = usePolled(async () => {
    const [obs, accounts] = await Promise.all([
      monitoring.obsCustomers(),
      adminview.accounts().catch(() => ({ accounts: [] as AdminAccount[] })),
    ])
    const bySite = new Map((accounts.accounts ?? []).map((a) => [a.id, a] as const))
    const rows: CustomerRow[] = (obs.customers ?? []).map((c) => ({
      ...c,
      name: bySite.get(c.website_id)?.name ?? '',
      org_name: bySite.get(c.website_id)?.org_name ?? '',
      server_name: bySite.get(c.website_id)?.server_name ?? '',
    }))
    return rows
  }, [])

  const rows = data

  const columns: Column<CustomerRow>[] = [
    {
      key: 'customer',
      header: 'Account',
      render: (r) => (
        <div className="max-w-[220px]">
          <div className="table-primary truncate">{r.name || <span className="font-mono">{shortId(r.website_id)}</span>}</div>
          <div className="table-secondary">{r.org_name || 'unknown organization'}</div>
        </div>
      ),
      filter: (r) => `${r.name} ${r.org_name}`,
    },
    {
      key: 'node',
      header: 'Node',
      render: (r) => (
        <div className="max-w-[160px]">
          <div className="table-primary truncate">{r.server_name || <span className="font-mono">{shortId(r.server_id)}</span>}</div>
        </div>
      ),
      filter: (r) => r.server_name,
    },
    { key: 'state', header: 'Health', render: (r) => <StateChip state={liveState(r.state) as UIState} /> },
    {
      key: 'cpu',
      header: 'CPU',
      render: (r) => <PctCell pct={r.cpu_percent} />,
    },
    { key: 'ram', header: 'RAM', render: (r) => <PctCell pct={r.ram_percent} note={r.ram_percent <= 0 ? 'no memory limit reported' : undefined} /> },
    {
      key: 'disk',
      header: 'Disk',
      render: (r) => (
        <span className="font-semibold text-ink">{r.disk_used_mb > 0 ? fmtBytes(r.disk_used_mb * 1024 * 1024) : '—'}</span>
      ),
    },
    {
      key: 'bandwidth',
      header: 'Bandwidth',
      render: (r) => <span className="font-semibold text-ink">{r.bandwidth_bps > 0 ? `${fmtBytes(r.bandwidth_bps)}/s` : '—'}</span>,
    },
  ]

  return (
    <SectionShell
      crumb="Customers"
      title="Customers"
      subtitle="Per-account CPU, RAM, Disk and Bandwidth sampled from the live stream."
      actions={
        <button className="btn-ghost" onClick={reload} aria-label="Refresh">
          <RefreshCw size={15} /> Refresh
        </button>
      }
    >
      {failed && <FailedNote what="Customer observability" onRetry={reload} />}
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => `${r.server_id}:${r.website_id}`}
          searchText={(r) => `${r.name} ${r.org_name} ${r.server_name} ${r.website_id}`}
          loading={rows === null}
          minWidth={820}
          empty={
            failed ? (
              <EmptyState icon={<Users size={20} />} title="Customer observability unavailable" subtitle="The request failed. Use refresh to retry." />
            ) : (
              <EmptyState icon={<Users size={20} />} title="No account samples" subtitle="Account metrics appear when hosted sites report through a connected node." />
            )
          }
        />
      </Card>
    </SectionShell>
  )
}

function PctCell({ pct, note }: { pct: number; note?: string }) {
  const cls = pct >= 90 ? 'text-danger' : pct >= 75 ? 'text-warn' : 'text-ink'
  return <span className={`font-semibold ${cls}`} title={note}>{pct > 0 ? `${Math.round(pct)}%` : '0%'}</span>
}
