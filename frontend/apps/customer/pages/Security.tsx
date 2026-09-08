import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { KeyRound, Plus, Trash2, ShieldCheck, Lock } from 'lucide-react'
import { api, useAuth, timeAgo } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, pushToast, ConfirmDialog } from '@epicpanel/ui'
import { Modal, Field, ErrorNote } from '@epicpanel/forms'
import { TwoFactorCard } from '../security/TwoFactor'

interface ApiToken {
  id: string
  name: string
  scopes: string[]
  last_used_at?: string
  revoked_at?: string
  created_at: string
}

/**
 * Account security: 2FA (Phase 2) + API keys. Session listing is deferred —
 * the backend has no session-list endpoint yet (noted in the Phase 5 handoff).
 */
export function SecurityPage() {
  const { org, myRole } = useAuth()
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [show, setShow] = useState(false)
  const [raw, setRaw] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', scopes: 'websites:read', expires_in_days: 90 })
  const [confirm, setConfirm] = useState<ApiToken | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  // API tokens are org-admin+ server-side; hide the section for lower roles.
  const isAdmin = myRole === 'owner' || myRole === 'admin'

  const load = () => {
    if (!org) return
    api
      .get<{ api_tokens: ApiToken[] }>(`/v1/organizations/${org.id}/api-tokens`)
      .then((r) => setTokens(r.api_tokens ?? []))
      .catch(() => setTokens([]))
  }

  useEffect(() => {
    if (isAdmin && org) load()
    else setTokens([])
  }, [org?.id, isAdmin]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const scopes = form.scopes.split(',').map((s) => s.trim()).filter(Boolean)
      const r = await api.post<{ raw_token: string }>(`/v1/organizations/${org.id}/api-tokens`, {
        name: form.name, scopes, expires_in_days: form.expires_in_days || undefined,
      })
      setRaw(r.raw_token)
      setShow(false)
      setForm({ name: '', scopes: 'websites:read', expires_in_days: 90 })
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create token')
    } finally {
      setBusy(false)
    }
  }

  const revoke = async (t: ApiToken) => {
    if (!org) return
    setConfirmBusy(true)
    try {
      await api.del(`/v1/organizations/${org.id}/api-tokens/${t.id}`)
      pushToast('success', `Token "${t.name}" revoked.`)
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Revoke failed')
      setConfirm(null)
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Security"
        subtitle="Protect your panel login and manage automation keys."
        actions={<Link to="/ssl" className="btn-ghost"><Lock size={14} /> SSL certificates</Link>}
      />

      <TwoFactorCard />

      <Card className="mb-3.5">
        <CardHeader title="Sessions" subtitle="Where you are signed in" right={<span className="status-chip status-live">This device</span>} />
        <p className="text-[12px] text-muted">
          Session history is not available yet — your provider can sign you out of all devices from the server.
        </p>
      </Card>

      {isAdmin && org && (
        <Card>
          <CardHeader
            title="API Keys"
            subtitle="Automate EpicHost from CI or billing systems"
            right={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> New key</button>}
          />
          {tokens === null ? (
            <SkeletonRows rows={2} height="h-12" />
          ) : tokens.length === 0 ? (
            <EmptyState icon={<KeyRound size={20} />} title="No API keys" subtitle="Create a key to automate EpicHost from CI or billing systems." />
          ) : (
            <div className="divide-y divide-line">
              {tokens.map((t) => (
                <div key={t.id} className="flex items-center gap-3 py-3.5">
                  <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-surface-2 text-[#667085]">
                    <KeyRound size={14} strokeWidth={1.8} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[11px] font-bold text-ink">{t.name}</div>
                    <div className="truncate text-[9.5px] text-muted">
                      {t.scopes.join(', ')} · created {timeAgo(t.created_at)}
                      {t.revoked_at ? ' · revoked' : ''}
                    </div>
                  </div>
                  {t.revoked_at ? (
                    <span className="status-chip">Revoked</span>
                  ) : (
                    <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => setConfirm(t)} title="Revoke">
                      <Trash2 size={13} />
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
        </Card>
      )}

      <Modal open={show} onClose={() => setShow(false)} title="Create API Key" subtitle="Scopes limit what the key can do.">
        <ErrorNote message={err} />
        <Field label="Name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="ci-pipeline" /></Field>
        <Field label="Scopes" hint="Comma-separated. e.g. websites:read, websites:write, databases:read">
          <input className="input" value={form.scopes} onChange={(e) => setForm({ ...form, scopes: e.target.value })} />
        </Field>
        <Field label="Expires in days (optional)"><input className="input" type="number" value={form.expires_in_days} onChange={(e) => setForm({ ...form, expires_in_days: Number(e.target.value) })} /></Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.name}>
          {busy ? 'Creating...' : 'Create Key'}
        </button>
      </Modal>

      <Modal open={!!raw} onClose={() => setRaw(null)} title="API Key Created" subtitle="Copy it now — it will never be shown again.">
        <div className="mb-3 flex items-center gap-2 text-[11px] font-semibold text-ok">
          <ShieldCheck size={13} /> Stored encrypted; this is the only copy.
        </div>
        <pre className="overflow-x-auto rounded-[10px] bg-navy p-4 font-mono text-[11.5px] text-slate-200">{raw}</pre>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={() => confirm && revoke(confirm)}
        title="Revoke API key"
        message={`Revoke "${confirm?.name}"? Anything using it stops working immediately.`}
        confirmLabel="Revoke"
        busy={confirmBusy}
      />
    </div>
  )
}
