import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Lock, Globe, RefreshCw, ShieldCheck, ShieldAlert, ShieldQuestion, ShieldPlus } from 'lucide-react'
import { api, useAuth } from '@epicpanel/core'
import type { Domain, Website } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, pushToast } from '@epicpanel/ui'
import { ErrorNote, Select } from '@epicpanel/forms'

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

/** Expiry countdown chip: expired / <=14d amber / otherwise green. */
export function SslExpiryChip({ d }: { d: Domain }) {
  if (!d.ssl_expires_at) return null
  const days = Math.floor((new Date(d.ssl_expires_at).getTime() - Date.now()) / 86400000)
  const cls = days < 0 ? 'status-down' : days <= 14 ? 'status-warning' : 'status-live'
  const label = days < 0 ? `expired ${-days}d ago` : days === 0 ? 'expires today' : `expires in ${days}d`
  return <span className={`status-chip ${cls}`}>{label}</span>
}

/** SSL / TLS screen — status, issue, renew and expiry countdown per domain. */
export function SslPage() {
  const { org, myRole } = useAuth()
  const [sites, setSites] = useState<Website[] | null>(null)
  const [domains, setDomains] = useState<Domain[] | null>(null)
  const [err, setErr] = useState('')
  const [busyId, setBusyId] = useState('')

  const canManage = myRole === 'owner' || myRole === 'admin' || myRole === 'developer'

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
      pushToast('success', `Certificate ${mode === 'letsencrypt' ? 'issuance' : mode} queued for ${d.domain}.`)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to set SSL mode')
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
      pushToast('success', `DNS verification queued for ${d.domain}.`)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to verify DNS')
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
      <PageTitle
        title="SSL / TLS"
        subtitle="Secure your sites with managed certificates and automatic renewal."
        actions={<button className="btn-ghost" onClick={() => void load()} disabled={!sites}><RefreshCw size={14} /> Refresh</button>}
      />

      <ErrorNote message={err} />

      {!domains ? (
        <Card><SkeletonRows rows={4} /></Card>
      ) : domains.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Lock size={22} />}
            title="No domains yet"
            subtitle="Create a site first — its primary domain appears here, ready for Let's Encrypt."
          />
        </Card>
      ) : (
        grouped.map(([websiteId, list]) => (
          <Card key={websiteId} className="mb-3.5 overflow-hidden !p-0">
            <CardHeader
              title={siteName(websiteId) || 'Website'}
              subtitle={`${list.length} domain${list.length > 1 ? 's' : ''}`}
              right={<Link to={`/websites/${websiteId}`} className="text-[11px] font-bold text-brand hover:underline">Open site</Link>}
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
                  {canManage && (
                    <div className="flex items-center gap-2">
                      <button
                        className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]"
                        onClick={() => verifyDns(d)}
                        disabled={busyId === d.id}
                        title="Check that this domain resolves to the server"
                      >
                        <ShieldQuestion size={13} /> {busyId === d.id ? '...' : 'Verify DNS'}
                      </button>
                      <button
                        className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]"
                        onClick={() => d.ssl_mode !== 'letsencrypt' && void setMode(d, 'letsencrypt')}
                        disabled={busyId === d.id || d.ssl_mode === 'letsencrypt'}
                        title="Issue or renew a Let's Encrypt certificate"
                      >
                        <ShieldPlus size={13} /> {d.ssl_mode === 'letsencrypt' ? 'Auto-renew on' : 'Issue certificate'}
                      </button>
                      <Select
                        value={d.ssl_mode}
                        onChange={(mode) => mode !== d.ssl_mode && void setMode(d, mode)}
                        options={SSL_MODES}
                      />
                    </div>
                  )}
                </div>
              ))}
            </div>
          </Card>
        ))
      )}
    </div>
  )
}
