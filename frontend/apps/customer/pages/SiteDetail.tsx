import { useCallback, useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { Link, useParams } from 'react-router-dom'
import { Globe, ArrowLeft, Folder, Clock, Network, KeyRound, History, Plus, Trash2, ShieldQuestion, RefreshCw } from 'lucide-react'
import { api, useAuth, domainsApi, redirectsApi, fmtBytes, timeAgo } from '@epicpanel/core'
import type { Website, Domain, Redirect } from '@epicpanel/core'
import { Card, CardHeader, StatusBadge, EmptyState, SkeletonRows, MiniItem, Breadcrumbs, RowActions, pushToast, UsageCard } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, ConfirmDialog } from '@epicpanel/forms'

interface Usage {
  cpu_percent: number
  memory_bytes: number
  disk_mb: number
  processes: number
  sampled_at?: string
}

export function SiteDetailPage() {
  const { org, myRole } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const [site, setSite] = useState<Website | null>(null)
  const [domains, setDomains] = useState<Domain[] | null>(null)
  const [redirects, setRedirects] = useState<Redirect[] | null>(null)
  const [usage, setUsage] = useState<Usage | null>(null)
  const [err, setErr] = useState('')
  const [showAlias, setShowAlias] = useState(false)
  const [aliasForm, setAliasForm] = useState({ domain: '', docroot_suffix: '' })
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<{ title: string; message: string; action: () => Promise<void> } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const canManage = myRole === 'owner' || myRole === 'admin' || myRole === 'developer'

  const load = useCallback(async () => {
    if (!org) return
    const base = `/v1/organizations/${org.id}/websites/${websiteId}`
    api.get<Website>(base).then(setSite).catch(() => setSite(null))
    api.get<{ domains: Domain[] }>(`${base}/domains`).then((r) => setDomains(r.domains ?? [])).catch(() => setDomains([]))
    api.get<{ usage: Usage }>(`${base}/usage`).then((r) => setUsage((r as any).usage ?? (r as any))).catch(() => setUsage(null))
    redirectsApi.list(org.id, websiteId).then((r) => setRedirects(r.redirects ?? [])).catch(() => setRedirects([]))
  }, [org?.id, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  const addAlias = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: { domain: string; docroot_suffix?: string } = { domain: aliasForm.domain.toLowerCase() }
      if (aliasForm.docroot_suffix.trim()) body.docroot_suffix = aliasForm.docroot_suffix.trim()
      await domainsApi.addAlias(org.id, websiteId, body)
      setShowAlias(false)
      setAliasForm({ domain: '', docroot_suffix: '' })
      pushToast('success', 'Domain added — serving config is updating.')
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to add domain')
    } finally {
      setBusy(false)
    }
  }

  const verifyDns = async (d: Domain) => {
    if (!org) return
    try {
      await domainsApi.verifyDns(org.id, d.id)
      pushToast('success', `DNS verification queued for ${d.domain}.`)
      setTimeout(() => void load(), 1500)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Verification failed')
    }
  }

  const removeAlias = async (d: Domain) => {
    if (!org) return
    setConfirm({
      title: 'Remove domain',
      message: `Remove "${d.domain}"? It stops serving immediately. The primary domain cannot be removed.`,
      action: async () => {
        await domainsApi.remove(org.id, websiteId, d.id)
        pushToast('success', 'Domain removed.')
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

  if (site === null && !domains) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <Card><SkeletonRows rows={3} /></Card>
      </div>
    )
  }

  const primary = domains?.find((d) => d.kind === 'primary')
  const aliases = domains?.filter((d) => d.kind !== 'primary') ?? []

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <Breadcrumbs crumbs={[{ label: 'Websites', to: '/websites' }, { label: site?.primary_domain || site?.name || 'Website' }]} />

      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <Link to="/websites" className="icon-btn !h-[34px] !w-[34px]" title="Back" aria-label="Back"><ArrowLeft size={15} /></Link>
          <div>
            <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">{site?.primary_domain || site?.name || 'Website'}</h1>
            <p className="mt-[5px] flex flex-wrap items-center gap-2 text-[12px] text-muted">
              {site && <StatusBadge status={site.status} />}
              {site && <span>{site.runtime}{site.runtime_version ? ` ${site.runtime_version}` : ''} · {site.web_server}</span>}
              {site && <span className="font-mono">{site.document_root}</span>}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Link to={`/files/${websiteId}`} className="btn-ghost"><Folder size={14} /> Files</Link>
          <Link to={`/crons/${websiteId}`} className="btn-ghost"><Clock size={14} /> Crons</Link>
          <Link to={`/dns/${websiteId}`} className="btn-ghost"><Network size={14} /> DNS</Link>
          <Link to={`/ftp`} className="btn-ghost"><KeyRound size={14} /> FTP</Link>
          <Link to={`/backups`} className="btn-ghost"><History size={14} /> Backups</Link>
        </div>
      </div>

      <ErrorNote message={err} />

      {/* Usage */}
      {usage && (
        <div className="mb-4 grid grid-cols-1 gap-3.5 sm:grid-cols-3">
          <UsageCard
            icon={<Globe size={14} strokeWidth={1.8} />} tone="blue" title="CPU" sub="This website"
            value={`${Math.round(usage.cpu_percent ?? 0)}%`} pct={usage.cpu_percent ?? 0} color="#2563eb"
            note={usage.sampled_at ? `sampled ${timeAgo(usage.sampled_at)}` : 'not sampled yet'}
          />
          <UsageCard
            icon={<Globe size={14} strokeWidth={1.8} />} tone="purple" title="Memory" sub="This website"
            value={usage.memory_bytes ? fmtBytes(usage.memory_bytes) : '—'}
            pct={0} color="#7c4dff"
            note={`${usage.processes ?? 0} processes`}
          />
          <UsageCard
            icon={<Globe size={14} strokeWidth={1.8} />} tone="green" title="Disk" sub="This website"
            value={usage.disk_mb ? fmtBytes(usage.disk_mb * 1024 * 1024) : '—'}
            pct={0} color="#0f9d6e"
            note="sampled by the agent"
          />
        </div>
      )}

      {/* Domains */}
      <Card className="mb-4 overflow-hidden !p-0">
        <CardHeader
          className="mx-4 mt-4 !mb-0"
          title="Domains"
          subtitle={`${domains?.length ?? 0} serving this website`}
          right={
            canManage ? (
              <button className="btn-primary !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => setShowAlias(true)}><Plus size={12} /> Add domain</button>
            ) : undefined
          }
        />
        {domains === null ? (
          <div className="p-5"><SkeletonRows rows={2} height="h-10" /></div>
        ) : (
          <div className="divide-y divide-line">
            {[primary, ...aliases].filter((d): d is Domain => !!d).map((d) => (
              <div key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <Globe size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[11px] font-bold text-[#243047]">{d.domain}</span>
                    <span className="status-chip">{d.kind === 'primary' ? 'Primary' : d.kind === 'sub' ? 'Subdomain' : 'Alias'}</span>
                    {d.ssl_state === 'active' ? (
                      <span className="status-chip status-live">SSL valid</span>
                    ) : d.ssl_state === 'issuing' ? (
                      <span className="status-chip status-warning">SSL issuing</span>
                    ) : (
                      <span className="status-chip">SSL {d.ssl_mode}</span>
                    )}
                  </div>
                  <div className="mt-0.5 text-[9.5px] text-muted">
                    {d.dns_points_to_server === true ? 'DNS points here' : d.dns_points_to_server === false ? 'DNS not pointing here' : 'DNS not verified'}
                    {d.docroot_suffix ? ` · docroot /${d.docroot_suffix}` : ''}
                  </div>
                </div>
                <RowActions>
                  <button className="icon-btn" title="Verify DNS" aria-label="Verify DNS" onClick={() => void verifyDns(d)}><ShieldQuestion size={13} /></button>
                  {d.kind !== 'primary' && (
                    <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Remove" aria-label="Remove" onClick={() => void removeAlias(d)}>
                      <Trash2 size={13} />
                    </button>
                  )}
                </RowActions>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* Redirects */}
      <Card>
        <CardHeader className="mx-4 mt-4 !mb-0" title="Redirects" subtitle="Forward visitors from one address to another" />
        {redirects === null ? (
          <div className="p-5"><SkeletonRows rows={2} height="h-10" /></div>
        ) : redirects.length === 0 ? (
          <EmptyState icon={<RefreshCw size={20} />} title="No redirects" subtitle="Create one on the Domains page." />
        ) : (
          <div className="divide-y divide-line">
            {redirects.map((r) => (
              <MiniItem
                key={r.id}
                icon={<RefreshCw size={14} strokeWidth={1.8} />}
                title={`${r.from_domain} → ${r.to_url}`}
                sub={`${r.status_code} · ${r.enabled ? 'active' : 'paused'}`}
                right={
                  <RowActions>
                    <button
                      className="icon-btn"
                      title={r.enabled ? 'Pause redirect' : 'Resume redirect'}
                      aria-label={r.enabled ? 'Pause redirect' : 'Resume redirect'}
                      onClick={() => org && redirectsApi.setEnabled(org.id, r.id, !r.enabled).then(load).catch((ex) => pushToast('error', ex.message))}
                    >
                      {r.enabled ? <Clock size={13} /> : <RefreshCw size={13} />}
                    </button>
                    <button
                      className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                      title="Delete redirect" aria-label="Delete redirect"
                      onClick={() => org && redirectsApi.remove(org.id, r.id).then(load).catch((ex) => pushToast('error', ex.message))}
                    >
                      <Trash2 size={13} />
                    </button>
                  </RowActions>
                }
              />
            ))}
          </div>
        )}
      </Card>

      <Modal open={showAlias} onClose={() => setShowAlias(false)} title="Add Domain" subtitle="Aliases and subdomains serve the same website.">
        <ErrorNote message={err} />
        <Field label="Domain" hint="e.g. shop.example.com or example.net">
          <input className="input" value={aliasForm.domain} onChange={(e) => setAliasForm({ ...aliasForm, domain: e.target.value })} placeholder="www.example.com" />
        </Field>
        <Field label="Document root suffix (optional)" hint="Serve this domain from a subfolder, e.g. shop → /shop">
          <input className="input" value={aliasForm.docroot_suffix} onChange={(e) => setAliasForm({ ...aliasForm, docroot_suffix: e.target.value })} placeholder="shop" />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={addAlias} disabled={busy || !aliasForm.domain}>
          {busy ? (<><Spinner size={13} /> Adding…</>) : 'Add Domain'}
        </button>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={runConfirm}
        title={confirm?.title ?? ''}
        message={confirm?.message ?? ''}
        confirmLabel="Remove"
        busy={confirmBusy}
      />
    </div>
  )
}
