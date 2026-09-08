import { useEffect, useState } from 'react'
import { Users, ShieldAlert, Search } from 'lucide-react'
import { Card, EmptyState, SkeletonRows, StatusBadge, PageTitle, Initials, StatCard } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminOrg, AdminUser } from '../adminview'

/** Customers: cross-organization management (orgs + their platform users). */
export function CustomersPage() {
  const [orgs, setOrgs] = useState<AdminOrg[] | null>(null)
  const [users, setUsers] = useState<AdminUser[] | null>(null)
  const [security, setSecurity] = useState<{ service_accounts_active: number; api_tokens_active: number } | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    Promise.all([adminview.organizations(), adminview.users()])
      .then(([o, u]) => {
        setOrgs(o.organizations ?? [])
        setUsers(u.users ?? [])
        setSecurity(u.security)
      })
      .catch((ex) => setErr(ex.message ?? 'Failed to load customers'))
  }, [])

  if (err) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <PageTitle title="Customers" subtitle="Cross-organization management." />
        <Card><EmptyState icon={<ShieldAlert size={20} />} title="Could not load customers" subtitle={err} /></Card>
      </div>
    )
  }

  const orgColumns: Column<AdminOrg>[] = [
    {
      key: 'org', header: 'Organization', render: (r) => (
        <div>
          <div className="table-primary">{r.name}</div>
          <div className="table-secondary">{r.slug}</div>
        </div>
      ),
      filter: (r) => `${r.name} ${r.slug}`,
    },
    { key: 'plan', header: 'Plan', render: (r) => r.plan ?? 'Unassigned' },
    { key: 'websites', header: 'Sites', render: (r) => r.websites },
    { key: 'databases', header: 'Databases', render: (r) => r.databases },
    { key: 'domains', header: 'Domains', render: (r) => r.domains },
    {
      key: 'owners', header: 'Members', render: (r) => (
        <span className="block max-w-[220px] truncate text-[9.5px] text-muted" title={r.owners}>{r.owners || '—'}</span>
      ),
    },
    { key: 'created', header: 'Created', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  const userColumns: Column<AdminUser>[] = [
    {
      key: 'user', header: 'User', render: (r) => (
        <div className="flex items-center gap-2.5">
          <Initials text={r.name || r.email} rounded="rounded-full" />
          <div>
            <div className="table-primary">{r.name || '—'}</div>
            <div className="table-secondary">{r.email}</div>
          </div>
        </div>
      ),
      filter: (r) => `${r.name} ${r.email}`,
    },
    { key: 'platform', header: 'Role', render: (r) => r.is_platform_admin ? <span className="badge-warn">Platform admin</span> : <span className="badge-neutral">Customer</span> },
    { key: 'mfa', header: '2FA', render: (r) => r.mfa_enabled ? <span className="badge-ok">On</span> : <span className="badge-neutral">Off</span> },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    { key: 'orgs', header: 'Organizations', render: (r) => <span className="block max-w-[220px] truncate text-[9.5px] text-muted" title={r.orgs}>{r.orgs || '—'}</span> },
    { key: 'created', header: 'Joined', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Customers" subtitle="Organizations and the users behind them." />
      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Organizations" value={orgs?.length ?? ''} loading={!orgs} icon={<Users size={15} strokeWidth={1.8} />} />
        <StatCard label="Platform users" value={users?.length ?? ''} loading={!users} icon={<Users size={15} strokeWidth={1.8} />} />
        <StatCard label="Service accounts" value={security?.service_accounts_active ?? ''} tone="purple" loading={!security} icon={<ShieldAlert size={15} strokeWidth={1.8} />} />
        <StatCard label="API tokens" value={security?.api_tokens_active ?? ''} tone="amber" loading={!security} icon={<ShieldAlert size={15} strokeWidth={1.8} />} />
      </div>

      <div className="mb-4 text-[11px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">Organizations</div>
      <Card className="mb-5 !p-0">
        <DataTable
          columns={orgColumns}
          rows={orgs}
          rowKey={(r) => r.id}
          minWidth={820}
          empty={<EmptyState icon={<Users size={20} />} title="No organizations" subtitle="Organizations appear as customers sign up or are created." />}
        />
      </Card>

      <div className="mb-4 text-[11px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">Users</div>
      <Card className="!p-0">
        <DataTable
          columns={userColumns}
          rows={users}
          rowKey={(r) => r.id}
          minWidth={820}
          empty={<EmptyState icon={<Search size={20} />} title="No users" subtitle="Create accounts from the platform settings." />}
        />
      </Card>
    </div>
  )
}
