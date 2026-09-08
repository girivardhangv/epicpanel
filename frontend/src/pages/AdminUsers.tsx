import { useEffect, useState } from 'react'
import { UserRound, Plus, ShieldCheck } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote } from '@/components/ui'
import type { User } from '@/lib/types'
import { timeAgo } from '@/lib/types'

// Platform-admin account management (cPanel operator model): the hosting
// operator creates customer accounts; customers never self-register privileges.
export function AdminUsersPage() {
  const { user } = useAuth()
  const [users, setUsers] = useState<User[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', email: '', password: '' })
  const isAdmin = !!user?.is_platform_admin

  const load = async () => {
    const r = await api.get<{ users: User[] }>('/v1/admin/users')
    setUsers(r.users ?? [])
  }

  useEffect(() => {
    if (isAdmin) void load()
  }, [isAdmin]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post('/v1/admin/users', form)
      setShow(false)
      setForm({ name: '', email: '', password: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create user')
    } finally {
      setBusy(false)
    }
  }

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <h1 className="text-[23px] font-bold tracking-[-.025em] text-ink">Users</h1>
        <Card className="mt-4">
          <EmptyState
            icon={<ShieldCheck size={22} />}
            title="Administrator-only"
            subtitle="Platform administrators manage customer accounts here."
          />
        </Card>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Platform Users</h1>
          <p className="mt-[5px] text-[12px] text-muted">Customer accounts you operate. Create accounts, then add them to organizations.</p>
        </div>
        <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Create user</button>
      </div>
      <Card className="overflow-hidden !p-0">
        {users === null ? (
          <SkeletonRows rows={3} />
        ) : users.length === 0 ? (
          <EmptyState icon={<UserRound size={22} />} title="No users" subtitle="Create customer accounts here." />
        ) : (
          <div className="divide-y divide-line">
            {users.map((u) => (
              <div key={u.id} className="flex items-center gap-3 px-4 py-3.5 transition hover:bg-surface-2">
                <div className="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-full bg-brand-soft text-[12px] font-extrabold text-brand">
                  {u.name.charAt(0).toUpperCase()}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-[11.5px] font-bold text-[#243047]">{u.name}</span>
                    {u.is_platform_admin && <span className="status-chip status-warning">admin</span>}
                  </div>
                  <div className="truncate text-[9.5px] text-muted">{u.email} · joined {timeAgo(u.created_at)}</div>
                </div>
                <span className={u.status === 'active' ? 'status-chip status-live' : 'status-chip'}>{u.status}</span>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Create Customer Account" subtitle="Customers sign in with the credentials you share.">
        <ErrorNote message={err} />
        <Field label="Full name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Customer Name" /></Field>
        <Field label="Email"><input className="input" type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} placeholder="customer@example.com" /></Field>
        <Field label="Password" hint="At least 10 characters. Share it with the customer securely.">
          <input className="input" type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.email || !form.name}>
          {busy ? 'Creating...' : 'Create Account'}
        </button>
      </Modal>
    </div>
  )
}
