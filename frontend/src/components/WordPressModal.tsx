import { useState } from 'react'
import { api } from '@/lib/api'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { Globe } from 'lucide-react'

export function WordPressModal({ open, onClose, orgId, websiteId, siteName, domain, onQueued }: {
  open: boolean
  onClose: () => void
  orgId: string
  websiteId: string
  siteName: string
  domain?: string
  onQueued?: () => void
}) {
  const [form, setForm] = useState({ title: 'My WordPress Site', admin_user: 'admin', admin_email: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${orgId}/websites/${websiteId}/wordpress`, {
        ...form,
        title: form.title || 'My WordPress Site',
      })
      onClose()
      onQueued?.()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Install WordPress" width="max-w-md">
      <ErrorNote message={err} />
      <p className="mb-4 rounded border border-brand/20 bg-brand/[0.04] px-3.5 py-2.5 text-[13px] leading-relaxed text-sub">
        Creates a fresh MariaDB database and installs the latest WordPress into <b>{siteName}</b>'s web root via wp-cli.
        The admin password is shown in <b>Activity</b> when installation completes.
      </p>
      <Field label="Site title">
        <input className="input" value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} placeholder="My WordPress Site" />
      </Field>
      <Field label="Admin username">
        <input className="input" value={form.admin_user} onChange={(e) => setForm({ ...form, admin_user: e.target.value })} />
      </Field>
      <Field label="Admin email">
        <input className="input" type="email" value={form.admin_email} onChange={(e) => setForm({ ...form, admin_email: e.target.value })} placeholder="admin@example.com" />
      </Field>
      {domain && (
        <p className="mb-4 flex items-center gap-2 text-[12.5px] text-muted">
          <Globe size={14} /> Will be served at http://{domain}
        </p>
      )}
      <button className="btn-brand w-full justify-center" onClick={submit} disabled={busy || !form.admin_email}>
        {busy ? 'Installing...' : 'Install WordPress'}
      </button>
    </Modal>
  )
}
