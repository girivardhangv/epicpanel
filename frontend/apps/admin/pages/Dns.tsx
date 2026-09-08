import { useEffect, useState } from 'react'
import { Globe } from 'lucide-react'
import { Card, EmptyState, PageTitle, StatusBadge } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminZone } from '../adminview'

/** DNS: authoritative zones across the fleet (record edits stay per-site in cPanel). */
export function DnsPage() {
  const [rows, setRows] = useState<AdminZone[] | null>(null)

  useEffect(() => {
    adminview.zones().then((r) => setRows(r.zones ?? [])).catch(() => setRows([]))
  }, [])

  const columns: Column<AdminZone>[] = [
    {
      key: 'zone', header: 'Zone', render: (r) => (
        <div>
          <div className="table-primary">{r.domain}</div>
          <div className="table-secondary">{r.website_name}</div>
        </div>
      ),
      filter: (r) => `${r.domain} ${r.website_name}`,
    },
    { key: 'org', header: 'Customer', render: (r) => <span className="text-sub">{r.org_name}</span>, filter: (r) => r.org_name },
    { key: 'records', header: 'Records', render: (r) => r.records },
    { key: 'serial', header: 'SOA serial', render: (r) => <span className="font-mono text-[10px]">{r.serial}</span> },
    { key: 'ttl', header: 'TTL', render: (r) => <span className="text-muted">{r.ttl}s</span> },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    { key: 'updated', header: 'Updated', render: (r) => <span className="text-muted">{timeAgo(r.updated_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="DNS" subtitle="Authoritative zones across all customers. Record editing lives in each site's cPanel." />
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.domain} ${r.org_name} ${r.website_name}`}
          minWidth={820}
          empty={<EmptyState icon={<Globe size={20} />} title="No zones" subtitle="Zones are created per website from the customer panel." />}
        />
      </Card>
    </div>
  )
}
