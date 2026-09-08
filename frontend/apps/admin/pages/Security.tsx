import { useEffect, useState } from 'react'
import { ShieldCheck, UserRound, KeyRound, ServerOff } from 'lucide-react'
import { api } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, PageTitle, StatCard, SkeletonRows, StatusBadge, Initials, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { Modal, Field, ErrorNote } from '@epicpanel/forms'
import { timeAgo } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminUser } from '../adminview'

/**
 * Security: platform posture — user accounts, 2FA adoption, service
 * principals. 2FA enrollment is per-user (cPanel Security page); the panel
 * never stores plaintext secrets.
 */
export function SecurityPage() {
  const [users, setUsers] = useState<AdminUser[] | null>(null)
  const [security, setSecurity] = useState<{ service_accounts_active: number; api_tokens_active: number } | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', email: '', password: '' })

  const load = async () => {
    const r = await adminview.users()
    setUsers(r.users ?? [])
    setSecurity(r.security)
  }
  useEffect(() => {
    void load()
  }, [])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post('/v1/admin/users', form)
      setShow(false)
      setForm({ name: '', email: '', password: '' })
      pushToast('success', 'User created')
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create user')
    } finally {
      setBusy(false)
    }
  }

  const mfaCount = (users ?? []).filter((u) => u.mfa_enabled).length

  const columns: Column<AdminUser>[] = [
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
    { key: 'role', header: 'Role', render: (r) => r.is_platform_admin ? <span className="badge-warn">Platform admin</span> : <span className="badge-neutral">Customer</span> },
    { key: 'mfa', header: 'Two-factor', render: (r) => r.mfa_enabled ? <span className="badge-ok">Enabled</span> : <span className="badge-off">Disabled</span> },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    { key: 'orgs', header: 'Organizations', render: (r) => <span className="block max-w-[200px] truncate text-[9.5px] text-muted" title={r.orgs}>{r.orgs || '—'}</span> },
    { key: 'created', header: 'Joined', render: (r) => <span className="text-muted">{timeAgo(r.created_at)}</span> },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Security"
        subtitle="Platform security posture."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><UserRound size={14} /> Create user</button>}
      />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Users" value={users?.length ?? ''} loading={!users} icon={<UserRound size={15} strokeWidth={1.8} />} />
        <StatCard label="2FA enabled" value={users ? `${mfaCount}/${users.length}` : ''} tone={users && mfaCount < users.length ? 'amber' : 'green'} loading={!users} icon={<ShieldCheck size={15} strokeWidth={1.8} />} />
        <StatCard label="Service accounts" value={security?.service_accounts_active ?? ''} tone="purple" loading={!security} icon={<KeyRound size={15} strokeWidth={1.8} />} />
        <StatCard label="API tokens active" value={security?.api_tokens_active ?? ''} tone="amber" loading={!security} icon={<KeyRound size={15} strokeWidth={1.8} />} />
      </div>

      <Card className="mb-4">
        <CardHeader title="Platform defaults" subtitle="Enforced server-side" />
        <div className="divide-y divide-line">
          <Note tone="green" title="Registration locked after setup" sub="cPanel model: the operator creates accounts; public register closes once first-boot setup completes." />
          <Note tone="green" title="API tokens never inherit platform-admin" sub="Admin routes require an interactive administrator session; tokens are org-confined." />
          <Note tone="amber" title="2FA is opt-in per user" sub="Enrollment lives in each user's account security page. Encourage platform admins to enable it." />
        </div>
      </Card>

      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={users}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.name} ${r.email} ${r.orgs}`}
          minWidth={860}
          empty={<EmptyState icon={<UserRound size={20} />} title="No users" subtitle="Create the first platform user." />}
        />
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Create platform user" subtitle="Customer accounts are created here, never self-serve.">
        <ErrorNote message={err} />
        <Field label="Name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>
        <Field label="Email"><input className="input" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} placeholder="user@example.com" /></Field>
        <Field label="Password" hint="Minimum 10 characters.">
          <input className="input" type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
        <div className="mt-3 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.email || !form.password}>{busy ? 'Creating...' : 'Create user'}</button>
        </div>
      </Modal>
    </div>
  )
}

function Note({ tone, title, sub }: { tone: 'green' | 'amber'; title: string; sub: string }) {
  return (
    <div className="flex items-start gap-3 py-3">
      <span className={`grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] ${tone === 'green' ? 'bg-ok-soft text-ok' : 'bg-warn-soft text-warn'}`}>
        <ServerOff size={13} />
      </span>
      <div className="min-w-0">
        <strong className="block text-[11px] text-ink">{title}</strong>
        <span className="block text-[9.5px] leading-relaxed text-muted">{sub}</span>
      </div>
    </div>
  )
}
