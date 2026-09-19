import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Globe, Database as DatabaseIcon, HardDrive, Gauge, Activity as ActivityIcon,
  FolderOpen, Network, Lock, History, ArrowRight, ShieldAlert, CircleCheck,
  Users, Server as ServerIcon,
} from 'lucide-react'
import { api, useAuth, useMetrics, useFreshness, seedFrames, normalizeBatch, primaryDisk } from '@epicpanel/core'
import type { Website, Server, Database, AuditEntry, Alert, SnapshotFrame } from '@epicpanel/core'
import { Card, CardHeader, StatusBadge, EmptyState, SkeletonRows, PageTitle, MiniItem, QuickAction, UsageCard, Initials, FeatureTile } from '@epicpanel/ui'
import { AreaChart, ChartControls } from '@epicpanel/charts'
import { FreshnessBadge } from '@epicpanel/ui'
import { fmtBytes, timeAgo } from '@epicpanel/core'

interface DashData {
  websites: Website[]
  servers: Server[]
  databases: Database[]
  history: { collected_at: string; cpu_percent: number; memory_used: number; memory_total: number; disk_used: number; disk_total: number }[]
  activity: AuditEntry[]
  alerts: Alert[]
  pkg: { name: string } | null
}

const QUICK_ACTIONS = [
  { icon: <FolderOpen size={15} strokeWidth={1.8} />, label: 'File Manager', desc: 'Upload, edit and manage files', to: '/files', tone: 'green' as const },
  { icon: <DatabaseIcon size={15} strokeWidth={1.8} />, label: 'Databases', desc: 'Create MySQL databases', to: '/databases', tone: 'purple' as const },
  { icon: <Network size={15} strokeWidth={1.8} />, label: 'Domains', desc: 'Add domains and subdomains', to: '/domains', tone: 'amber' as const },
  { icon: <Lock size={15} strokeWidth={1.8} />, label: 'SSL / TLS', desc: 'Protect your websites', to: '/ssl', tone: 'green' as const },
  { icon: <History size={15} strokeWidth={1.8} />, label: 'Backups', desc: 'Restore your hosting data', to: '/backups', tone: 'blue' as const },
  { icon: <ActivityIcon size={15} strokeWidth={1.8} />, label: 'Metrics', desc: 'Usage over time', to: '/metrics', tone: 'blue' as const },
]

const FEATURES = [
  { icon: <Globe size={14} strokeWidth={1.8} />, label: 'Websites', desc: 'Sites & applications', to: '/websites' },
  { icon: <Network size={14} strokeWidth={1.8} />, label: 'Domains', desc: 'Domains & subdomains', to: '/domains' },
  { icon: <FolderOpen size={14} strokeWidth={1.8} />, label: 'File Manager', desc: 'Manage hosting files', to: '/files' },
  { icon: <KeyRoundIcon />, label: 'FTP Accounts', desc: 'FTP & SFTP users', to: '/ftp' },
  { icon: <DatabaseIcon size={14} strokeWidth={1.8} />, label: 'Databases', desc: 'Databases & phpMyAdmin', to: '/databases' },
  { icon: <Network size={14} strokeWidth={1.8} />, label: 'DNS Zone', desc: 'Manage DNS records', to: '/dns' },
  { icon: <Lock size={14} strokeWidth={1.8} />, label: 'SSL / TLS', desc: 'Certificates & HTTPS', to: '/ssl' },
  { icon: <History size={14} strokeWidth={1.8} />, label: 'Backups', desc: 'Restore points', to: '/backups' },
]

function KeyRoundIcon() {
  return <ServerIcon size={14} strokeWidth={1.8} />
}

