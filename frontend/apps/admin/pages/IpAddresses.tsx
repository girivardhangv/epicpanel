import { useEffect, useState } from 'react'
import { HardDrive, ShieldAlert } from 'lucide-react'
import { Card, CardHeader, EmptyState, PageTitle, SkeletonRows, StatCard } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { adminview } from '../adminview'
import type { PortPool } from '../adminview'

interface PortsPayload {
  apache: PortPool
  ols: PortPool
  allocations: { server: string; port: number; website: string; org: string; status: string }[]
  nodes: { name: string; hostname: string; status: string }[]
  note: string
}

/**
 * IP Addresses: the honest audit of what exists today. `internal/ports`
 * manages per-node BACKEND port ranges for proxy-mode web servers; there is
 * no dedicated-IP pool table in the platform. This view shows port-pool
 * utilization, per-node allocations and enrolled hostnames, and says so.
 */
export function IpAddressesPage() {
  const [data, setData] = useState<PortsPayload | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    adminview.ports().then(setData).catch((ex) => setErr(ex.message ?? 'Failed to load port pools'))
  }, [])

  const columns: Column<PortsPayload['allocations'][number]>[] = [
    { key: 'server', header: 'Node', render: (r) => <span className="table-primary">{r.server}</span>, filter: (r) => r.server },
    { key: 'port', header: 'Backend port', render: (r) => <span className="font-mono">{r.port}</span> },
    { key: 'site', header: 'Account', render: (r) => r.website, filter: (r) => r.website },
    { key: 'org', header: 'Customer', render: (r) => <span className="text-sub">{r.org}</span>, filter: (r) => r.org },
    { key: 'status', header: 'Site status', render: (r) => <span className="badge-neutral capitalize">{r.status}</span> },
  ]

  const pool = (p: PortPool | undefined, label: string) => (
    <StatCard
      label={label}
      value={p ? `${p.allocated}/${p.total}` : ''}
      change={p ? `range ${p.range}` : undefined}
      up={false}
      tone={p && p.allocated / Math.max(1, p.total) > 0.8 ? 'amber' : 'blue'}
      loading={!data}
      icon={<HardDrive size={15} strokeWidth={1.8} />}
    />
  )

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="IP Addresses" subtitle="Addressing and port allocation across the fleet." />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        {pool(data?.apache, 'Apache pool')}
        {pool(data?.ols, 'OpenLiteSpeed pool')}
        <StatCard label="Nodes enrolled" value={data?.nodes.length ?? ''} loading={!data} icon={<HardDrive size={15} strokeWidth={1.8} />} />
        <StatCard label="Allocations" value={data?.allocations.length ?? ''} loading={!data} icon={<HardDrive size={15} strokeWidth={1.8} />} />
      </div>

      {err && <Card className="mb-4"><EmptyState icon={<ShieldAlert size={20} />} title="Unavailable" subtitle={err} /></Card>}

      <Card className="mb-4">
        <CardHeader title="Scope note" subtitle="What the platform manages today" />
        <p className="text-[11.5px] leading-relaxed text-sub">{data?.note ?? 'Loading...'}</p>
        <p className="mt-2 text-[11px] text-muted">
          A dedicated-IP pool (shared vs. dedicated addressing per account) is not implemented; when it
          lands it plugs into the same allocation seam the agent uses for port verification.
        </p>
      </Card>

      <Card className="mb-4 !p-0">
        <DataTable
          columns={columns}
          rows={data?.allocations ?? null}
          rowKey={(r) => `${r.server}-${r.port}-${r.website}`}
          searchText={(r) => `${r.server} ${r.website} ${r.org}`}
          minWidth={760}
          empty={<EmptyState icon={<HardDrive size={20} />} title="No backend port allocations" subtitle="Sites on proxy-mode web servers reserve backend ports automatically." />}
        />
      </Card>

      <Card>
        <CardHeader title="Node hostnames" subtitle="Enrolled addressing per node" />
        {!data ? (
          <SkeletonRows rows={2} height="h-9" />
        ) : data.nodes.length === 0 ? (
          <EmptyState title="No nodes" subtitle="Enrolled nodes appear here with their hostnames." />
        ) : (
          <div className="divide-y divide-line">
            {data.nodes.map((n) => (
              <div key={n.name} className="flex items-center gap-3 py-2.5">
                <span className={`h-1.5 w-1.5 rounded-full ${n.status === 'online' ? 'bg-ok' : 'bg-danger'}`} />
                <span className="min-w-0 flex-1 truncate text-[11px] font-bold text-ink">{n.name}</span>
                <span className="truncate font-mono text-[10px] text-muted">{n.hostname || 'hostname pending'}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}
