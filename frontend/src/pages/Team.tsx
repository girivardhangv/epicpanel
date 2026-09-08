import { useEffect, useState } from 'react'
import { UserRound, Plus, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import type { Member } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

export function TeamPage() {
  const { org, user, myRole } = useAuth()
  const [members, setMembers] = useState<Member[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ email: '', role: 'developer' })
  // Org owners/admins manage the team; platform admins manage everything.
  const canManage = !!user?.is_platform_admin || myRole === 'owner' || myRole === 'admin'

  const load = async () => {
    if (!org) return
    const r = await api.get<{ members: Member[] }>(`/v1/organizations/${org.id}/members`)
    setMembers(r.members ?? [])
  }

  useEffect(() => {
    if (org) void load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const add = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/members`, form)
      setShow(false)
      setForm({ email: '', role: 'developer' })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to add member')
    } finally {
      setBusy(false)
    }
  }

  const remove = async (m: Member) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Remove Member', message: `Remove ${m.email} from this organization?`, confirmLabel: 'Remove' }))) return
    await api.del(`/v1/organizations/${org.id}/members/${m.user_id}`)
    await load()
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Users</h1>
          <p className="mt-[5px] text-[12px] text-muted">People with access to {org?.name}.</p>
        </div>
        {canManage && <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add member</button>}
      </div>
      <Card className="overflow-hidden !p-0">
        {members === null ? (
          <div className="p-5"><SkeletonRows rows={2} /></div>
        ) : members.length === 0 ? (
          <EmptyState icon={<UserRound size={22} />} title="No members" subtitle="Invite teammates to collaborate." />
        ) : (
          <div className="divide-y divide-line">
            {members.map((m) => (
              <div key={m.user_id} className="flex items-center gap-3 px-4 py-3.5 transition hover:bg-surface-2">
                <div className="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-full bg-brand-soft text-[12px] font-extrabold text-brand">
                  {m.name.charAt(0).toUpperCase()}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[11.5px] font-bold text-[#243047]">{m.name}</div>
                  <div className="truncate text-[9.5px] text-muted">{m.email}</div>
                </div>
                <span className="status-chip">{m.role}</span>
                {canManage && m.role !== 'owner' && (
                  <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => remove(m)} title="Remove member">
                    <Trash2 size={13} />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add Member">
        <ErrorNote message={err} />
        <Field label="Email" hint="The user must already have an EpicHost account.">
          <input className="input" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} placeholder="teammate@company.com" />
        </Field>
        <Field label="Role">
          <Select
            value={form.role}
            onChange={(v) => setForm({ ...form, role: v })}
            options={[
              { value: 'admin', label: 'Administrator' },
              { value: 'developer', label: 'Developer' },
              { value: 'billing', label: 'Billing' },
              { value: 'support', label: 'Support' },
            ]}
          />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={add} disabled={busy || !form.email}>
          {busy ? 'Adding...' : 'Add Member'}
        </button>
      </Modal>
    </div>
  )
}
