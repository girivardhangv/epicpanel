import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, Lock, Globe, RefreshCw, ShieldCheck, ShieldAlert, ShieldQuestion } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, EmptyState, SkeletonRows } from '@/components/cards'
import { Select, ErrorNote } from '@/components/ui'
import type { Domain, Website } from '@/lib/types'

const SSL_MODES = [
  { value: 'none', label: 'No SSL (HTTP only)' },
  { value: 'selfsigned', label: 'Self-signed' },
  { value: 'letsencrypt', label: "Let's Encrypt" },
]

const STATE_STYLES: Record<string, string> = {
  active: 'text-ok',
  issuing: 'text-warn',
  pending: 'text-muted',
  failed: 'text-danger',
}

function StateLabel({ d }: { d: Domain }) {
  const cls = STATE_STYLES[d.ssl_state] ?? 'text-muted'
  const label =
    d.ssl_mode === 'none' && d.ssl_state === 'pending'
      ? 'SSL: off'
      : `SSL: ${d.ssl_mode} · ${d.ssl_state}`
  return <span className={`text-[11.5px] font-semibold ${cls}`}>{label}</span>
}

function SslExpiryChip({ d }: { d: Domain }) {
  if (!d.ssl_expires_at) return null
  const days = Math.floor((new Date(d.ssl_expires_at).getTime() - Date.now()) / 86400000)
  const cls = days < 0 ? 'status-down' : days <= 14 ? 'status-warning' : 'status-live'
  const label = days < 0 ? 'expired' : `expires in ${days}d`
  return <span className={`status-chip ${cls}`}>{label}</span>
}

