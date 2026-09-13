import { useCallback, useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, KeyRound, Plus, Trash2, Loader2 } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { timeAgo } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface SSHKey {
  id: string
  name: string
  public_key: string
  fingerprint: string
  added_at: string
}

export function SSHKeysPage() {
  const { org } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const [keys, setKeys] = useState<SSHKey[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [removingId, setRemovingId] = useState('')
  const [form, setForm] = useState({ name: '', public_key: '' })
  const base = `/v1/organizations/${org?.id}/websites/${websiteId}/ssh-keys`

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ ssh_keys: SSHKey[] }>(base)
      setKeys(r.ssh_keys ?? [])
    } catch {
      setKeys([])
    }
  }, [base])

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post(base, form)
      setShow(false)
      setForm({ name: '', public_key: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (k: SSHKey) => {
    if (!(await confirmAction({ title: 'Remove SSH Key', message: `Remove key "${k.name}"? SSH access with it stops immediately.`, confirmLabel: 'Remove Key' }))) return
    setRemovingId(k.id)
    try {
      await api.del(`/v1/organizations/${org?.id}/ssh-keys/${k.id}`)
      await load()
    } finally {
      setRemovingId('')
    }
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <Link to={`/sites/${websiteId}`} className="icon-btn !h-[34px] !w-[34px]" title="Back" aria-label="Back to site"><ArrowLeft size={15} /></Link>
          <div>
            <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">SSH Keys</h1>
            <p className="mt-[5px] text-[12px] text-muted">
              Key-based SSH as this site's user. Connect with: <code className="rounded-[6px] bg-surface-2 px-1.5 py-0.5 text-[10.5px]">ssh &lt;site-user&gt;@&lt;server&gt;</code>
            </p>
          </div>
        </div>
        <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add key</button>
      </div>

      <Card className="overflow-hidden !p-0">
        {keys === null ? (
          <SkeletonRows rows={2} />
        ) : keys.length === 0 ? (
          <EmptyState
            icon={<KeyRound size={22} />}
            title="No SSH keys"
            subtitle="Add your public key to access this site over SSH (SFTP works with the same key)."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={16} /> Add Key</button>}
          />
        ) : (
          <div className="divide-y divide-line">
            {keys.map((k) => (
              <div key={k.id} className="flex items-center gap-3 px-4 py-3.5 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <KeyRound size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[11px] font-bold text-[#243047]">{k.name}</div>
                  <div className="truncate font-mono text-[10px] text-muted">{k.fingerprint}</div>
                </div>
                <span className="shrink-0 text-[10px] text-muted">added {timeAgo(k.added_at)}</span>
                <button
                  className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                  onClick={() => remove(k)}
                  disabled={removingId === k.id}
                  title="Remove"
                  aria-label={`Remove key ${k.name}`}
                >
                  {removingId === k.id ? <Loader2 size={13} className="animate-spin" /> : <Trash2 size={13} />}
                </button>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add SSH Public Key" subtitle="SFTP works with the same key." width="max-w-lg">
        <ErrorNote message={err} />
        <Field label="Key name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="my-laptop" /></Field>
        <Field label="Public key" hint="OpenSSH format: ssh-ed25519 AAAA... comment">
          <textarea className="input h-28 font-mono text-[12px]" value={form.public_key} onChange={(e) => setForm({ ...form, public_key: e.target.value })} placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... user@host" />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.name || !form.public_key}>
          {busy ? 'Adding...' : 'Add Key'}
        </button>
      </Modal>
    </div>
  )
}
