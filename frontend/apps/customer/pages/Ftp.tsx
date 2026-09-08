import { useCallback, useEffect, useState } from 'react'
import { KeyRound, Plus, Trash2, RotateCcw, Eye } from 'lucide-react'
import { api, ftpApi, useAuth, timeAgo } from '@epicpanel/core'
import type { FtpAccount, Website } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, ConfirmDialog } from '@epicpanel/forms'

const ALPHABET = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789'
function generatePassword(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => ALPHABET[b % ALPHABET.length]).join('') + '!7'
}

interface ConfirmState {
  title: string
  message: string
  action: () => Promise<void>
  danger?: boolean
  confirmLabel?: string
}

export function FtpPage() {
  const { org } = useAuth()
  const [sites, setSites] = useState<Website[]>([])
  const [websiteId, setWebsiteId] = useState('')
  const [accounts, setAccounts] = useState<FtpAccount[] | null>(null)
  const [err, setErr] = useState('')
  const [show, setShow] = useState(false)
  const [form, setForm] = useState({ protocol: 'ftp', label: '', home_subdir: '', password: '' })
  const [autoPassword, setAutoPassword] = useState(true)
  const [busy, setBusy] = useState(false)
  const [otp, setOtp] = useState<{ label: string; password: string; once: boolean } | null>(null)
  const [confirm, setConfirm] = useState<ConfirmState | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  useEffect(() => {
    if (!org) return
    api
      .get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) => {
        const list = (r.websites ?? []).filter((w) => w.status !== 'deleted' && w.status !== 'deleting')
        setSites(list)
        setWebsiteId((prev) => prev || list[0]?.id || '')
      })
      .catch(() => setSites([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const load = useCallback(async () => {
    if (!org || !websiteId) {
      setAccounts([])
      return
    }
    ftpApi
      .list(org.id, websiteId)
      .then((r) => setAccounts(r.ftp_accounts ?? []))
      .catch(() => setAccounts([]))
  }, [org?.id, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    if (!org || !websiteId) return
    setErr('')
    setBusy(true)
    try {
      const password = autoPassword ? generatePassword() : form.password || undefined
      const created = await ftpApi.create(org.id, websiteId, {
        protocol: form.protocol,
        label: form.label,
        home_subdir: form.home_subdir || undefined,
        password,
      })
      setShow(false)
      setForm({ protocol: 'ftp', label: '', home_subdir: '', password: '' })
      if (created?.password) {
        setOtp({ label: created.label || created.user_name, password: created.password, once: true })
      } else if (password) {
        setOtp({ label: created.label || created.user_name, password, once: false })
      }
      pushToast('success', 'FTP account created — the agent is provisioning access.')
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create the FTP account')
    } finally {
      setBusy(false)
    }
  }

  const rotate = (a: FtpAccount) => {
    if (!org) return
    setConfirm({
      title: 'Rotate password',
      message: `Generate a new password for "${a.label || a.user_name}"? The old password stops working immediately.`,
      action: async () => {
        const r = await ftpApi.rotatePassword(org.id, a.id)
        setOtp({ label: a.label || a.user_name, password: r.password, once: false })
        await load()
      },
      danger: false,
      confirmLabel: 'Rotate password',
    })
  }

  const reveal = async (a: FtpAccount) => {
    if (!org) return
    try {
      const r = await ftpApi.reveal(org.id, a.id)
      setOtp({ label: a.label || a.user_name, password: r.password, once: true })
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Reveal failed')
    }
  }

  const remove = (a: FtpAccount) => {
    if (!org) return
    setConfirm({
      title: 'Delete FTP account',
      message: `Delete "${a.label || a.user_name}" (${a.protocol.toUpperCase()})? Files are not touched — only access is removed.`,
      action: async () => {
        await ftpApi.remove(org.id, a.id)
        pushToast('success', 'FTP account deleted.')
        await load()
      },
    })
  }

  const runConfirm = async () => {
    if (!confirm) return
    setConfirmBusy(true)
    try {
      await confirm.action()
      setConfirm(null)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Action failed')
      setConfirm(null)
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="FTP Accounts"
        subtitle="Give designers or collaborators access to specific folders — no shell, ever."
        actions={<button className="btn-primary" onClick={() => setShow(true)} disabled={!websiteId}><Plus size={14} /> Add account</button>}
      />

      {sites.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <span className="text-[10px] font-extrabold uppercase tracking-[.06em] text-muted">Website</span>
          <select className="input w-[260px] cursor-pointer" value={websiteId} onChange={(e) => setWebsiteId(e.target.value)}>
            {sites.map((s) => (
              <option key={s.id} value={s.id}>{s.primary_domain || s.name}</option>
            ))}
          </select>
        </div>
      )}

      <ErrorNote message={err} />

      <Card className="overflow-hidden !p-0">
        <CardHeader
          className="mx-4 mt-4 !mb-0"
          title="Accounts"
          subtitle={sites.find((s) => s.id === websiteId) ? `${sites.find((s) => s.id === websiteId)!.primary_domain || sites.find((s) => s.id === websiteId)!.name}` : undefined}
        />
        {accounts === null ? (
          <div className="p-5"><SkeletonRows rows={2} height="h-10" /></div>
        ) : accounts.length === 0 ? (
          <EmptyState
            icon={<KeyRound size={22} />}
            title="No FTP accounts"
            subtitle="Create an account to upload files over FTP or SFTP with its own folder scope."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Add Account</button>}
          />
        ) : (
          <div className="divide-y divide-line">
            {accounts.map((a) => (
              <div key={a.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <KeyRound size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[11px] font-bold text-[#243047]">{a.label || a.user_name}</span>
                    <span className="status-chip">{a.protocol.toUpperCase()}</span>
                    <StatusBadge status={a.status} />
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[9.5px] text-muted">
                    {a.user_name} · {a.home_subdir || '/'} · created {timeAgo(a.created_at)}
                  </div>
                  {a.error_message && <div className="mt-1 text-[10px] text-danger">{a.error_message}</div>}
                </div>
                <RowActions>
                  <button className="icon-btn" title="Reveal password (one-time)" onClick={() => void reveal(a)}><Eye size={13} /></button>
                  <button className="icon-btn" title="Rotate password" onClick={() => rotate(a)}><RotateCcw size={13} /></button>
                  <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Delete" onClick={() => remove(a)}><Trash2 size={13} /></button>
                </RowActions>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add FTP Account" subtitle="Chrooted access — the account cannot leave its folder.">
        <ErrorNote message={err} />
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <Field label="Label">
            <input className="input" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} placeholder="Designer" />
          </Field>
          <Field label="Protocol">
            <Select value={form.protocol} onChange={(v) => setForm({ ...form, protocol: v })} options={[{ value: 'ftp', label: 'FTP (FTPS)' }, { value: 'sftp', label: 'SFTP (SSH file transfer)' }]} />
          </Field>
        </div>
        <Field label="Home subfolder (optional)" hint="Relative to the website root, e.g. wp-content/uploads">
          <input className="input" value={form.home_subdir} onChange={(e) => setForm({ ...form, home_subdir: e.target.value })} placeholder="public_html" />
        </Field>
        <label className="mb-3 flex items-center gap-2 text-[11.5px] font-semibold text-sub">
          <input type="checkbox" checked={autoPassword} onChange={(e) => setAutoPassword(e.target.checked)} />
          Generate a strong password for me
        </label>
        {!autoPassword && (
          <Field label="Password" hint="Minimum 10 characters.">
            <input className="input" type="text" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          </Field>
        )}
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.label}>
          {busy ? 'Creating...' : 'Create Account'}
        </button>
      </Modal>

      <Modal open={!!otp} onClose={() => setOtp(null)} title="Password" subtitle={otp?.once ? 'Shown once — copy it now.' : otp?.label}>
        {otp && (
          <div className="space-y-2.5">
            <div className="break-all rounded-[10px] border border-line bg-surface-2 p-3 font-mono text-[12.5px] font-bold text-ink">{otp.password}</div>
            <p className="text-[11px] text-muted">
              {otp.once
                ? 'The panel stores this password encrypted. This is the only time it is displayed in full.'
                : 'Store this password now — it is encrypted at rest and only revealed on demand.'}
            </p>
            <div className="flex justify-end">
              <button className="btn-brand" onClick={() => setOtp(null)}>Done</button>
            </div>
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={runConfirm}
        title={confirm?.title ?? ''}
        message={confirm?.message ?? ''}
        confirmLabel={confirm?.confirmLabel ?? 'Confirm'}
        danger={confirm?.danger ?? true}
        busy={confirmBusy}
      />
    </div>
  )
}