// Org-wide SSL / certificates manager.
export function SecurityPage() {
  const { org } = useAuth()
  const [sites, setSites] = useState<Website[] | null>(null)
  const [domains, setDomains] = useState<Domain[] | null>(null)
  const [err, setErr] = useState('')
  const [busyId, setBusyId] = useState('')

  const canManage = !!org

  const load = useCallback(async () => {
    if (!org) return
    try {
      const [sitesRes, domainsRes] = await Promise.all([
        api.get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`),
        api.get<{ domains: Domain[] }>(`/v1/organizations/${org.id}/domains`),
      ])
      setSites(sitesRes.websites ?? [])
      setDomains(domainsRes.domains ?? [])
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to load SSL state')
    }
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  // Auto-refresh while any certificate is being issued.
  const hasPending = useMemo(
    () => (domains ?? []).some((d) => d.ssl_state === 'issuing' || d.ssl_state === 'pending'),
    [domains],
  )
  useEffect(() => {
    if (!hasPending || !org) return
    const t = setInterval(() => void load(), 4000)
    return () => clearInterval(t)
  }, [hasPending, load, org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const setMode = async (d: Domain, mode: string) => {
    if (!org) return
    setErr('')
    setBusyId(d.id)
    try {
      await api.post(`/v1/organizations/${org.id}/domains/${d.id}/ssl`, { mode })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to set SSL mode')
    } finally {
      setBusyId('')
    }
  }

  const verifyDns = async (d: Domain) => {
    if (!org) return
    setErr('')
    setBusyId(d.id)
    try {
      await api.post(`/v1/organizations/${org.id}/domains/${d.id}/verify-dns`)
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to verify DNS')
    } finally {
      setBusyId('')
    }
  }

  const siteName = (id: string) => sites?.find((s) => s.id === id)?.primary_domain || sites?.find((s) => s.id === id)?.name || ''

  const grouped = useMemo(() => {
    const map = new Map<string, Domain[]>()
    for (const d of domains ?? []) {
      const list = map.get(d.website_id) ?? []
      list.push(d)
      map.set(d.website_id, list)
    }
    return Array.from(map.entries())
  }, [domains])

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Security</h1>
          <p className="mt-[5px] max-w-[560px] text-[12px] text-muted">
            Two-factor authentication for your account and SSL certificates for every domain.
          </p>
        </div>
        <button className="btn-ghost" onClick={() => void load()} disabled={!sites}>
          <RefreshCw size={14} /> Refresh
        </button>
      </div>

      <ErrorNote message={err} />

      <TwoFactorCard />

      {!domains ? (
        <Card>
          <SkeletonRows rows={4} />
        </Card>
      ) : domains.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Lock size={22} />}
            title="No domains yet"
            subtitle="Create a site first — its primary domain appears here, ready for Let's Encrypt."
          />
        </Card>
      ) : (
        grouped.map(([websiteId, list]) => {
          const name = siteName(websiteId)
          return (
            <Card key={websiteId} className="mb-3.5 overflow-hidden !p-0">
              <CardHeader
                title={name || 'Website'}
                subtitle={`${list.length} domain${list.length > 1 ? 's' : ''}`}
                right={<Link to={`/sites/${websiteId}`} className="text-[11px] font-bold text-brand hover:underline">Open site <ArrowRight size={11} className="inline" /></Link>}
              />
              <div className="divide-y divide-line">
                {list.map((d) => (
                  <div key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                    <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                      <Globe size={14} strokeWidth={1.8} />
                    </div>
                    <div className="min-w-[180px] flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-[11px] font-bold text-[#243047]">{d.domain}</span>
                        {d.kind === 'primary' && <span className="status-chip">primary</span>}
                      </div>
                      <div className="mt-0.5 flex flex-wrap items-center gap-2 text-[10px]">
                        <StateLabel d={d} />
                        <SslExpiryChip d={d} />
                        {d.dns_points_to_server === false && (
                          <span className="text-warn">· DNS not pointing here</span>
                        )}
                        {d.dns_points_to_server === true && (
                          <span className="inline-flex items-center gap-1 text-ok"><ShieldCheck size={11} /> DNS ok</span>
                        )}
                      </div>
                      {d.ssl_error && (
                        <div className="mt-1.5 flex items-start gap-1.5 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-2.5 py-1.5 text-[10.5px] font-semibold text-danger">
                          <ShieldAlert size={12} className="mt-0.5 shrink-0" />
                          <span className="break-words">{d.ssl_error}</span>
                        </div>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <button
                        className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]"
                        onClick={() => verifyDns(d)}
                        disabled={busyId === d.id}
                        title="Check that this domain resolves to the server"
                      >
                        <ShieldQuestion size={13} /> {busyId === d.id ? '...' : 'Verify DNS'}
                      </button>
                      <Select
                        value={d.ssl_mode}
                        onChange={(mode) => mode !== d.ssl_mode && void setMode(d, mode)}
                        options={SSL_MODES}
                      />
                    </div>
                  </div>
                ))}
              </div>
            </Card>
          )
        })
      )}

      {!canManage && (
        <Card className="mt-4">
          <div className="flex items-center gap-2 text-[13px] text-sub">
            <Lock size={14} /> Ask an administrator to configure certificates.
          </div>
        </Card>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Two-factor authentication (account-level, 2FA foundation)
// ---------------------------------------------------------------------------

type MfaSetup = { secret: string; otpauth_uri: string }
type MfaEnabled = { enabled: boolean; recovery_codes: string[] }

export function TwoFactorCard() {
  const { user } = useAuth()
  const [setup, setSetup] = useState<MfaSetup | null>(null)
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState<string[]>([])
  const [disabling, setDisabling] = useState(false)
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const enabled = user?.mfa_enabled === true

  const startSetup = async () => {
    setErr(''); setBusy(true)
    try {
      setSetup(await api.post<MfaSetup>('/v1/auth/mfa/setup'))
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to start setup')
    } finally { setBusy(false) }
  }

  const confirmSetup = async () => {
    setErr(''); setBusy(true)
    try {
      const res = await api.post<MfaEnabled>('/v1/auth/mfa/enable', { code })
      setSetup(null)
      setRecovery(res.recovery_codes ?? [])
    } catch (ex: any) {
      setErr(ex.message ?? 'Invalid code')
    } finally { setBusy(false) }
  }

  const disable = async () => {
    setErr(''); setBusy(true)
    try {
      await api.post('/v1/auth/mfa/disable', { password })
      setDisabling(false); setPassword('')
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to disable')
    } finally { setBusy(false) }
  }

  if (recovery.length > 0) {
    return (
      <Card className="mb-4 border-2 border-[#ffd0d7]">
        <CardHeader title="Two-factor authentication is ON" subtitle="Save your recovery codes now — they are shown only once." />
        <div className="grid grid-cols-2 gap-1.5 rounded-[9px] bg-surface-2 p-3 font-mono text-[11.5px] sm:grid-cols-5">
          {recovery.map((c) => <span key={c}>{c}</span>)}
        </div>
        <button className="btn-brand mt-3 !min-h-[32px] !px-3 !text-[11px]" onClick={() => setRecovery([])}>
          I saved my codes
        </button>
      </Card>
    )
  }

  return (
    <Card className="mb-4">
      <CardHeader
        title="Two-factor authentication"
        subtitle={enabled ? 'Active — a code is required at every login.' : 'Add a TOTP app (Google Authenticator, 1Password, ...) as a second factor.'}
      />
      <ErrorNote message={err} />
      {enabled ? (
        disabling ? (
          <div className="flex flex-wrap items-end gap-2">
            <label className="block">
              <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Confirm password</span>
              <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
            <button className="btn-ghost !text-danger" disabled={busy} onClick={disable}>Turn off 2FA</button>
            <button className="btn-ghost" onClick={() => setDisabling(false)}>Cancel</button>
          </div>
        ) : (
          <button className="btn-ghost !text-danger" onClick={() => setDisabling(true)}>
            <ShieldAlert size={13} /> Disable two-factor
          </button>
        )
      ) : setup ? (
        <div className="space-y-3">
          <div className="break-all rounded-[9px] bg-surface-2 p-2.5 font-mono text-[11px]">{setup.secret}</div>
          <div className="text-[11px] text-sub">Add this secret to your authenticator app (or scan its otpauth:// URI from the setup response), then enter the 6-digit code.</div>
          <div className="flex items-end gap-2">
            <input className="input max-w-[140px]" inputMode="numeric" placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} />
            <button className="btn-brand !min-h-[32px] !px-3 !text-[11px]" disabled={busy} onClick={confirmSetup}>Verify & enable</button>
          </div>
        </div>
      ) : (
        <button className="btn-brand !min-h-[32px] !px-3 !text-[11px]" disabled={busy} onClick={startSetup}>
          <Lock size={13} /> Set up two-factor
        </button>
      )}
    </Card>
  )
}
