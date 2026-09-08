import { useEffect, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import {
  ArrowLeft, ArrowUpRight, Ban, Copy, Eye, Globe, Folder, Clock, KeyRound, Terminal as TerminalIcon,
  Database as DatabaseIcon, Lock, History, Network, Pause, Play, Plus, RefreshCw, ShieldQuestion,
  Trash2, FileText, GitBranch, X,
} from 'lucide-react'
import { api, domainsApi, ftpApi, redirectsApi, lifecycleApi } from '@/lib/api'
import type { FtpAccount, Redirect } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, UsageCard, MiniItem } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { WordPressModal } from '@/components/WordPressModal'
import { fmtBytes, timeAgo } from '@/lib/types'
import type { Website, Database, Domain } from '@/lib/types'

interface SiteUsage {
  cpu_percent: number
  memory_bytes: number
  disk_mb: number
  processes: number
  sampled_at?: string
  memory_limit?: number
  cpu_limit_cores?: number
  disk_limit_mb?: number
}

interface SiteResource {
  id: string
  name?: string
  status?: string
  [k: string]: any
}

// cPanel-style per-site management hub: every tool for one site on one page.
export function SiteDetailPage() {
  const { org, user } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const navigate = useNavigate()
  const [site, setSite] = useState<Website | null>(null)
  const [dbs, setDbs] = useState<Database[]>([])
  const [domains, setDomains] = useState<Domain[]>([])
  const [loading, setLoading] = useState(true)
  const [showWP, setShowWP] = useState(false)
  const [showDelete, setShowDelete] = useState(false)
  const [err, setErr] = useState('')
  const [rules, setRules] = useState('')
  const [rulesBusy, setRulesBusy] = useState(false)
  const [rulesErr, setRulesErr] = useState('')
  const [rulesSaved, setRulesSaved] = useState('')
  const [serverRuntimes, setServerRuntimes] = useState<{ type: string; version: string; status: string }[]>([])
  const [wsBusy, setWsBusy] = useState(false)
  const [wsMsg, setWsMsg] = useState('')
  const [docrootDraft, setDocrootDraft] = useState('')
  const [usage, setUsage] = useState<SiteUsage | null>(null)
  const [usageBusy, setUsageBusy] = useState(false)
  const [redirects, setRedirects] = useState<Redirect[]>([])
  const [redirForm, setRedirForm] = useState({ from_domain: '', to_url: '', status_code: '301' })
  const [redirBusy, setRedirBusy] = useState(false)
  const [redirErr, setRedirErr] = useState('')
  const [ftp, setFtp] = useState<FtpAccount[]>([])
  const [showFtp, setShowFtp] = useState(false)
  const [ftpForm, setFtpForm] = useState({ protocol: 'ftp', label: '', password: '', home_subdir: '' })
  const [ftpBusy, setFtpBusy] = useState(false)
  const [ftpErr, setFtpErr] = useState('')
  const [ftpReveal, setFtpReveal] = useState<{ label: string; password: string; once: boolean } | null>(null)
  const [ftpBusyId, setFtpBusyId] = useState('')
  const [aliasForm, setAliasForm] = useState({ domain: '', docroot_suffix: '' })
  const [aliasBusy, setAliasBusy] = useState(false)
  const [busyDomain, setBusyDomain] = useState('')
  const [lifeBusy, setLifeBusy] = useState(false)
  const [lifeAction, setLifeAction] = useState<'' | 'suspend' | 'resume'>('')
  const isAdmin = !!user?.is_platform_admin

  const load = async () => {
    if (!org) return
    setLoading(true)
    try {
      const base = `/v1/organizations/${org.id}/websites/${websiteId}`
      const ws = await api.get<Website>(base)
      setSite(ws)
      const [d, dm, rd, ft] = await Promise.all([
        api.get<{ databases: Database[] }>(`/v1/organizations/${org.id}/databases`).catch(() => ({ databases: [] })),
        api.get<{ domains: Domain[] }>(`${base}/domains`).catch(() => ({ domains: [] })),
        redirectsApi.list(org.id, websiteId).catch(() => ({ redirects: [] })),
        ftpApi.list(org.id, websiteId).catch(() => ({ ftp_accounts: [] })),
      ])
      setDbs((d.databases ?? []).filter((x) => x.website_id === websiteId))
      setDomains(dm.domains ?? [])
      setRedirects(rd.redirects ?? [])
      setFtp(ft.ftp_accounts ?? [])
      if (ws.server_id) {
        const rt = await api
          .get<{ runtimes: { type: string; version: string; status: string }[] }>(`/v1/organizations/${org.id}/servers/${ws.server_id}/runtimes`)
          .catch(() => ({ runtimes: [] }))
        setServerRuntimes((rt.runtimes ?? []).filter((r) => r.status === 'available'))
      }
      setDocrootDraft(ws.docroot_suffix ?? '')
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    void loadConfig()
  }, [org?.id, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  const deleteSite = async () => {
    if (!org || !site) return
    setErr('')
    try {
      await api.del(`/v1/organizations/${org.id}/websites/${site.id}`)
      setShowDelete(false)
      navigate('/sites')
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const setSslMode = async (d: Domain, mode: string) => {
    if (!org) return
    setErr('')
    try {
      await api.post(`/v1/organizations/${org.id}/domains/${d.id}/ssl`, { mode })
      setTimeout(() => void load(), 500)
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const addAlias = async () => {
    if (!org) return
    setErr('')
    setAliasBusy(true)
    try {
      const body: { domain: string; docroot_suffix?: string } = { domain: aliasForm.domain.toLowerCase() }
      if (aliasForm.docroot_suffix.trim()) body.docroot_suffix = aliasForm.docroot_suffix.trim()
      await domainsApi.addAlias(org.id, websiteId, body)
      setAliasForm({ domain: '', docroot_suffix: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setAliasBusy(false)
    }
  }

  const removeAlias = async (d: Domain) => {
    if (!org || !confirm(`Delete alias "${d.domain}"? It stops serving immediately.`)) return
    setErr('')
    try {
      await domainsApi.remove(org.id, websiteId, d.id)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const verifyDomainDns = async (d: Domain) => {
    if (!org) return
    setErr('')
    setBusyDomain(d.id)
    try {
      await domainsApi.verifyDns(org.id, d.id)
      setTimeout(() => void load(), 500)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusyDomain('')
    }
  }

  const addRedirect = async () => {
    if (!org) return
    setRedirErr('')
    setRedirBusy(true)
    try {
      await redirectsApi.create(org.id, websiteId, {
        from_domain: redirForm.from_domain,
        to_url: redirForm.to_url,
        status_code: Number(redirForm.status_code),
      })
      setRedirForm({ from_domain: '', to_url: '', status_code: '301' })
      setRedirects((await redirectsApi.list(org.id, websiteId)).redirects ?? [])
    } catch (ex: any) {
      setRedirErr(ex.message)
    } finally {
      setRedirBusy(false)
    }
  }

  const toggleRedirect = async (r: Redirect) => {
    if (!org) return
    setRedirErr('')
    try {
      await redirectsApi.setEnabled(org.id, r.id, !r.enabled)
      setRedirects((prev) => prev.map((x) => (x.id === r.id ? { ...x, enabled: !x.enabled } : x)))
    } catch (ex: any) {
      setRedirErr(ex.message)
    }
  }

  const removeRedirect = async (r: Redirect) => {
    if (!org || !confirm(`Delete the redirect from "${r.from_domain}"?`)) return
    setRedirErr('')
    try {
      await redirectsApi.remove(org.id, r.id)
      setRedirects((prev) => prev.filter((x) => x.id !== r.id))
    } catch (ex: any) {
      setRedirErr(ex.message)
    }
  }

  const createFtp = async () => {
    if (!org) return
    setFtpErr('')
    setFtpBusy(true)
    try {
      const body: { protocol: string; label: string; password?: string; home_subdir?: string } = {
        protocol: ftpForm.protocol,
        label: ftpForm.label,
      }
      if (ftpForm.password) body.password = ftpForm.password
      if (ftpForm.home_subdir.trim()) body.home_subdir = ftpForm.home_subdir.trim()
      const res = await ftpApi.create(org.id, websiteId, body)
      setShowFtp(false)
      setFtpForm({ protocol: 'ftp', label: '', password: '', home_subdir: '' })
      if (res?.password) setFtpReveal({ label: res.label, password: res.password, once: true })
      await load()
    } catch (ex: any) {
      setFtpErr(ex.message)
    } finally {
      setFtpBusy(false)
    }
  }

  const rotateFtp = async (a: FtpAccount) => {
    if (!org || !confirm(`Rotate the password for "${a.label}"? The current password stops working immediately.`)) return
    setFtpErr('')
    setFtpBusyId(a.id)
    try {
      const res = await ftpApi.rotatePassword(org.id, a.id)
      if (res?.password) setFtpReveal({ label: a.label, password: res.password, once: true })
      else setFtpErr('The server did not return the new password.')
    } catch (ex: any) {
      setFtpErr(ex.message)
    } finally {
      setFtpBusyId('')
    }
  }

  const revealFtp = async (a: FtpAccount) => {
    if (!org) return
    setFtpErr('')
    setFtpBusyId(a.id)
    try {
      const res = await ftpApi.reveal(org.id, a.id)
      setFtpReveal({ label: a.label, password: res.password, once: false })
    } catch (ex: any) {
      setFtpErr(ex.message)
    } finally {
      setFtpBusyId('')
    }
  }

  const removeFtp = async (a: FtpAccount) => {
    if (!org || !confirm(`Delete FTP account "${a.label}"? Logins with it stop immediately.`)) return
    setFtpErr('')
    setFtpBusyId(a.id)
    try {
      await ftpApi.remove(org.id, a.id)
      await load()
    } catch (ex: any) {
      setFtpErr(ex.message)
    } finally {
      setFtpBusyId('')
    }
  }

  const suspendSite = async () => {
    if (!org || !site) return
    if (!confirm(`Suspend "${site.primary_domain || site.name}"? Visitors and FTP logins are blocked until it is resumed.`)) return
    setErr('')
    setLifeBusy(true)
    try {
      await lifecycleApi.suspend(org.id, site.id)
      setWsMsg('Suspend queued — the site is being taken offline.')
      setTimeout(() => setWsMsg(''), 4000)
      setLifeAction('suspend')
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setLifeBusy(false)
    }
  }

  const resumeSite = async () => {
    if (!org || !site) return
    setErr('')
    setLifeBusy(true)
    try {
      await lifecycleApi.resume(org.id, site.id)
      setWsMsg('Resume queued — the site is coming back online.')
      setTimeout(() => setWsMsg(''), 4000)
      setLifeAction('resume')
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setLifeBusy(false)
    }
  }

  // Suspend/resume return 202 with a job id — poll until the status flips.
  useEffect(() => {
    if (!lifeAction || !org) return
    let tries = 0
    const target = lifeAction === 'suspend' ? 'suspended' : 'ready'
    const t = setInterval(async () => {
      tries += 1
      try {
        const ws = await api.get<Website>(`/v1/organizations/${org.id}/websites/${websiteId}`)
        setSite((prev) => (prev ? { ...prev, status: ws.status } : prev))
        if (ws.status === target || tries >= 15) setLifeAction('')
      } catch {
        if (tries >= 15) setLifeAction('')
      }
    }, 2000)
    return () => clearInterval(t)
  }, [lifeAction, org?.id, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  const loadConfig = async () => {
    if (!org) return
    try {
      const cfg = await api.get<{ rewrite_rules: string }>(`/v1/organizations/${org.id}/websites/${websiteId}/config`)
      setRules(cfg.rewrite_rules ?? '')
    } catch { /* first load before card mounts */ }
  }

  const saveRules = async () => {
    if (!org) return
    setRulesErr('')
    setRulesBusy(true)
    try {
      await api.put(`/v1/organizations/${org.id}/websites/${websiteId}/config/rewrite`, { rewrite_rules: rules })
      setRulesSaved('Saved — the web server config is being updated.')
      setTimeout(() => setRulesSaved(''), 4000)
    } catch (ex: any) {
      setRulesErr(ex.message)
    } finally {
      setRulesBusy(false)
    }
  }

  const loadUsage = async () => {
    if (!org) return
    setUsageBusy(true)
    try {
      const r = await api.get<SiteUsage>(`/v1/organizations/${org.id}/websites/${websiteId}/usage`)
      setUsage(r)
    } catch { /* transient */ } finally {
      setUsageBusy(false)
    }
  }

  useEffect(() => {
    if (site?.status === 'ready') {
      void loadUsage()
      const t = setInterval(() => void loadUsage(), 30000)
      return () => clearInterval(t)
    }
  }, [org?.id, websiteId, site?.status]) // eslint-disable-line react-hooks/exhaustive-deps

  const memLimit = usage?.memory_limit ?? 0
  const memPct = memLimit > 0 ? ((usage?.memory_bytes ?? 0) / memLimit) * 100 : 0
  const memLimitLabel = memLimit > 0 ? `of ${fmtBytes(memLimit)} strict cap` : 'no memory cap set'
  const diskLimitMB = usage?.disk_limit_mb ?? 0
  const diskPct = diskLimitMB > 0 ? ((usage?.disk_mb ?? 0) / diskLimitMB) * 100 : 0
  const diskLimitLabel = diskLimitMB > 0 ? `of ${fmtBytes(diskLimitMB * 1024 * 1024)} plan allowance` : 'plan disk allowance'
  const cpuCoresLabel = usage?.cpu_limit_cores ? `of ${usage.cpu_limit_cores} core allowance` : 'of one core'

  const updateSite = async (patch: { runtime?: string; runtime_version?: string; web_server?: string; docroot_suffix?: string }) => {
    if (!org || !site) return
    setWsMsg('')
    setErr('')
    setWsBusy(true)
    try {
      await api.patch(`/v1/organizations/${org.id}/websites/${site.id}`, patch)
      setWsMsg('Change queued — the server is being reconciled.')
      setTimeout(() => setWsMsg(''), 4000)
      setTimeout(() => void load(), 800)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setWsBusy(false)
    }
  }

  const tools = site ? [
    { to: `/sites/${site.id}/files`, icon: Folder, label: 'File Manager', desc: 'Browse, edit, upload files' },
    { to: `/sites/${site.id}/dns`, icon: Network, label: 'DNS Zone', desc: 'Records, MX & publishing' },
    { to: `/sites/${site.id}/crons`, icon: Clock, label: 'Cron Jobs', desc: 'Scheduled commands' },
    // Terminal & SSH keys are admin-only (Phase 5): hidden from the customer
    // surface; the server gates both at admin rank as well.
    ...(user?.is_platform_admin ? [{ to: `/sites/${site.id}/ssh-keys`, icon: KeyRound, label: 'SSH Keys', desc: 'Manage SSH access' }] : []),
    ...(user?.is_platform_admin && site.status === 'ready' ? [{ to: `/sites/${site.id}/terminal`, icon: TerminalIcon, label: 'Terminal', desc: 'Shell in browser' }] : []),
    { to: '/databases', icon: DatabaseIcon, label: 'Databases', desc: `${dbs.length} attached` },
    { to: '/backups', icon: History, label: 'Backups', desc: 'Backup & restore' },
    { to: '/security', icon: Lock, label: 'SSL / Security', desc: `${domains.filter((d) => d.ssl_state === 'active').length} active` },
  ] : []

  if (loading) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <SkeletonRows rows={4} />
      </div>
    )
  }
  if (!site) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <Card><EmptyState icon={<Globe size={22} />} title="Site not found" subtitle={err || 'It may have been deleted.'} /></Card>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Link to="/sites" className="icon-btn !h-[34px] !w-[34px]" title="Back to sites"><ArrowLeft size={15} /></Link>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="truncate text-[23px] font-bold tracking-[-.025em] text-ink">{site.primary_domain || site.name}</h1>
            {site.is_staging && <span className="status-chip status-warning">STAGING</span>}
            {site.status === 'suspended' && <span className="status-chip status-warning">SUSPENDED</span>}
            <StatusBadge status={site.status} />
          </div>
          <div className="mt-0.5 text-[11px] text-muted">
            {site.runtime}{site.runtime_version ? ` ${site.runtime_version}` : ''} · {site.web_server} · {site.document_root}
          </div>
        </div>
        <div className="ml-auto flex items-center gap-2">
          {site.runtime === 'php' && site.status === 'ready' && (
            <button className="btn-primary" onClick={() => setShowWP(true)}><Globe size={14} /> Install WordPress</button>
          )}
          {isAdmin && site.status === 'ready' && (
            <button className="btn-ghost" onClick={suspendSite} disabled={lifeBusy} title="Suspend this website">
              <Pause size={13} /> {lifeBusy ? 'Working…' : 'Suspend'}
            </button>
          )}
          {isAdmin && site.status === 'suspended' && (
            <button className="btn-primary" onClick={resumeSite} disabled={lifeBusy} title="Bring the site back online">
              <Play size={13} /> {lifeBusy ? 'Working…' : 'Resume'}
            </button>
          )}
          {isAdmin && (
            <button className="icon-btn !h-[36px] !w-[36px] hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Delete site" onClick={() => setShowDelete(true)}>
              <Trash2 size={15} />
            </button>
          )}
        </div>
      </div>

      {err && <div className="mb-4 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{err}</div>}

      {/* Suspended banner — always visible while the site is suspended */}
      {site.status === 'suspended' && (
        <div className="mb-4 flex flex-wrap items-center gap-2.5 rounded-[10px] border border-[#ffe8b1] bg-warn-soft px-4 py-3 text-[11.5px] font-semibold text-warn">
          <Ban size={15} />
          <span>This website is suspended — the web server and FTP logins are disabled until an administrator resumes it.</span>
          {isAdmin && (
            <button className="btn-primary !min-h-[28px] !px-2.5 !text-[10.5px] ml-auto" onClick={resumeSite} disabled={lifeBusy}>
              <Play size={12} /> Resume now
            </button>
          )}
        </div>
      )}

      {/* Resource usage — ref usage cards */}
      {site.status === 'ready' && (
        <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
          <UsageCard
            tone="green" color="#0f9d6e" icon={<Globe size={13} strokeWidth={1.8} />}
            title="CPU" sub={(usage?.cpu_percent ?? 0) > 0 ? cpuCoresLabel : 'idle right now'}
            value={`${(usage?.cpu_percent ?? 0).toFixed(1)}%`} pct={usage?.cpu_percent ?? 0}
          />
          <UsageCard
            tone="blue" color="#2563eb" icon={<GitBranch size={13} strokeWidth={1.8} />}
            title="Memory" sub={memLimitLabel}
            value={usage ? fmtBytes(usage.memory_bytes) : '—'} pct={memPct}
          />
          <UsageCard
            tone="amber" color="#d88b00" icon={<Folder size={13} strokeWidth={1.8} />}
            title="Disk" sub={diskLimitLabel}
            value={usage ? fmtBytes(usage.disk_mb * 1024 * 1024) : '—'} pct={diskPct}
          />
          <UsageCard
            tone="purple" color="#7c4dff" icon={<FileText size={13} strokeWidth={1.8} />}
            title="Processes" sub={usage?.sampled_at ? `sampled ${timeAgo(usage.sampled_at)}` : 'not sampled yet'}
            value={usage?.processes ?? 0} pct={Math.min(100, (usage?.processes ?? 0) * 2)}
          />
        </div>
      )}

      <div className="mb-4 flex justify-end">
        <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => void loadUsage()} disabled={usageBusy}>
          <RefreshCw size={12} className={usageBusy ? 'animate-spin' : ''} /> Refresh usage
        </button>
      </div>

      {/* Domains + SSL */}
      <Card className="mb-4 overflow-hidden !p-0">
        <CardHeader
          className="mx-4 mt-4 !mb-0"
          title="Domains & SSL"
          subtitle="Certificates and HTTPS per domain"
          right={<Link to="/security" className="text-[11px] font-bold text-brand hover:underline">Manage SSL →</Link>}
        />
        {domains.length === 0 ? (
          <EmptyState icon={<Globe size={20} />} title="No domains" subtitle="The primary domain is attached automatically at creation." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border-collapse">
              <thead>
                <tr>
                  {['Domain', 'Kind', 'SSL', 'Mode', 'DNS', ''].map((h) => (
                    <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {domains.map((d) => (
                  <tr key={d.id}>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex items-center gap-2">
                        <Globe size={13} className="text-brand" />
                        <span className="truncate text-[10.5px] font-bold text-[#243047]">{d.domain}</span>
                      </div>
                    </td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <span className={`status-chip ${d.kind === 'primary' ? 'status-live' : ''}`}>{d.kind}</span>
                    </td>
                    <td className="border-b border-line px-4 py-[11px]">
                      {d.ssl_state === 'active'
                        ? <SslChip expiresAt={d.ssl_expires_at} />
                        : d.ssl_state === 'failed'
                          ? <span className="status-chip status-down">Failed</span>
                          : <span className="status-chip status-warning">Pending</span>}
                      {d.ssl_error && <div className="mt-1 rounded border border-[#ffd0d7] bg-danger-soft px-2 py-1 text-[9.5px] text-danger">{d.ssl_error}</div>}
                    </td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="w-[150px]">
                        <Select
                          value={d.ssl_mode}
                          onChange={(mode) => mode !== d.ssl_mode && void setSslMode(d, mode)}
                          options={[
                            { value: 'none', label: 'No SSL' },
                            { value: 'selfsigned', label: 'Self-signed' },
                            { value: 'letsencrypt', label: "Let's Encrypt" },
                          ]}
                        />
                      </div>
                    </td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex items-center gap-2">
                        {d.dns_verified_at
                          ? <span className="status-chip status-live" title={`Verified ${timeAgo(d.dns_verified_at)}`}>Verified</span>
                          : <span className="status-chip">Unknown</span>}
                        <button
                          className="btn-ghost !min-h-[28px] !px-2 !text-[10px]"
                          onClick={() => void verifyDomainDns(d)}
                          disabled={busyDomain === d.id}
                          title="Check that this domain resolves to the server"
                        >
                          <ShieldQuestion size={12} /> {busyDomain === d.id ? '...' : 'Verify'}
                        </button>
                      </div>
                    </td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex justify-end gap-[5px]">
                        {d.kind === 'alias' && (
                          <button
                            className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                            onClick={() => void removeAlias(d)}
                            title="Delete alias"
                          >
                            <Trash2 size={13} />
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {domains.length > 0 && (
          <div className="border-t border-line bg-[#fbfcfe] px-4 py-3">
            <div className="mb-2 flex items-center gap-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">
              <Plus size={12} /> Add alias
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <input
                className="input h-[34px] w-[220px] text-[11px]"
                value={aliasForm.domain}
                onChange={(e) => setAliasForm({ ...aliasForm, domain: e.target.value })}
                placeholder="www.example.com"
              />
              <input
                className="input h-[34px] w-[190px] font-mono text-[11px]"
                value={aliasForm.docroot_suffix}
                onChange={(e) => setAliasForm({ ...aliasForm, docroot_suffix: e.target.value })}
                placeholder="docroot (optional)"
              />
              <button
                className="btn-primary !min-h-[32px] !px-3 !text-[11px]"
                onClick={addAlias}
                disabled={aliasBusy || !aliasForm.domain}
              >
                {aliasBusy ? 'Adding…' : 'Add alias'}
              </button>
            </div>
          </div>
        )}
      </Card>

      {/* Redirects */}
      <Card className="mb-4 overflow-hidden !p-0">
        <CardHeader className="mx-4 mt-4 !mb-0" title="Redirects" subtitle="Send one of this site's domains to another URL" />
        {redirErr && <div className="mx-4 mt-3 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{redirErr}</div>}
        {redirects.length === 0 ? (
          <EmptyState
            icon={<ArrowUpRight size={20} />}
            title="No redirects"
            subtitle="Point a domain of this site at any other URL, permanently or temporarily."
          />
        ) : (
          <div className="divide-y divide-line">
            {redirects.map((r) => (
              <div key={r.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <ArrowUpRight size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[11px] font-bold text-[#243047]">{r.from_domain}</span>
                    <span className="text-muted">→</span>
                    <span className="truncate font-mono text-[10.5px] text-ink">{r.to_url}</span>
                    <span className="status-chip">{r.status_code}</span>
                    <span className={`status-chip ${r.enabled ? 'status-live' : 'status-warning'}`}>{r.enabled ? 'Enabled' : 'Paused'}</span>
                  </div>
                  <div className="mt-0.5 text-[9.5px] text-muted">created {timeAgo(r.created_at)}</div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    className="icon-btn"
                    onClick={() => void toggleRedirect(r)}
                    title={r.enabled ? 'Pause redirect' : 'Enable redirect'}
                  >
                    {r.enabled ? <Pause size={13} /> : <Play size={13} />}
                  </button>
                  <button
                    className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                    onClick={() => void removeRedirect(r)}
                    title="Delete redirect"
                  >
                    <Trash2 size={13} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
        {domains.length > 0 && (
          <div className="border-t border-line bg-[#fbfcfe] px-4 py-3">
            <div className="mb-2 flex items-center gap-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">
              <Plus size={12} /> Add redirect
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <div className="w-[210px]">
                <Select
                  value={redirForm.from_domain}
                  onChange={(v) => setRedirForm({ ...redirForm, from_domain: v })}
                  placeholder="From domain..."
                  options={domains.map((d) => ({ value: d.domain, label: d.domain }))}
                />
              </div>
              <input
                className="input h-[34px] w-[240px] font-mono text-[11px]"
                value={redirForm.to_url}
                onChange={(e) => setRedirForm({ ...redirForm, to_url: e.target.value })}
                placeholder="https://other.example.com"
              />
              <div className="w-[90px]">
                <Select
                  value={redirForm.status_code}
                  onChange={(v) => setRedirForm({ ...redirForm, status_code: v })}
                  options={[
                    { value: '301', label: '301' },
                    { value: '302', label: '302' },
                  ]}
                />
              </div>
              <button
                className="btn-primary !min-h-[32px] !px-3 !text-[11px]"
                onClick={addRedirect}
                disabled={redirBusy || !redirForm.from_domain || !redirForm.to_url}
              >
                {redirBusy ? 'Adding…' : 'Add redirect'}
              </button>
            </div>
          </div>
        )}
      </Card>

      {/* FTP / SFTP accounts */}
      <Card className="mb-4 overflow-hidden !p-0">
        <CardHeader
          className="mx-4 mt-4 !mb-0"
          title="FTP / SFTP accounts"
          subtitle="Extra logins scoped to a folder of this site"
          right={<button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => setShowFtp(true)}><Plus size={12} /> Add account</button>}
        />
        {ftpErr && <div className="mx-4 mt-3 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{ftpErr}</div>}
        {ftp.length === 0 ? (
          <EmptyState
            icon={<KeyRound size={20} />}
            title="No FTP accounts"
            subtitle="The site's system user already works over SFTP with an SSH key — add extra accounts here."
            action={<button className="btn-brand" onClick={() => setShowFtp(true)}><Plus size={14} /> Add Account</button>}
          />
        ) : (
          <div className="divide-y divide-line">
            {ftp.map((a) => (
              <div key={a.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <KeyRound size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[11px] font-bold text-[#243047]">{a.label}</span>
                    <span className="status-chip">{a.protocol.toUpperCase()}</span>
                    {a.status === 'active' && <span className="status-chip status-live">Active</span>}
                    {a.status === 'pending' && <span className="status-chip">Pending</span>}
                    {a.status === 'failed' && <span className="status-chip status-down" title={a.error_message || undefined}>Failed</span>}
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-2 text-[9.5px] text-muted">
                    <span>login: <span className="font-mono text-sub">{a.user_name}</span></span>
                    {a.home_subdir && <span>· folder: <span className="font-mono">{a.home_subdir}</span></span>}
                    <span>· created {timeAgo(a.created_at)}</span>
                  </div>
                  {a.status === 'failed' && a.error_message && (
                    <div className="mt-1 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-2 py-1 text-[9.5px] font-semibold text-danger">{a.error_message}</div>
                  )}
                </div>
                <div className="flex items-center gap-[5px]">
                  <button className="icon-btn" onClick={() => void revealFtp(a)} disabled={ftpBusyId === a.id} title="Show password">
                    <Eye size={13} />
                  </button>
                  <button className="icon-btn" onClick={() => void rotateFtp(a)} disabled={ftpBusyId === a.id} title="Rotate password">
                    <RefreshCw size={13} />
                  </button>
                  <button
                    className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                    onClick={() => void removeFtp(a)}
                    disabled={ftpBusyId === a.id}
                    title="Delete account"
                  >
                    <Trash2 size={13} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* Web server + runtime */}
      <Card className="mb-4">
        <CardHeader className="mx-4 mt-4 !mb-0" title="Web Server & Runtime" subtitle="Nginx stays the public edge; pick the backend and runtime" />
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <div>
            <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">Web server stack</div>
            <Select
              value={site.web_server}
              onChange={(v) => v !== site.web_server && void updateSite({ web_server: v })}
              options={[
                { value: 'nginx', label: 'Nginx (serves directly)' },
                { value: 'nginx,apache', label: 'Nginx + Apache backend' },
                { value: 'nginx,openlitespeed', label: 'Nginx + OpenLiteSpeed backend' },
              ]}
            />
            <div className="mt-1.5 text-[10px] leading-relaxed text-muted">
              The selected backend runs privately on 127.0.0.1 with a panel-assigned port.
            </div>
          </div>
          <div>
            <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">Runtime</div>
            <div className="flex gap-2">
              <Select
                value={site.runtime}
                onChange={(v) => {
                  if (v === 'static') void updateSite({ runtime: 'static' })
                  else {
                    const first = serverRuntimes.find((r) => r.type === v)
                    void updateSite({ runtime: v, runtime_version: first?.version ?? '' })
                  }
                }}
                options={[
                  { value: 'static', label: 'Static files' },
                  { value: 'php', label: 'PHP' },
                  { value: 'node', label: 'Node.js' },
                  { value: 'python', label: 'Python' },
                  { value: 'go', label: 'Go' },
                ]}
              />
              {site.runtime !== 'static' && (
                <Select
                  key={site.runtime}
                  value={site.runtime_version || ''}
                  onChange={(v) => v && void updateSite({ runtime_version: v })}
                  placeholder="Version"
                  options={serverRuntimes.filter((r) => r.type === site.runtime).map((r) => ({ value: r.version, label: r.version }))}
                />
              )}
            </div>
            <div className="mt-1.5 text-[10px] leading-relaxed text-muted">
              {isAdmin ? <>Install more versions from the <Link to="/software" className="link">Software</Link> page.</> : 'Ask your administrator to install more runtime versions.'}
            </div>
          </div>
          <div className="md:col-span-2">
            <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">Running directory (docroot)</div>
            <div className="flex flex-wrap gap-2">
              <input
                className="input flex-1 font-mono sm:min-w-[240px]"
                value={docrootDraft}
                onChange={(e) => setDocrootDraft(e.target.value)}
                placeholder="public  (relative to the site folder)"
              />
              <button
                className="btn-primary"
                onClick={() => void updateSite({ docroot_suffix: docrootDraft })}
                disabled={wsBusy || docrootDraft.trim() === (site.docroot_suffix ?? '')}
              >
                {wsBusy ? 'Applying…' : 'Apply'}
              </button>
            </div>
            <div className="mt-1.5 text-[10px] leading-relaxed text-muted">
              Framework layouts: Laravel → <code className="rounded bg-surface-2 px-1">public</code>, Symfony → <code className="rounded bg-surface-2 px-1">public</code>, CodeIgniter 4 → <code className="rounded bg-surface-2 px-1">public</code>. Leave empty for the default.
            </div>
            <div className="mt-1 text-[10px] text-sub">
              Currently serving: <code className="rounded bg-surface-2 px-1.5 py-0.5 font-mono">{`/srv/epicpanel/websites/${site.id}/${site.docroot_suffix || 'public'}`}</code>
            </div>
          </div>
        </div>
        {wsMsg && <div className="mt-3 rounded-[9px] border border-[#cef0e1] bg-ok-soft px-3 py-2 text-[11px] font-semibold text-ok">{wsMsg}</div>}
      </Card>

      {/* Rewrite rules */}
      <Card className="mb-4">
        <CardHeader className="mx-4 mt-4 !mb-0" title="Rewrite Rules & Custom Config" subtitle="nginx directives applied to every server block of this site" />
        <p className="mb-3 text-[11px] leading-relaxed text-muted">
          Allowed: <code className="rounded bg-surface-2 px-1">rewrite</code>, <code className="rounded bg-surface-2 px-1">return</code>, <code className="rounded bg-surface-2 px-1">if</code>, <code className="rounded bg-surface-2 px-1">try_files</code>, <code className="rounded bg-surface-2 px-1">add_header</code>, <code className="rounded bg-surface-2 px-1">deny</code>, <code className="rounded bg-surface-2 px-1">allow</code>, <code className="rounded bg-surface-2 px-1">expires</code>, <code className="rounded bg-surface-2 px-1">error_page</code>, <code className="rounded bg-surface-2 px-1">client_max_body_size</code>.
        </p>
        <textarea
          className="input min-h-[120px] font-mono text-[12px]"
          value={rules}
          onChange={(e) => setRules(e.target.value)}
          placeholder={'# e.g. serve .php for legacy .html URLs:\nlocation ~ \\.html$ {\n\trewrite ^(.*)\\.html$ $1.php last;\n}'}
        />
        {rulesErr && <div className="mt-2 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{rulesErr}</div>}
        {rulesSaved && <div className="mt-2 rounded-[9px] border border-[#cef0e1] bg-ok-soft px-3 py-2 text-[11px] font-semibold text-ok">{rulesSaved}</div>}
        <div className="mt-3 flex justify-end">
          <button className="btn-primary" onClick={saveRules} disabled={rulesBusy}>
            {rulesBusy ? 'Applying…' : 'Save & Apply'}
          </button>
        </div>
      </Card>

      {/* App process */}
      {(site.runtime === 'node' || site.runtime === 'python' || site.runtime === 'go') && (
        <Card className="mb-4">
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title={(site.runtime === 'node' ? 'Node.js' : site.runtime === 'python' ? 'Python' : 'Go') + ' Application Process'}
            subtitle="Supervised process with reverse proxy, build pipeline and logs"
            right={<Link to={`/sites/${site.id}/application`} className="text-[11px] font-bold text-brand hover:underline">Manage Application →</Link>}
          />
          <MiniItem
            tone="blue"
            icon={<GitBranch size={14} strokeWidth={1.8} />}
            title={`${site.runtime === 'node' ? 'Node.js' : site.runtime === 'python' ? 'Python' : 'Go'} ${site.runtime_version} application`}
            sub="Supervisor-managed, proxied through nginx"
            right={<Link to={`/sites/${site.id}/application`} className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]">Manage →</Link>}
          />
        </Card>
      )}

      {/* Feature grid — ref "All features" pattern */}
      <Card className="mb-4 overflow-hidden !p-0">
        <CardHeader className="mx-4 mt-4 !mb-0" title="Site Tools" subtitle="Everything for this website" />
        <div className="grid grid-cols-2 gap-2.5 p-4 sm:grid-cols-4">
          {tools.map((t) => {
            const Icon = t.icon
            return (
              <Link
                key={t.to} to={t.to}
                className="flex items-center gap-2.5 rounded-[10px] border border-line bg-surface-2 p-3 transition hover:border-[#ced9f7] hover:bg-white"
              >
                <div className="grid h-[34px] w-[34px] flex-none place-items-center rounded-[8px] border border-line bg-white text-brand">
                  <Icon size={15} strokeWidth={1.8} />
                </div>
                <div className="min-w-0">
                  <strong className="block truncate text-[10.5px] text-ink">{t.label}</strong>
                  <span className="block truncate text-[8.5px] text-muted">{t.desc}</span>
                </div>
              </Link>
            )
          })}
        </div>
      </Card>

      {/* Attached databases */}
      <Card>
        <CardHeader className="mx-4 mt-4 !mb-0" title="Attached Databases" right={<Link to="/databases" className="text-[11px] font-bold text-brand hover:underline">All databases →</Link>} />
        {dbs.length === 0 ? (
          <EmptyState icon={<DatabaseIcon size={20} />} title="No databases attached" subtitle="Create one from the Databases page and attach it here." />
        ) : (
          <div className="divide-y divide-line">
            {dbs.map((db) => (
              <div key={db.id} className="flex items-center gap-3 py-3">
                <div className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-surface-2 text-[#667085]"><DatabaseIcon size={14} strokeWidth={1.8} /></div>
                <span className="min-w-0 flex-1 truncate font-mono text-[11px] font-bold text-[#243047]">{db.name}</span>
                <span className="status-chip">{db.engine}</span>
                <StatusBadge status={db.status} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <WordPressModal
        open={showWP}
        onClose={() => setShowWP(false)}
        orgId={org?.id ?? ''}
        websiteId={site.id}
        siteName={site.name}
        domain={site.primary_domain}
        onQueued={() => void load()}
      />

      <Modal open={showFtp} onClose={() => setShowFtp(false)} title="Add FTP / SFTP Account" subtitle="Runs as the site's system user, optionally jailed to a subfolder.">
        <ErrorNote message={ftpErr} />
        <Field label="Protocol">
          <Select
            value={ftpForm.protocol}
            onChange={(v) => setFtpForm({ ...ftpForm, protocol: v })}
            options={[
              { value: 'ftp', label: 'FTP (password login)' },
              { value: 'sftp', label: 'SFTP (password login)' },
            ]}
          />
        </Field>
        <Field label="Label"><input className="input" value={ftpForm.label} onChange={(e) => setFtpForm({ ...ftpForm, label: e.target.value })} placeholder="designer" /></Field>
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <Field label="Password (optional)" hint="Leave empty to let the panel generate one.">
            <input className="input" type="password" value={ftpForm.password} onChange={(e) => setFtpForm({ ...ftpForm, password: e.target.value })} autoComplete="new-password" />
          </Field>
          <Field label="Home subfolder (optional)" hint="Relative to the site's folder, e.g. www/shop.">
            <input className="input font-mono" value={ftpForm.home_subdir} onChange={(e) => setFtpForm({ ...ftpForm, home_subdir: e.target.value })} placeholder="www/shop" />
          </Field>
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowFtp(false)}>Cancel</button>
          <button className="btn-brand" onClick={createFtp} disabled={ftpBusy || !ftpForm.label}>
            {ftpBusy ? 'Creating...' : 'Create Account'}
          </button>
        </div>
      </Modal>

      <Modal
        open={!!ftpReveal}
        onClose={() => setFtpReveal(null)}
        title={ftpReveal?.once ? 'Password Created' : `Password for ${ftpReveal?.label ?? ''}`}
        subtitle={ftpReveal?.once ? 'Copy it now — it will not be shown again.' : 'Each reveal is audited.'}
      >
        {ftpReveal && (
          <div className="space-y-3">
            {ftpReveal.once && (
              <p className="rounded-[9px] border border-[#ffe8b1] bg-warn-soft px-3 py-2 text-[11px] font-semibold text-warn">
                This password is shown only once. Store it in your password manager now.
              </p>
            )}
            <div className="flex items-center justify-between gap-2 rounded-[9px] border border-line bg-surface-2 px-3 py-2.5">
              <code className="min-w-0 flex-1 break-all font-mono text-[11.5px] font-semibold text-ink">{ftpReveal.password}</code>
              <button
                className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]"
                onClick={() => void navigator.clipboard?.writeText(ftpReveal.password).catch(() => {})}
              >
                <Copy size={12} /> Copy
              </button>
            </div>
            <div className="flex justify-end pt-1">
              <button className="btn-ghost" onClick={() => setFtpReveal(null)}><X size={13} /> Close</button>
            </div>
          </div>
        )}
      </Modal>

      <Modal open={showDelete} onClose={() => setShowDelete(false)} title="Delete Website" subtitle="This action cannot be undone.">
        <p className="mb-4 text-[11.5px] leading-relaxed text-muted">
          Delete <b className="text-ink">{site.primary_domain || site.name}</b>? Files, cron jobs and the vhost are removed. Databases are kept.
        </p>
        <div className="flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowDelete(false)}>Cancel</button>
          <button className="btn-danger" onClick={deleteSite}>Delete</button>
        </div>
      </Modal>
    </div>
  )
}

export function humanAction(action: string): string {
  return action
    .split(/[._]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

// "expires in N days" chip for an active certificate (shared look with Security).
function SslChip({ expiresAt }: { expiresAt?: string }) {
  if (!expiresAt) return <span className="status-chip status-live">Active</span>
  const days = Math.floor((new Date(expiresAt).getTime() - Date.now()) / 86400000)
  const label = days < 0 ? 'Expired' : `Active · ${days}d left`
  return <span className={`status-chip ${days < 0 ? 'status-down' : days <= 14 ? 'status-warning' : 'status-live'}`}>{label}</span>
}
