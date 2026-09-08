import { useEffect, useState } from 'react'
import { Database } from 'lucide-react'
import { Card, EmptyState, PageTitle, StatusBadge } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminDatabase } from '../adminview'

/** Databases: fleet-wide database inventory (credentials are never exposed). */
export function DatabasesPage() {
  const [rows, setRows] = useState<AdminDatabase[] | null>(null)

  useEffect(() => {
    adminview.databases().then((r) => setRows(r.databases ?? [])).catch(() => setRows([]))
  }, [])

  const columns: Column<AdminDatabase>[] = [
    {
      key: 'name', header: 'Database', render: (r) => (
        <div>
          <div className="table-primary font-mono">{r.name}</div>
          <div className="table-secondary font-mono">{r.db_user}</div>
        </div>
      ),
      filter: (r) => `${r.name} ${r.db_user}`,
    },
    { key: 'engine', header: 'Engine', render: (r) => <span className="badge-neutral capitalize">{r.engine}</span> },
    { key: 'org', header: 'Customer', render: (r) => <span className="text-sub">{r.org_name}</span>, filter: (r) => r.org_name },
    {
      key: 'site', header: 'Account', render: (r) => (
        <div>
          <div className="table-primary">{r.website_name ?? '—'}</div>
          <div className="table-secondary">{r.server_name}</div>
        </div>
      ),
    },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    { key: 'created', header: 'Created', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Databases" subtitle="Databases across all customers and nodes. Credentials are never shown." />
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.name} ${r.db_user} ${r.org_name} ${r.server_name} ${r.website_name ?? ''}`}
          minWidth={820}
          empty={<EmptyState icon={<Database size={20} />} title="No databases" subtitle="Customer databases appear here as they are created." />}
        />
      </Card>
    </div>
  )
}
