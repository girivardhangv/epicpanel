// Nodes drill-down — verbatim tree branch: CPU / RAM / Disk / Network / Health.
// Rows come from GET /v1/admin/observability/nodes (LiveStore snapshot);
// per-value freshness comes from the WS frames joined by server_id (P6 fleet
// pattern: REST seed + live stream). The backend nodes projection leaves
// disk/network at zero for now — those columns fall back to the WS sample and
// render an honest "—" when no source has a value.
import { Server as ServerIcon, HardDrive, Wifi, HeartPulse } from 'lucide-react'
import { fmtBytes, primaryDisk, useFreshness, useMetrics } from '@epicpanel/core'
import type { SnapshotFrame } from '@epicpanel/core'
import { Card, EmptyState, SkeletonRows } from '@epicpanel/ui'
import { monitoring } from './client'
import type { ObsNode } from './data'
import { liveState } from './data'
import { FailedNote, SectionShell, StateChip, usePolled } from './bits'
import type { UIState } from './bits'

export function NodesSection() {
  const { data, failed, reload } = usePolled(() => monitoring.obsNodes(), [])
  const rows = data?.nodes ?? null

  return (
    <SectionShell
      crumb="Nodes"
      title="Nodes"
      subtitle="Fleet hardware health — CPU, RAM, Disk, Network and stream freshness per node."
      actions={rows !== null && rows.length > 0 ? <StateChip state={overallState(rows)} label="Fleet" /> : undefined}
    >
      {failed && <FailedNote what="Node observability" onRetry={reload} />}
      {rows === null ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : rows.length === 0 ? (
        <Card>
          <EmptyState icon={<ServerIcon size={20} />} title="No nodes enrolled" subtitle="Enroll a server and the observability tree fills in the moment its agent connects." />
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
          {rows.map((n) => (
            <NodeObsCard key={n.server_id} node={n} />
          ))}
        </div>
      )}
    </SectionShell>
  )
}

function overallState(rows: ObsNode[] | null): UIState {
  if (!rows || rows.length === 0) return 'OFFLINE'
  if (rows.some((r) => liveState(r.state) === 'LIVE')) return 'LIVE'
  if (rows.some((r) => liveState(r.state) === 'STALE')) return 'STALE'
  return 'OFFLINE'
}

function NodeObsCard({ node }: { node: ObsNode }) {
  const { frames } = useMetrics()
  const frame: SnapshotFrame | null = frames[node.server_id] ?? null
  const fresh = useFreshness(frame)
  const state: UIState = frame ? fresh.state : liveState(node.state)
  const ageMs = frame ? fresh.ageMs : node.age_seconds > 0 ? node.age_seconds * 1000 : undefined
  const sample = frame?.sample?.node ?? null
  const disk = primaryDisk(sample)
  const diskPct = disk && disk.total_bytes > 0 ? (disk.used_bytes / disk.total_bytes) * 100 : null
  const cpu = sample ? sample.cpu_percent : state !== 'OFFLINE' ? node.cpu_percent : null
  const ram = sample && sample.memory_total_bytes > 0
    ? (sample.memory_used_bytes / sample.memory_total_bytes) * 100
    : state !== 'OFFLINE' && node.ram_percent > 0
      ? node.ram_percent
      : null

  return (
    <Card className="!p-0">
      <div className="flex items-center gap-2.5 border-b border-line px-4 py-3">
        <span className="grid h-[32px] w-[32px] place-items-center rounded-[9px] bg-brand-soft text-brand">
          <ServerIcon size={15} strokeWidth={1.8} />
        </span>
        <div className="min-w-0 flex-1">
          <strong className="block truncate text-[12px] text-ink">{node.name}</strong>
          <span className="block truncate text-[9px] text-muted font-mono">{node.server_id.slice(0, 8)}</span>
        </div>
        <StateChip state={state} ageMs={ageMs} />
      </div>
      <div className="space-y-2.5 px-4 py-3">
        {state === 'OFFLINE' && !sample ? (
          <div className="flex items-center gap-2.5 rounded-[9px] bg-danger-soft px-3 py-2.5 text-[10.5px] font-semibold text-danger">
            <HeartPulse size={14} /> Offline — no live sample from this node.
          </div>
        ) : (
          <>
            <Metric icon={<ServerIcon size={12} />} label="CPU" value={cpu !== null ? `${Math.round(cpu)}%` : '—'} pct={cpu ?? 0} color="#2563eb" />
            <Metric icon={<ServerIcon size={12} />} label="RAM" value={ram !== null ? `${Math.round(ram)}%` : '—'} pct={ram ?? 0} color="#0f9d6e" />
            <Metric
              icon={<HardDrive size={12} />}
              label="Disk"
              value={diskPct !== null ? `${Math.round(diskPct)}%` : '—'}
              pct={diskPct ?? 0}
              color="#7c4dff"
              note={diskPct === null ? 'not reported by the stream' : undefined}
            />
            <div className="flex items-center gap-2 pt-0.5 text-[10px] text-muted">
              <Wifi size={12} className="flex-none text-[#98a2b3]" />
              {sample ? (
                <span>
                  in <b className="text-ink">{fmtBytes(sample.net.rx_bps)}/s</b> · out{' '}
                  <b className="text-ink">{fmtBytes(sample.net.tx_bps)}/s</b>
                </span>
              ) : (
                <span title="network counters not in the observability projection">network — no sample</span>
              )}
            </div>
          </>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-line px-4 py-2.5 text-[9.5px] text-muted">
        <span>Health</span>
        <span>
          {state === 'OFFLINE'
            ? 'no recent frames'
            : ageMs !== undefined
              ? `stream age ${Math.round(ageMs / 1000)}s`
              : 'waiting for frames'}
        </span>
      </div>
    </Card>
  )
}

function Metric({ icon, label, value, pct, color, note }: {
  icon: React.ReactNode
  label: string
  value: string
  pct: number
  color: string
  note?: string
}) {
  return (
    <div>
      <div className="mb-[5px] flex items-baseline justify-between gap-3">
        <strong className="flex items-center gap-1.5 text-[10px] text-ink">{icon}{label}</strong>
        <span className="text-[9.5px] font-bold text-muted" title={note}>{value}</span>
      </div>
      <div className="h-[6px] w-full overflow-hidden rounded-full bg-line-soft">
        <div className="h-full rounded-full" style={{ width: `${Math.max(note ? 0 : 2, Math.min(100, pct))}%`, background: color }} />
      </div>
    </div>
  )
}
