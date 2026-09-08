import { useEffect, useState } from 'react'
import { Network } from 'lucide-react'
import { Card, EmptyState, PageTitle, StatusBadge, Initials } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminDomain } from '../adminview'

function sslChip(d: AdminDomain) {
  if (d.ssl_mode === 'none') return <span className="badge-neutral">No SSL</span>
  if (d.ssl_mode === 'selfsigned') return <span className="badge-warn">Self-signed</span>
  if (d.ssl_state === 'active') {
    const exp = d.ssl_expires_at ? new Date(d.ssl_expires_at).getTime() : 0
    const days = exp ? Math.floor((exp - Date.now()) / 86400000) : null
    if (days != null && days <= 14) return <span className="badge-warn">Expiring {days}d</span>
    return <span className="badge-ok">Valid{days != null ? ` ${days}d` : ''}</span>
  }
  return <StatusBadge status={d.ssl_state} />
}

/** Domains: fleet-wide domain assignments with SSL status and renewals. */
export function DomainsPage() {
  const [rows, setRows] = useState<AdminDomain[] | null>(null)

  useEffect(() => {
    adminview.domains().then((r) => setRows(r.domains ?? [])).catch(() => setRows([]))
  }, [])

  const columns: Column<AdminDomain>[] = [
    {
      key: 'domain', header: 'Domain', render: (r) => (
        <div>
          <div className="table-primary">{r.domain}</div>
          <div className="table-secondary capitalize">{r.kind}</div>
        </div>
      ),
      filter: (r) => r.domain,
    },
    {
      key: 'owner', header: 'Customer', render: (r) => (
        <div className="flex items-center gap-2">
          <Initials text={r.org_name} size={24} />
          <span className="text-sub">{r.org_name}</span>
        </div>
      ),
      filter: (r) => r.org_name,
    },
    {
      key: 'site', header: 'Account', render: (r) => (
        <div>
          <div className="table-primary">{r.website_name}</div>
          <div className="table-secondary">{r.server_name}</div>
        </div>
      ),
    },
    { key: 'ssl', header: 'SSL', render: (r) => sslChip(r) },
    {
      key: 'renewal', header: 'Expires', render: (r) => (
        <span className="text-muted">{r.ssl_expires_at ? new Date(r.ssl_expires_at).toLocaleDateString() : '—'}</span>
      ),
    },
    { key: 'created', header: 'Added', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Domains" subtitle="Assignments, SSL status and renewals across customers." />
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.domain} ${r.org_name} ${r.website_name} ${r.server_name}`}
          filterSlot={
            <select className="input h-[34px] w-auto cursor-pointer text-[11px]" defaultValue="">
              <option value="" disabled>All SSL states</option>
            </select>
          }
          minWidth={860}
          empty={<EmptyState icon={<Network size={20} />} title="No domains" subtitle="Domains appear as customers add them." />}
        />
      </Card>
    </div>
  )
}