export function humanAction(action: string): string {
  return action
    .split(/[._]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

export function DashboardPage() {
  const { user, org } = useAuth()
  const [data, setData] = useState<DashData | null>(null)
  const { frames } = useMetrics()
  const [loading, setLoading] = useState(true)
  const [range, setRange] = useState('24h')

  const load = async () => {
    if (!org) return
    setLoading(true)
    try {
      const base = `/v1/organizations/${org.id}`
      const [ws, sv, dbs, pkg] = await Promise.all([
        api.get<{ websites: Website[] }>(`${base}/websites`),
        api.get<{ servers: Server[] }>(`${base}/servers`),
        api.get<{ databases: Database[] }>(`${base}/databases`).catch(() => ({ databases: [] as Database[] })),
        api.get<{ package: { name: string } | null }>(`${base}/package`).catch(() => ({ package: null })),
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
      setData({
        websites,
        servers,
        databases: dbs.databases ?? [],
        history,
        activity: (act.logs ?? []).slice(0, 6),
        alerts: al.alerts ?? [],
        pkg: pkg.package ?? null,
      })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const websites = data?.websites ?? []
  const dbs = data?.databases ?? []
  const servers = data?.servers ?? []
  // First server frame that is not offline; fall back to the first frame.
  const primaryFrame: SnapshotFrame | null =
    servers.map((s) => frames[s.id] ?? null).find((f) => f && f.node_state !== 'OFFLINE') ??
    servers.map((s) => frames[s.id] ?? null).find((f) => f !== null) ??
    null
  const fresh = useFreshness(primaryFrame)
  const node = primaryFrame?.sample?.node
  const disk = primaryDisk(node)
  const storageUsed = disk?.used_bytes ?? 0
  const storageTotal = disk?.total_bytes ?? 0

  // Live per-site rollup (own cgroups only, from the WS frame).
  const mySites = websites.filter((w) => w.status !== 'deleted' && w.status !== 'deleting')
  const siteSamples = useMemo(() => {
    const list = primaryFrame?.sample?.sites ?? []
    return mySites
      .map((w) => ({ site: w, sample: list.find((s) => s.website_id === w.id) }))
      .filter((x) => !!x.sample)
  }, [primaryFrame, mySites])

  const aggMetrics = useMemo(() => {
    let cpu = 0
    let mem = 0
    let memLimit = 0
    let bw = 0
    let limitKnown = false
    for (const { sample } of siteSamples) {
      if (sample) {
        cpu += sample.cpu_percent
        mem += sample.memory_bytes
        if (sample.memory_limit_bytes > 0) {
          memLimit += sample.memory_limit_bytes
          limitKnown = true
        }
        bw += sample.bandwidth_bps
      }
    }
    return { cpu, mem, memLimit, limitKnown, bw }
  }, [siteSamples])

  /* History chart (historical store — separate from the live cards). */
  const chart = useMemo(() => {
    const pts = data?.history ?? []
    if (pts.length === 0) return null
    let filtered = pts
    if (range === '24h') {
      const cutoff = Date.now() - 24 * 3600 * 1000
      filtered = pts.filter((p) => new Date(p.collected_at).getTime() >= cutoff)
    }
    if (filtered.length === 0) filtered = pts.slice(-20)
    const step = Math.max(1, Math.ceil(filtered.length / 24))
    const sampled = filtered.filter((_, i) => i % step === 0 || i === filtered.length - 1)
    const label = (p: DashData['history'][number]) => {
      const d = new Date(p.collected_at)
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
    const memPct = (p: DashData['history'][number]) =>
      p.memory_total > 0 ? (p.memory_used / p.memory_total) * 100 : 0
    return {
      categories: sampled.map(label),
      series: [
        { name: 'CPU', data: sampled.map((p) => p.cpu_percent) },
        { name: 'Memory', data: sampled.map(memPct) },
      ],
    }
  }, [data?.history, range])

  const attention: { icon: React.ReactNode; title: string; sub: string; to?: string; tone: 'red' | 'amber' | 'blue' | 'green' }[] = [
    ...(data && data.alerts.filter((a) => !a.resolved_at).length > 0 ? [{
      icon: <ShieldAlert size={14} strokeWidth={1.8} />, title: `${data.alerts.filter((a) => !a.resolved_at).length} alert${data.alerts.filter((a) => !a.resolved_at).length > 1 ? 's' : ''} need attention`,
      sub: data.alerts[0]?.message ?? '', to: '/metrics', tone: 'red' as const,
    }] : []),
    ...(websites.some((w) => w.status === 'failed') ? [{
      icon: <Globe size={14} strokeWidth={1.8} />, title: 'A website failed to provision',
      sub: 'Open the website for details', to: '/websites', tone: 'red' as const,
    }] : []),
    {
      icon: <CircleCheck size={14} strokeWidth={1.8} />, title: 'Panel operational',
      sub: 'Web, databases and jobs healthy', tone: 'green' as const,
    },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Welcome back"
        subtitle="Your websites and hosting resources at a glance."
        actions={
          <>
            <Link to="/metrics" className="btn-ghost"><ActivityIcon size={14} /> Metrics</Link>
            <Link to="/websites" className="btn-primary"><Network size={14} /> Add website</Link>
          </>
        }
      />

      {/* Account strip */}
      <div className="card mb-4 flex flex-wrap items-center gap-3.5 p-4">
        <Initials text={org?.name ?? user?.name ?? 'U'} size={38} rounded="rounded-full" />
        <div className="min-w-0">
          <strong className="block truncate text-[12.5px] text-ink">{org?.name ?? user?.name}</strong>
          <span className="block truncate text-[10px] text-muted">
            {data?.pkg?.name ? `${data.pkg.name} · ` : ''}{user?.email}
          </span>
        </div>
        <div className="ml-auto text-right">
          <strong className="block text-[11.5px] text-ok">Hosting active</strong>
          <span className="block text-[10px] text-muted">
            {mySites.length > 0 ? `${mySites.filter((w) => w.status === 'ready').length} of ${mySites.length} sites live` : 'All services healthy'}
          </span>
        </div>
      </div>

      {/* Quick actions */}
      <div className="mb-4 grid grid-cols-2 gap-2.5 sm:grid-cols-3 xl:grid-cols-6">
        {QUICK_ACTIONS.map((a) => <QuickAction key={a.label} {...a} />)}
      </div>

      {/* Live resource cards — WS stream only, every value carries freshness */}
      <div className="mb-4 grid grid-cols-1 gap-3.5 sm:grid-cols-2 xl:grid-cols-4">
        <UsageCard
          icon={<Gauge size={14} strokeWidth={1.8} />} tone="blue" title="CPU" sub="Isolated site usage"
          value={primaryFrame ? `${Math.round(aggMetrics.cpu)}%` : '—'}
          pct={aggMetrics.cpu} color="#2563eb"
          note={primaryFrame ? 'Live CPU usage' : 'waiting for agent'}
          note2=""
        />
        <UsageCard
          icon={<ServerIcon size={14} strokeWidth={1.8} />} tone="purple" title="Memory" sub="Isolated site usage"
          value={primaryFrame ? fmtBytes(aggMetrics.mem) : '—'}
          pct={aggMetrics.limitKnown ? (aggMetrics.mem / aggMetrics.memLimit) * 100 : 0}
          color="#7c4dff"
          note={aggMetrics.limitKnown ? `${fmtBytes(aggMetrics.memLimit - aggMetrics.mem)} free` : (primaryFrame ? 'Live memory usage' : 'waiting for agent')}
          note2=""
        />
        <UsageCard
          icon={<HardDrive size={14} strokeWidth={1.8} />} tone="green" title="Disk" sub="Files + databases"
          value={storageTotal ? fmtBytes(storageUsed) : '—'}
          pct={storageTotal ? (storageUsed / storageTotal) * 100 : 0}
          color="#0f9d6e"
          note={storageTotal ? `${Math.round((storageUsed / storageTotal) * 100)}% used` : 'waiting for agent'}
          note2={storageTotal ? `${fmtBytes(storageTotal - storageUsed)} free` : ''}
        />
        <UsageCard
          icon={<Network size={14} strokeWidth={1.8} />} tone="amber" title="Bandwidth" sub="Live site traffic"
          value={primaryFrame ? fmtBytes(aggMetrics.bw) + '/s' : '—'}
          pct={Math.min(100, aggMetrics.bw / 125_000)}
          color="#d88b00"
          note={primaryFrame ? 'Live bandwidth usage' : 'waiting for agent'}
          note2=""
        />
      </div>

      {/* Traffic chart + websites */}
      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
        <Card>
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="Website traffic"
            subtitle="CPU and memory history across your hosting (historical store)"
            right={<ChartControls value={range} onChange={setRange} options={[{ label: '24h', value: '24h' }, { label: '7d', value: '7d' }]} />}
          />
          <div className="min-h-[240px] px-1 pb-2">
            {loading ? (
              <div className="skeleton h-[240px] w-full" />
            ) : chart ? (
              <AreaChart categories={chart.categories} series={chart.series} height={240} />
            ) : (
              <EmptyState icon={<ActivityIcon size={20} />} title="No metrics yet" subtitle="Waiting for the first samples." />
            )}
          </div>
        </Card>

        <Card>
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="Websites"
            subtitle="Current site status"
            right={<Link to="/websites" className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]">Manage</Link>}
          />
          {loading ? (
            <div className="p-5"><SkeletonRows rows={3} height="h-12" /></div>
          ) : mySites.length === 0 ? (
            <EmptyState icon={<Globe size={20} />} title="No websites yet" subtitle="Create your first website to get started." />
          ) : (
            <div className="space-y-2.5 p-4">
              {mySites.slice(0, 6).map((w) => (
                <Link key={w.id} to={`/websites/${w.id}`} className="flex items-center gap-3 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff] hover:shadow-card">
                  <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                    <Globe size={14} strokeWidth={1.8} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <strong className="block truncate text-[11px] text-ink">{w.primary_domain || w.name}</strong>
                    <span className="block truncate text-[9.5px] text-muted">
                      {w.runtime}{w.runtime_version ? ` ${w.runtime_version}` : ''}{siteSamples.find((s) => s.site.id === w.id) ? ` · ${Math.round(siteSamples.find((s) => s.site.id === w.id)!.sample!.cpu_percent)}% CPU` : ''}
                    </span>
                  </div>
                  <StatusBadge status={w.status} />
                </Link>
              ))}
            </div>
          )}
        </Card>
      </div>

      <div className="h-4" />

      {/* Activity + attention */}
      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
        <Card>
          <CardHeader className="mx-4 mt-4 !mb-0" title="Activity" subtitle="Recent actions on your account" />
          {loading ? (
            <div className="p-5"><SkeletonRows rows={3} height="h-10" /></div>
          ) : (data?.activity ?? []).length === 0 ? (
            <EmptyState icon={<ActivityIcon size={20} />} title="No activity yet" subtitle="Actions like publishing DNS or issuing SSL show up here." />
          ) : (
            <div className="space-y-2.5 p-4">
              {data!.activity.map((a) => (
                <MiniItem
                  key={a.id}
                  tone={a.result === 'ok' ? 'green' : 'red'}
                  icon={<ActivityIcon size={14} strokeWidth={1.8} />}
                  title={humanAction(a.action)}
                  sub={`${a.actor_email ?? 'system'} · ${timeAgo(a.created_at)}`}
                  right={<span className={`status-chip ${a.result === 'ok' ? 'status-live' : 'status-down'}`}>{a.result}</span>}
                />
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader className="mx-4 mt-4 !mb-0" title="Attention" subtitle="Items worth checking" />
          <div className="space-y-3 p-4">
            {attention.map((a, i) => (
              <MiniItem
                key={i}
                tone={a.tone}
                icon={a.icon}
                title={a.title}
                sub={a.sub}
                right={a.to ? <Link to={a.to} className="icon-btn" aria-label="Open"><ArrowRight size={13} /></Link> : <span className="status-chip status-live">OK</span>}
              />
            ))}
            <MiniItem
              tone="purple"
              icon={<Users size={14} strokeWidth={1.8} />}
              title="Need help?"
              sub="Your hosting provider can restore backups or adjust limits for you"
            />
          </div>
        </Card>
      </div>

      {/* All features */}
      <section className="mt-6">
        <h2 className="mb-2.5 text-[15px] font-bold tracking-[-.01em] text-ink">All features</h2>
        <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
          {FEATURES.map((f) => <FeatureTile key={f.label} {...f} />)}
        </div>
      </section>
    </div>
  )
}
