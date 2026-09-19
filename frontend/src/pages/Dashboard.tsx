import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Globe, Database as DatabaseIcon, HardDrive, Plus, Folder, Lock, History,
  Server as ServerIcon, ArrowRight, RotateCcw, ShieldAlert, Clock, Users,
  CircleCheck, ChevronRight, Activity as ActivityIcon2,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, MiniItem, QuickAction, AreaChart, ChartControls, Initials } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { FreshnessBadge } from '@/components/FreshnessBadge'
import { fmtBytes, timeAgo } from '@/lib/types'
import type { Website, Server, Database, AuditEntry, Alert } from '@/lib/types'
import {
  useMetrics, useFreshness, seedFrames, normalizeBatch, primaryDisk,
} from '@/lib/metrics'
import type { SnapshotFrame } from '@/lib/metrics'

interface DashData {
  websites: Website[]
  servers: Server[]
  databases: Database[]
  history: { collected_at: string; cpu_percent: number; memory_used: number; memory_total: number; disk_used: number; disk_total: number }[]
  activity: AuditEntry[]
  alerts: Alert[]
}

const QUICK_ACTIONS = [
  { icon: <Globe size={15} strokeWidth={1.8} />, label: 'Sites', desc: 'Create and manage websites', to: '/sites', tone: 'blue' as const },
  { icon: <Folder size={15} strokeWidth={1.8} />, label: 'File Manager', desc: 'Upload, edit and manage files', to: '/files', tone: 'green' as const },
  { icon: <DatabaseIcon size={15} strokeWidth={1.8} />, label: 'Databases', desc: 'MySQL, MariaDB, PostgreSQL', to: '/databases', tone: 'purple' as const },
  { icon: <Lock size={15} strokeWidth={1.8} />, label: 'SSL / TLS', desc: 'Protect your websites', to: '/security', tone: 'green' as const },
  { icon: <History size={15} strokeWidth={1.8} />, label: 'Backups', desc: 'Restore points & schedules', to: '/backups', tone: 'amber' as const },
  { icon: <ActivityIcon2 size={15} strokeWidth={1.8} />, label: 'Activity', desc: 'Audit trail and alerts', to: '/activity', tone: 'blue' as const },
]

