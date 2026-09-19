import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { timeAgo } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface ApiToken {
  id: string
  name: string
  scopes: string[]
  last_used_at?: string
  revoked_at?: string
  created_at: string
}

export function SettingsPage() {
  const { user, org } = useAuth()
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [show, setShow] = useState(false)
  const [raw, setRaw] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', scopes: 'websites:read', expires_in_days: 90 })
  const [hostname, setHostname] = useState<string | null>(null)
  const [hostnameDraft, setHostnameDraft] = useState('')
  const [hostnameBusy, setHostnameBusy] = useState(false)
  const [hostnameMsg, setHostnameMsg] = useState('')
  const isAdmin = !!user?.is_platform_admin

  const load = async () => {
    if (!org) return
    const r = await api.get<{ api_tokens: ApiToken[] }>(`/v1/organizations/${org.id}/api-tokens`).catch(() => ({ api_tokens: [] as ApiToken[] }))
    setTokens(r.api_tokens ?? [])
  }

  useEffect(() => {
    if (isAdmin && org) void load()
  }, [org?.id, isAdmin]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!isAdmin) return
    api.get<{ hostname: string }>('/v1/settings')
      .then((r) => {
        setHostname(r.hostname ?? '')
        setHostnameDraft(r.hostname ?? '')
      })
      .catch(() => setHostname(''))
  }, [isAdmin])

  const saveHostname = async () => {
    setErr('')
    setHostnameMsg('')
    setHostnameBusy(true)
    try {
      const r = await api.patch<{ hostname: string }>('/v1/settings/hostname', { hostname: hostnameDraft })
      setHostname(r.hostname)
      setHostnameMsg('Hostname updated.')
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to update hostname')
    } finally {
      setHostnameBusy(false)
    }
  }

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
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create token')
    } finally {
      setBusy(false)
    }
  }

  const revoke = async (t: ApiToken) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Revoke Token', message: `Revoke token "${t.name}"?`, confirmLabel: 'Revoke Token' }))) return
    await api.del(`/v1/organizations/${org.id}/api-tokens/${t.id}`)
    await load()
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5">
        <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Settings</h1>
        <p className="mt-[5px] text-[12px] text-muted">Workspace defaults, security and automation.</p>
      </div>

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
      <Card className="mb-3.5">
        <CardHeader title="Profile" subtitle="Your account identity" />
        <div className="space-y-2.5">
          <Row label="Name" value={user?.name ?? ''} />
          <Row label="Email" value={user?.email ?? ''} />
          <Row label="Role" value={user?.is_platform_admin ? 'Platform Administrator' : 'Member'} />
          <Row label="Account" value={org?.name ?? '—'} />
        </div>
      </Card>

      {isAdmin && (
        <Card className="mb-3.5">
          <CardHeader title="Panel Hostname" subtitle="The address where this panel is reachable" />
          <p className="mb-3 text-[13px] text-sub">
            The domain or address where this panel is reachable. Change it here anytime — no reinstall needed.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="input flex-1 sm:min-w-[260px]"
              value={hostnameDraft}
              onChange={(e) => setHostnameDraft(e.target.value)}
              placeholder="panel.example.com"
            />
            <button
              className="btn-primary"
              onClick={saveHostname}
              disabled={hostnameBusy || !hostnameDraft || hostnameDraft === hostname}
            >
              {hostnameBusy ? 'Saving...' : 'Save'}
            </button>
          </div>
          {hostnameMsg && <p className="mt-2 text-[12.5px] font-semibold text-ok">{hostnameMsg}</p>}
        </Card>
      )}

      {isAdmin && org && (
        <Card>
          <CardHeader
            title="API Tokens"
            subtitle="Automate EpicHost from CI or billing systems"
            right={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> New token</button>}
          />
          {tokens === null ? (
            <SkeletonRows rows={2} height="h-12" />
          ) : tokens.length === 0 ? (
            <EmptyState icon={<KeyRound size={20} />} title="No API tokens" subtitle="Create a token to automate EpicHost from CI or billing systems." />
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
                  {t.revoked_at && <span className="status-chip">Revoked</span>}
                  {!t.revoked_at && (
                    <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => revoke(t)} title="Revoke">
                      <Trash2 size={13} />
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
        </Card>
      )}
      </div>

      <Modal open={show} onClose={() => setShow(false)} title="Create API Token" subtitle="Scopes limit what the token can do.">
        <ErrorNote message={err} />
        <Field label="Name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="ci-pipeline" /></Field>
        <Field label="Scopes" hint="Comma-separated. e.g. websites:read, websites:write, databases:read">
          <input className="input" value={form.scopes} onChange={(e) => setForm({ ...form, scopes: e.target.value })} />
        </Field>
        <Field label="Expires in days (optional)"><input className="input" type="number" value={form.expires_in_days} onChange={(e) => setForm({ ...form, expires_in_days: Number(e.target.value) })} /></Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.name}>
          {busy ? 'Creating...' : 'Create Token'}
        </button>
      </Modal>

      <Modal open={!!raw} onClose={() => setRaw(null)} title="Token Created" subtitle="Copy it now — it will never be shown again.">
        
        <pre className="overflow-x-auto rounded-[10px] bg-navy p-4 font-mono text-[11.5px] text-slate-200">{raw}</pre>
      </Modal>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between rounded-[9px] border border-line px-3 py-2.5">
      <span className="text-[11px] font-bold text-[#566278]">{label}</span>
      <span className="max-w-[60%] truncate text-[11px] font-semibold text-ink">{value}</span>
    </div>
  )
}
