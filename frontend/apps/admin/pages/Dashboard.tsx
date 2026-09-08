import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Globe2, Database as DatabaseIcon, Users, Wallet, Activity, Server as ServerIcon,
  ListChecks, ShieldAlert, CircleCheck, HardDrive, ArrowRight,
} from 'lucide-react'
import { fmtBytes, useMetrics, useFreshness, formatAge } from '@epicpanel/core'
import type { SnapshotFrame } from '@epicpanel/core'
import { Card, CardHeader, StatCard, ProgressBar, EmptyState, SkeletonRows, FreshnessBadge } from '@epicpanel/ui'
import { adminview } from '../adminview'
import type { Overview } from '../adminview'

/** WHM overview: fleet health in seconds (master-doc principle, verbatim). */
export function DashboardPage() {
  const [data, setData] = useState<Overview | null>(null)
  const [err, setErr] = useState('')
  const { frames } = useMetrics()

  useEffect(() => {
    adminview
      .overview()
      .then(setData)
      .catch((ex) => setErr(ex.message ?? 'Failed to load overview'))
  }, [])

  const frameList = useMemo(() => Object.values(frames), [frames])
  const fleet = data?.fleet

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Server overview</h1>
          <p className="mt-[5px] text-[12px] text-muted">Everything important about your hosting platform in one place.</p>
        </div>
        {fleet && <FleetFreshness fleet={fleet} />}
      </div>

      {err && <Card className="mb-4"><EmptyState icon={<ShieldAlert size={20} />} title="Overview unavailable" subtitle={err} /></Card>}

      {/* Fleet consumption — live WS, LIVE/STALE/OFFLINE labeled */}
      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard
          icon={<Users size={15} strokeWidth={1.8} />}
          label="Hosting accounts"
          value={data ? data.accounts.total : ''}
          loading={!data}
          linkTo="/accounts" linkLabel="Manage accounts"
        />
        <StatCard
          icon={<Globe2 size={15} strokeWidth={1.8} />}
          label="Domains"
          value={data ? data.domains.total : ''}
          loading={!data}
          change={data && data.domains.ssl_expiring_30d > 0 ? `${data.domains.ssl_expiring_30d} SSL expiring 30d` : undefined}
          up={false}
          linkTo="/domains" linkLabel="View domains"
        />
        <StatCard
          icon={<ServerIcon size={15} strokeWidth={1.8} />}
          label="Nodes online"
          value={data ? `${data.nodes.online}/${data.nodes.total}` : ''}
          loading={!data}
          change={data && data.nodes.maintenance > 0 ? `${data.nodes.maintenance} in maintenance` : undefined}
          up={false}
          linkTo="/nodes" linkLabel="Node health"
        />
        <StatCard
          icon={<ListChecks size={15} strokeWidth={1.8} />}
          label="Failed jobs"
          value={data ? data.jobs.failed : ''}
          tone={data && data.jobs.failed > 0 ? 'red' : 'green'}
          loading={!data}
          linkTo="/jobs" linkLabel="Jobs console"
        />
      </div>

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
        {/* Fleet resource consumption (live) */}
        <Card>
          <CardHeader
            title="Fleet resource consumption"
            subtitle="Aggregated across live nodes — never from the history DB"
            right={fleet && <FleetFreshness fleet={fleet} compact />}
          />
          {!fleet ? (
            <SkeletonRows rows={2} height="h-10" />
          ) : fleet.sampled_nodes === 0 ? (
            <EmptyState
              icon={<Activity size={20} />}
              title="No live samples"
              subtitle="The agent stream has not reported yet — consumption appears the moment a node connects."
            />
          ) : (
            <div className="space-y-4">
              <Res label="CPU" value={`${Math.round(fleet.cpu_pct)}%`} pct={fleet.cpu_pct} color="#2563eb" />
              <Res
                label="Memory"
                value={fleet.mem_total_bytes ? `${fmtBytes(fleet.mem_used_bytes)} / ${fmtBytes(fleet.mem_total_bytes)}` : '—'}
                pct={fleet.mem_total_bytes ? (fleet.mem_used_bytes / fleet.mem_total_bytes) * 100 : 0}
                color="#0f9d6e"
              />
              <Res
                label="Storage"
                value={fleet.disk_total_bytes ? `${fmtBytes(fleet.disk_used_bytes)} / ${fmtBytes(fleet.disk_total_bytes)}` : '—'}
                pct={fleet.disk_total_bytes ? (fleet.disk_used_bytes / fleet.disk_total_bytes) * 100 : 0}
                color="#7c4dff"
              />
              <div className="flex flex-wrap gap-4 pt-1 text-[10px] text-muted">
                <span>Network in <b className="text-ink">{fmtBytes(fleet.rx_bps)}/s</b></span>
                <span>Network out <b className="text-ink">{fmtBytes(fleet.tx_bps)}/s</b></span>
                <span>Live nodes <b className="text-ink">{fleet.sampled_nodes}</b></span>
              </div>
            </div>
          )}
        </Card>

        {/* Node health grid (live WS frames) */}
        <Card>
          <CardHeader
            title="Node health"
            subtitle="Connection state per node — live stream"
            right={<Link to="/nodes" className="btn-ghost !min-h-[28px] !px-2 !text-[10px]">All nodes</Link>}
          />
          {frameList.length === 0 ? (
            <EmptyState
              icon={<ServerIcon size={20} />}
              title="No nodes reporting yet"
              subtitle="Enroll a server to see live health. State is LIVE within 15s of the last frame."
            />
          ) : (
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              {frameList.map((f) => <NodeHealthCard key={f.server_id} frame={f} />)}
            </div>
          )}
        </Card>
      </div>

      <div className="mt-3.5 grid grid-cols-1 gap-3.5 xl:grid-cols-3">
        {/* Account distribution */}
        <Card>
          <CardHeader title="Account distribution" subtitle="Where accounts live: plan, status, node" />
          {!data ? (
            <SkeletonRows rows={3} height="h-9" />
          ) : (
            <div className="space-y-4">
              {data.distribution.by_plan.map((d) => (
                <Res key={d.key} label={d.key} value={`${d.count}`} pct={pct(d.count, data.accounts.total)} color="#2563eb" />
              ))}
              <div className="border-t border-line pt-3">
                {data.distribution.by_status.map((d) => (
                  <div key={d.key} className="flex items-center justify-between py-0.5 text-[10.5px]">
                    <span className="capitalize text-sub">{d.key}</span>
                    <span className="font-bold text-ink">{d.count}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </Card>

        {/* Active services */}
        <Card>
          <CardHeader title="Active services" subtitle="Reported states across live nodes" />
          {!data || data.services.length === 0 ? (
            <EmptyState
              icon={<CircleCheck size={20} />}
              title="No service samples"
              subtitle="Service health (nginx, php-fpm, MariaDB, Docker) arrives with the live stream."
            />
          ) : (
            <div className="divide-y divide-line">
              {data.services.map((s) => (
                <div key={s.name} className="flex items-center gap-3 py-2.5">
                  <span className={`h-1.5 w-1.5 rounded-full ${s.down > 0 ? 'bg-warn' : 'bg-ok'}`} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-mono text-[11px] font-bold text-ink">{s.name}</div>
                    <div className="text-[9px] text-muted">{s.ok} ok{s.down > 0 ? ` · ${s.down} down` : ''}</div>
                  </div>
                  <span className={`status-chip ${s.down > 0 ? 'status-warning' : 'status-live'}`}>{s.down > 0 ? 'Watch' : 'OK'}</span>
                </div>
              ))}
            </div>
          )}
        </Card>

        {/* Failed jobs + alerts */}
        <Card>
          <CardHeader title="Needs attention" subtitle="Terminal jobs and open alerts" />
          {!data ? (
            <SkeletonRows rows={3} height="h-9" />
          ) : (
            <div className="space-y-2.5">
              {data.jobs.failed > 0 && (
                <Link to="/jobs?status=failed" className="flex items-center gap-2.5 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff]">
                  <span className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-danger-soft text-danger"><ListChecks size={14} /></span>
                  <span className="min-w-0 flex-1">
                    <strong className="block text-[11px] text-ink">{data.jobs.failed} failed job{data.jobs.failed > 1 ? 's' : ''}</strong>
                    <span className="block text-[9px] text-muted">Retry or inspect in the jobs console</span>
                  </span>
                  <ArrowRight size={13} className="text-muted" />
                </Link>
              )}
              {data.alerts.unresolved > 0 && (
                <Link to="/monitoring" className="flex items-center gap-2.5 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff]">
                  <span className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-warn-soft text-warn"><ShieldAlert size={14} /></span>
                  <span className="min-w-0 flex-1">
                    <strong className="block text-[11px] text-ink">{data.alerts.unresolved} open alert{data.alerts.unresolved > 1 ? 's' : ''}</strong>
                    <span className="block text-[9px] text-muted">Across customer organizations</span>
                  </span>
                  <ArrowRight size={13} className="text-muted" />
                </Link>
              )}
              {data.backups.failed_7d > 0 && (
                <Link to="/backups" className="flex items-center gap-2.5 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff]">
                  <span className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-purple-soft text-purple"><HardDrive size={14} /></span>
                  <span className="min-w-0 flex-1">
                    <strong className="block text-[11px] text-ink">{data.backups.failed_7d} backup failure{data.backups.failed_7d > 1 ? 's' : ''} (7d)</strong>
                    <span className="block text-[9px] text-muted">Review restore points</span>
                  </span>
                  <ArrowRight size={13} className="text-muted" />
                </Link>
              )}
              {data.jobs.failed === 0 && data.alerts.unresolved === 0 && data.backups.failed_7d === 0 && (
                <div className="flex items-center gap-2.5 rounded-[10px] border border-line px-3 py-2.5">
                  <span className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-ok-soft text-ok"><CircleCheck size={14} /></span>
                  <span className="min-w-0 flex-1">
                    <strong className="block text-[11px] text-ink">All clear</strong>
                    <span className="block text-[9px] text-muted">No failed jobs, open alerts or backup failures</span>
                  </span>
                </div>
              )}
              <div className="grid grid-cols-3 gap-2 border-t border-line pt-3 text-center">
                <Mini label="Customers" value={data.customers.orgs} icon={<Users size={12} />} />
                <Mini label="Databases" value={data.databases} icon={<DatabaseIcon size={12} />} />
                <Mini label="Backups 7d" value={data.backups.last_7d} icon={<Wallet size={12} />} />
              </div>
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}

function FleetFreshness({ fleet, compact }: { fleet: Overview['fleet']; compact?: boolean }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => tick((x) => x + 1), 1000)
    return () => clearInterval(t)
  }, [])
  const state = fleet.freshness?.state ?? 'OFFLINE'
  const ageMs = fleet.freshness?.age_ms ?? 0
  if (compact) return <FreshnessBadge state={state} ageMs={ageMs} />
  return (
    <div className="flex items-center gap-2">
      <FreshnessBadge state={state} ageMs={ageMs} label="Fleet" />
      {!compact && <span className="text-[10px] text-muted">{fleet.sampled_nodes} live nodes · updated {formatAge(ageMs)} ago</span>}
    </div>
  )
}

/** One node card in the health grid — pure live WS data. */
function NodeHealthCard({ frame }: { frame: SnapshotFrame }) {
  const fresh = useFreshness(frame)
  const node = frame.sample?.node
  const state = fresh.state
  return (
    <div className={`rounded-[11px] border border-line p-3 ${state === 'OFFLINE' ? 'opacity-80' : ''}`}>
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="truncate text-[11px] font-bold text-ink">{frame.server_id.slice(0, 8)}</span>
        <FreshnessBadge state={state} ageMs={fresh.ageMs} />
      </div>
      {state === 'OFFLINE' || !node ? (
        <div className="text-[9.5px] text-muted">
          {frame.connected === false ? 'Agent disconnected' : 'Waiting for the first sample'}
        </div>
      ) : (
        <div className="space-y-2">
          <Res small label="CPU" value={`${Math.round(node.cpu_percent)}%`} pct={node.cpu_percent} color="#2563eb" />
          <Res
            small
            label="MEM"
            value={node.memory_total_bytes ? `${fmtBytes(node.memory_used_bytes)}` : '—'}
            pct={node.memory_total_bytes ? (node.memory_used_bytes / node.memory_total_bytes) * 100 : 0}
            color="#0f9d6e"
          />
        </div>
      )}
      {frame.degraded && <div className="mt-2 text-[9px] font-bold text-warn">Degraded collection</div>}
    </div>
  )
}

function Res({ label, value, pct, color, small }: { label: string; value: string; pct: number; color: string; small?: boolean }) {
  return (
    <div>
      <div className={`mb-1.5 flex items-baseline justify-between gap-3 ${small ? '' : ''}`}>
        <strong className={small ? 'text-[10px] text-ink' : 'text-[11px] text-ink'}>{label}</strong>
        <span className="text-[10px] font-bold text-muted">{value}</span>
      </div>
      <ProgressBar pct={pct} color={color} />
    </div>
  )
}

function Mini({ label, value, icon }: { label: string; value: number; icon: React.ReactNode }) {
  return (
    <div className="rounded-[10px] bg-surface-2 px-2 py-2">
      <div className="flex items-center justify-center gap-1 text-[9px] font-bold uppercase tracking-[.05em] text-muted">{icon} {label}</div>
      <div className="mt-0.5 text-[16px] font-extrabold text-ink">{value}</div>
    </div>
  )
}

function pct(part: number, total: number): number {
  return total > 0 ? (part / total) * 100 : 0
}