export function DashboardPage() {
  const { user, org } = useAuth()
  const [data, setData] = useState<DashData | null>(null)
  const { frames } = useMetrics()
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [createErr, setCreateErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', primary_domain: '' })
  const [range, setRange] = useState('24h')

  const load = async () => {
    if (!org) return
    setLoading(true)
    try {
      const base = `/v1/organizations/${org.id}`
      const [ws, sv, dbs] = await Promise.all([
        api.get<{ websites: Website[] }>(`${base}/websites`),
        api.get<{ servers: Server[] }>(`${base}/servers`),
        api.get<{ databases: Database[] }>(`${base}/databases`).catch(() => ({ databases: [] as Database[] })),
      ])
      const websites = ws.websites ?? []
      const servers = sv.servers ?? []
      let history: DashData['history'] = []
      if (servers.length > 0) {
        history = await api
          .get<{ metrics: DashData['history'] }>(`${base}/servers/${servers[0].id}/metrics/history`)
          .then((r) => (r.metrics ?? []).slice().reverse())
          .catch(() => [])
      }
      // Initial REST paint for the live layer; WS pushes take over after.
      await api
        .get<{ metrics: unknown }>(`${base}/servers/metrics`)
        .then((r) => seedFrames(normalizeBatch(r)))
        .catch(() => undefined)
      const [act, al] = await Promise.all([
        api.get<{ logs: AuditEntry[] }>(`/v1/audit-logs?organization_id=${org.id}`).catch(() => ({ logs: [] as AuditEntry[] })),
        api.get<{ alerts: Alert[] }>(`${base}/alerts`).catch(() => ({ alerts: [] as Alert[] })),
      ])
      setData({ websites, servers, databases: dbs.databases ?? [], history, activity: (act.logs ?? []).slice(0, 5), alerts: al.alerts ?? [] })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const createSite = async () => {
    if (!org) return
    setCreateErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: form.name.toLowerCase() }
      if (form.primary_domain) body.primary_domain = form.primary_domain.toLowerCase()
      await api.post(`/v1/organizations/${org.id}/websites`, body)
      setShowCreate(false)
      setForm({ name: '', primary_domain: '' })
      await load()
    } catch (ex: any) {
      setCreateErr(ex.message ?? 'Failed to create website')
    } finally {
      setBusy(false)
    }
  }

  const websites = data?.websites ?? []
  const prodWebsites = websites.filter((w) => !w.is_staging)
  const dbs = data?.databases ?? []
  const servers = data?.servers ?? []
  // First server whose frame reports a non-offline node; falls back to the
  // first server overall, then to server.status when no frame exists.
  const primaryFrame: SnapshotFrame | null =
    servers.map((s) => frames[s.id] ?? null).find((f) => f && f.node_state !== 'OFFLINE') ??
    servers.map((s) => frames[s.id] ?? null).find((f) => f !== null) ??
    null
  const primaryFresh = useFreshness(primaryFrame)
  const primaryLive = primaryFrame !== null && primaryFresh.state !== 'OFFLINE'
  const node = primaryFrame?.sample?.node
  const disk = primaryDisk(node)
  const online = servers.filter((s) => {
    const f = frames[s.id]
    return (f ? f.node_state !== 'OFFLINE' : s.status === 'online')
  }).length
  const storageUsed = disk?.used_bytes ?? 0
  const storageTotal = disk?.total_bytes ?? 0
  const alerts = (data?.alerts ?? []).filter((a) => !a.resolved_at)

  /* Chart data from real metrics history */
  const chart = useMemo(() => {
    const pts = data?.history ?? []
    if (pts.length === 0) return null
    let filtered = pts
    if (range === '24h') {
      const cutoff = Date.now() - 24 * 3600 * 1000
      filtered = pts.filter((p) => new Date(p.collected_at).getTime() >= cutoff)
    } else if (range === '7d') {
      const cutoff = Date.now() - 7 * 24 * 3600 * 1000
      filtered = pts.filter((p) => new Date(p.collected_at).getTime() >= cutoff)
    }
    if (filtered.length === 0) filtered = pts.slice(-20)
    // Downsample to ~24 points
    const step = Math.max(1, Math.ceil(filtered.length / 24))
    const sampled = filtered.filter((_, i) => i % step === 0 || i === filtered.length - 1)
    const label = (p: DashData['history'][number]) => {
      const d = new Date(p.collected_at)
      return range === '30d'
        ? `${d.getDate()}/${d.getMonth() + 1}`
        : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
    const memPct = (p: DashData['history'][number]) =>
      p.memory_total > 0 ? (p.memory_used / p.memory_total) * 100 : 0
    const diskPct = (p: DashData['history'][number]) =>
      p.disk_total > 0 ? (p.disk_used / p.disk_total) * 100 : 0
    return {
      categories: sampled.map(label),
      series: [
        { name: 'CPU', data: sampled.map((p) => p.cpu_percent) },
        { name: 'Memory', data: sampled.map(memPct) },
        { name: 'Disk', data: sampled.map(diskPct) },
      ],
    }
  }, [data?.history, range])

  const attention: { icon: React.ReactNode; title: string; sub: string; to?: string; tone: 'red' | 'amber' | 'blue' | 'green'; chip?: string }[] = [
    ...(alerts.length > 0 ? [{
      icon: <ShieldAlert size={14} strokeWidth={1.8} />, title: `${alerts.length} alert${alerts.length > 1 ? 's' : ''} need attention`,
      sub: alerts[0]?.message ?? '', to: '/activity', tone: 'red' as const,
    }] : []),
    ...(user?.is_platform_admin && servers.some((s) => {
      const f = frames[s.id]
      return (f ? f.node_state === 'OFFLINE' : s.status !== 'online')
    }) ? [{
      icon: <ServerIcon size={14} strokeWidth={1.8} />, title: 'Some servers are offline',
      sub: `${servers.length - online} of ${servers.length} not reachable`, to: '/servers', tone: 'amber' as const,
    }] : []),
    ...(websites.some((w) => w.status === 'failed') ? [{
      icon: <Globe size={14} strokeWidth={1.8} />, title: 'A website failed to provision',
      sub: 'Check the site detail page for the error', to: '/sites', tone: 'red' as const,
    }] : []),
    {
      icon: <CircleCheck size={14} strokeWidth={1.8} />, title: 'Panel operational',
      sub: `Web, databases and agent queue healthy`, tone: 'green' as const,
    },
  ].slice(0, 4)

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title={`Server overview`}
        subtitle="Everything important about your hosting platform in one place."
        actions={
          <>
            <Link to="/activity" className="btn-ghost"><ActivityIcon2 size={14} /> Activity</Link>
            {user?.is_platform_admin && (
              <button className="btn-primary" onClick={() => setShowCreate(true)}><Plus size={14} /> Create site</button>
            )}
          </>
        }
      />

      {/* Beta notice */}
      {user?.is_platform_admin && servers.length === 0 && !loading && (
        <div className="mb-6 flex flex-col gap-3 rounded-[12px] border border-[#ffe8b1] bg-warn-soft p-4 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 className="text-[13px] font-bold text-ink">Connect your first server</h2>
            <p className="mt-1 max-w-[640px] text-[11px] leading-relaxed text-sub">
              EpicHost runs on your own infrastructure. Register a server, install the agent and start hosting in minutes.
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-3">
            <Link to="/servers" className="btn-primary">Connect a Server</Link>
          </div>
        </div>
      )}

      {/* Stats grid */}
      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard
          icon={<Globe size={17} strokeWidth={1.8} />} tone="blue" label="Websites"
          value={loading ? '—' : prodWebsites.length} linkTo="/sites" loading={loading}
          change={websites.some((w) => w.is_staging) ? `${websites.length - prodWebsites.length} staging` : undefined}
        />
        <StatCard
          icon={<DatabaseIcon size={17} strokeWidth={1.8} />} tone="purple" label="Databases"
          value={loading ? '—' : dbs.length} linkTo="/databases" loading={loading}
        />
        <StatCard
          icon={<ServerIcon size={17} strokeWidth={1.8} />} tone="green" label="Servers online"
          value={loading ? '—' : `${online}/${servers.length}`} linkTo={user?.is_platform_admin ? '/servers' : undefined} loading={loading}
        />
        <StatCard
          icon={<HardDrive size={17} strokeWidth={1.8} />} tone="amber" label="Disk used"
          value={loading ? '—' : storageTotal ? fmtBytes(storageUsed) : '—'} loading={loading}
          change={storageTotal ? `${Math.round((storageUsed / storageTotal) * 100)}% of ${fmtBytes(storageTotal)}` : undefined}
        >
          <div className="mt-2.5">
            <div className="h-[7px] w-full overflow-hidden rounded-full bg-line-soft">
              <div className="h-full rounded-full bg-brand transition-all" style={{ width: `${storageTotal ? Math.min(100, (storageUsed / storageTotal) * 100) : 0}%` }} />
            </div>
          </div>
        </StatCard>
      </div>

      {/* Quick actions */}
      <div className="mb-4 grid grid-cols-2 gap-2.5 sm:grid-cols-3 xl:grid-cols-6">
        {QUICK_ACTIONS.map((a) => <QuickAction key={a.label} {...a} />)}
      </div>

      {/* Chart + server health */}
      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
        <Card>
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="Platform performance"
            subtitle="CPU, memory and disk usage over time"
            right={<ChartControls value={range} onChange={setRange} options={[{ label: '24h', value: '24h' }, { label: '7d', value: '7d' }, { label: '30d', value: '30d' }]} />}
          />
          <div className="min-h-[260px] px-1 pb-2">
            {loading ? (
              <div className="skeleton h-[260px] w-full" />
            ) : chart ? (
              <AreaChart categories={chart.categories} series={chart.series} />
            ) : (
              <EmptyState
                icon={<ActivityIcon2 size={20} />}
                title="No metrics yet"
                subtitle={servers.length ? 'Waiting for the agent to report the first metrics.' : 'Connect a server to start collecting metrics.'}
              />
            )}
          </div>
        </Card>

        <Card>
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="Server health"
            subtitle="Live resource pressure"
            right={
              <div className="flex items-center gap-2">
                {user?.is_platform_admin && <Link to="/servers" className="icon-btn" title="Manage servers" aria-label="Manage servers"><ServerIcon size={14} /></Link>}
              </div>
            }
          />
          {loading ? (
            <SkeletonRows rows={3} height="h-12" />
            ) : servers.length === 0 ? (
              <EmptyState
                icon={<ServerIcon size={20} />}
                title="No servers yet"
                subtitle={user?.is_platform_admin ? 'Connect one from the Servers page.' : 'Your hosting provider connects servers for you.'}
              />
          ) : (
            <div>
              {node && (
                <div className="mb-4 space-y-3.5">
                  <ResourceBar label="CPU usage" value={`${Math.round(node.cpu_percent)}%`} pct={node.cpu_percent} color="#2563eb" />
                  <ResourceBar
                    label="Memory"
                    value={node.memory_total_bytes ? `${fmtBytes(node.memory_used_bytes)} / ${fmtBytes(node.memory_total_bytes)}` : '—'}
                    pct={node.memory_total_bytes ? (node.memory_used_bytes / node.memory_total_bytes) * 100 : 0}
                    color="#0f9d6e"
                  />
                  <ResourceBar
                    label="Storage"
                    value={disk && disk.total_bytes ? `${fmtBytes(disk.used_bytes)} / ${fmtBytes(disk.total_bytes)}` : '—'}
                    pct={disk && disk.total_bytes ? (disk.used_bytes / disk.total_bytes) * 100 : 0}
                    color="#7c4dff"
                  />
                </div>
              )}
              <div className="h-px bg-line" />
              <div className="mt-4 space-y-2.5">
                {servers.map((s) => {
                  const f = frames[s.id]
                  const isOnline = f ? f.node_state !== 'OFFLINE' : s.status === 'online'
                  return (
                    <MiniItem
                      key={s.id}
                      icon={<ServerIcon size={14} strokeWidth={1.8} />}
                      title={s.name}
                      sub={`${s.hostname || '—'} · last seen ${timeAgo(s.last_seen_at)}`}
                      right={<span className={`status-chip ${isOnline ? 'status-live' : 'status-down'}`}>{isOnline ? 'Healthy' : 'Offline'}</span>}
                    />
                  )
                })}
              </div>
            </div>
          )}
        </Card>
      </div>

      <div className="h-4" />

      {/* Sites + attention */}
      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
        <Card className="overflow-hidden !p-0">
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="Recent websites"
            subtitle="Latest sites and their status"
            right={<Link to="/sites" className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]">View all</Link>}
          />
          {loading ? (
            <div className="p-5"><SkeletonRows rows={3} /></div>
          ) : websites.length === 0 ? (
            <EmptyState
              icon={<Globe size={22} />} title="No websites yet" subtitle="Create your first website to get started."
              action={user?.is_platform_admin ? <button className="btn-brand" onClick={() => setShowCreate(true)}><Plus size={14} /> Create Website</button> : undefined}
            />
          ) : (
            <div className="divide-y divide-line">
              {websites.slice(0, 5).map((w) => (
                <Link key={w.id} to={`/sites/${w.id}`} className="flex items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                  <Initials text={w.primary_domain || w.name} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[11px] font-bold text-[#243047]">{w.primary_domain || `${w.name}.epichost.local`}</div>
                    <div className="truncate text-[9px] text-muted">
                      {w.runtime}{w.runtime_version ? ` ${w.runtime_version}` : ''} · created {timeAgo(w.created_at)}
                    </div>
                  </div>
                  <StatusBadge status={w.status} />
                  <ArrowRight size={13} className="shrink-0 text-[#98a2b3]" />
                </Link>
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader className="mx-4 mt-4 !mb-0" title="Attention needed" subtitle="Items worth checking today" />
          <div className="space-y-3">
            {attention.map((a, i) => (
              <MiniItem
                key={i}
                tone={a.tone}
                icon={a.icon}
                title={a.title}
                sub={a.sub}
                right={
                  a.to ? (
                    <Link to={a.to} className="icon-btn" title="View details" aria-label="View details"><ChevronRight size={13} /></Link>
                  ) : (
                    <span className="status-chip status-live">OK</span>
                  )
                }
              />
            ))}
            {dbs.length === 0 && (
              <MiniItem
                tone="blue"
                icon={<DatabaseIcon size={14} strokeWidth={1.8} />}
                title="No databases yet"
                sub="Attach one to a site from Databases"
                right={<Link to="/databases" className="icon-btn" title="Open databases" aria-label="Open databases"><ChevronRight size={13} /></Link>}
              />
            )}
          </div>
        </Card>
      </div>

      {/* Tools accordion */}
      <section className="mt-6">
        <h2 className="mb-2.5 text-[15px] font-bold tracking-[-.01em] text-ink">All tools</h2>
        <ToolAccordion />
      </section>

      <CreateSiteModal
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onCreate={createSite}
        form={form}
        setForm={setForm}
        err={createErr}
        busy={busy}
      />
    </div>
  )
}

/* Tool accordion (shared dashboard section) */
const TOOLS = [
  { icon: Globe, label: 'Website Management', to: '/sites', desc: 'List, create and manage websites, runtimes and web servers.' },
  { icon: DatabaseIcon, label: 'Databases', to: '/databases', desc: 'Create, link and manage MySQL, MariaDB and PostgreSQL databases.' },
  { icon: Folder, label: 'File Manager', to: '/files', desc: 'Browse, edit and upload files on any of your sites.' },
  { icon: History, label: 'Backups', to: '/backups', desc: 'Manual and scheduled backups with retention and one-click restore.' },
  { icon: Lock, label: 'SSL / TLS', to: '/security', desc: "Self-signed and Let's Encrypt certificates per domain." },
  { icon: Users, label: 'Users', to: '/team', desc: 'Organization members and their roles.' },
]

function ToolAccordion() {
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div>
      {TOOLS.map((t) => {
        const Icon = t.icon
        const isOpen = open === t.label
        return (
          <div key={t.label} className="mb-1.5">
            <button className="tool w-full" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : t.label)}>
              <span className="flex items-center gap-2.5 text-[12.5px] font-semibold text-ink">
                <Icon size={15} className="text-[#39465a]" /> {t.label}
              </span>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
                className={`text-[#98a2b3] transition-transform duration-200 ${isOpen ? 'rotate-180' : ''}`}>
                <path d="m6 9 6 6 6-6" />
              </svg>
            </button>
            {isOpen && (
              <div className="tool-panel flex items-center justify-between gap-3">
                <span>{t.desc}</span>
                <Link to={t.to} className="btn-brand shrink-0">Open</Link>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function ResourceBar({ label, value, pct, color }: { label: string; value: string; pct: number; color: string }) {
  return (
    <div>
      <div className="mb-[7px] flex items-baseline justify-between gap-3">
        <strong className="text-[11px] text-ink">{label}</strong>
        <span className="text-[10px] font-bold text-muted">{value}</span>
      </div>
      <div className="h-[7px] w-full overflow-hidden rounded-full bg-line-soft">
        <div className="h-full rounded-full transition-all" style={{ width: `${Math.max(2, Math.min(100, pct))}%`, background: color }} />
      </div>
    </div>
  )
}

function StatCard({ icon, value, label, linkTo, loading, change, tone = 'blue', children }: {
  icon: React.ReactNode
  value: React.ReactNode
  label: string
  linkTo?: string
  loading?: boolean
  change?: string
  tone?: 'blue' | 'green' | 'amber' | 'purple' | 'red'
  children?: React.ReactNode
}) {
  const toneCls: Record<string, string> = {
    blue: 'bg-brand-soft text-brand',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    purple: 'bg-purple-soft text-purple',
    red: 'bg-danger-soft text-danger',
  }
  const inner = (
    <div className="card min-w-0 p-[15px]">
      <div className="flex items-start justify-between gap-2.5">
        <div className="min-w-0">
          <div className="text-[10px] font-bold uppercase tracking-[.06em] text-muted">{label}</div>
          {loading ? <div className="skeleton mt-2 h-7 w-16" /> : (
            <div className="mt-[5px] text-[24px] font-extrabold leading-[1.1] tracking-[-.04em] text-ink">{value}</div>
          )}
          {change && <div className="mt-2 text-[10px] font-bold text-muted">{change}</div>}
        </div>
        <div className={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px] ${toneCls[tone]}`}>{icon}</div>
      </div>
      {children}
      {linkTo && <Link to={linkTo} className="mt-3 inline-flex items-center gap-1 text-[11px] font-bold text-brand hover:underline">View all <ArrowRight size={12} /></Link>}
    </div>
  )
  return linkTo ? <Link to={linkTo} className="transition hover:shadow-pop">{inner}</Link> : inner
}

export function humanAction(action: string): string {
  return action
    .split(/[._]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

function CreateSiteModal({ open, onClose, onCreate, form, setForm, err, busy }: {
  open: boolean
  onClose: () => void
  onCreate: () => void
  form: { name: string; primary_domain: string }
  setForm: (f: { name: string; primary_domain: string }) => void
  err: string
  busy: boolean
}) {
  return (
    <Modal open={open} onClose={onClose} title="Create Website" subtitle="Provision a site in under a minute.">
      <ErrorNote message={err} />
      <Field label="Site name" hint="Lowercase letters, digits and dashes.">
        <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="my-project" />
      </Field>
      <Field label="Primary domain (optional)">
        <input className="input" value={form.primary_domain} onChange={(e) => setForm({ ...form, primary_domain: e.target.value })} placeholder="example.com" />
      </Field>
      <Field label="Runtime">
        <Select value="static" onChange={() => undefined} options={[{ value: 'static', label: 'Static site (HTML)' }]} />
      </Field>
      <button className="btn-brand w-full justify-center" onClick={onCreate} disabled={busy || !form.name}>
        {busy ? 'Creating...' : 'Create Website'}
      </button>
    </Modal>
  )
}
