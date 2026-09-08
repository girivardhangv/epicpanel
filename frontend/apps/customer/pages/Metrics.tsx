import { useEffect, useMemo, useState } from 'react'
import { Activity as ActivityIcon } from 'lucide-react'
import { api, useAuth, useMetrics, useFreshness, seedFrames, normalizeBatch, primaryDisk, fmtBytes } from '@epicpanel/core'
import type { Server, SnapshotFrame } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, MiniItem } from '@epicpanel/ui'
import { FreshnessBadge } from '@epicpanel/ui'
import { AreaChart, ChartControls } from '@epicpanel/charts'

interface HistPoint {
  collected_at: string
  cpu_percent: number
  memory_used: number
  memory_total: number
  disk_used?: number
  disk_total?: number
  load1?: number
  rx_bps?: number
  tx_bps?: number
}

/**
 * Metrics history — the historical store endpoints (24h raw / 7d+30d rollup).
 * The LIVE cards never read this; they come from the WebSocket only (Phase 3
 * contract #4). This page is the opposite: pure history.
 */
export function MetricsPage() {
  const { org } = useAuth()
  const [servers, setServers] = useState<Server[]>([])
  const [serverId, setServerId] = useState('')
  const [range, setRange] = useState('24h')
  const [history, setHistory] = useState<HistPoint[] | null>(null)
  const { frames } = useMetrics()

  useEffect(() => {
    if (!org) return
    api
      .get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`)
      .then(async (r) => {
        const sv = r.servers ?? []
        setServers(sv)
        setServerId((prev) => prev || sv[0]?.id || '')
        // Seed the live layer once so the header badge has something to show.
        return api
          .get<{ metrics: unknown }>(`/v1/organizations/${org.id}/servers/metrics`)
          .then((res) => seedFrames(normalizeBatch(res)))
          .catch(() => undefined)
      })
      .catch(() => setServers([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!org || !serverId) return
    setHistory(null)
    api
      .get<{ metrics: HistPoint[] }>(`/v1/organizations/${org.id}/servers/${serverId}/metrics/history?range=${range}`)
      .then((r) => setHistory((r.metrics ?? []).slice().reverse()))
      .catch(() => setHistory([]))
  }, [org?.id, serverId, range]) // eslint-disable-line react-hooks/exhaustive-deps

  const frame: SnapshotFrame | null = frames[serverId] ?? null
  const fresh = useFreshness(frame)
  const node = frame?.sample?.node
  const disk = primaryDisk(node)

  const chart = useMemo(() => {
    const pts = history ?? []
    if (pts.length === 0) return null
    const step = Math.max(1, Math.ceil(pts.length / 40))
    const sampled = pts.filter((_, i) => i % step === 0 || i === pts.length - 1)
    const label = (p: HistPoint) => {
      const d = new Date(p.collected_at)
      return range === '30d'
        ? `${d.getDate()}/${d.getMonth() + 1}`
        : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
    const memPct = (p: HistPoint) => (p.memory_total > 0 ? (p.memory_used / p.memory_total) * 100 : 0)
    const netKbps = (p: HistPoint) => ((p.rx_bps ?? 0) + (p.tx_bps ?? 0)) / 1000
    return {
      traffic: {
        categories: sampled.map(label),
        series: [
          { name: 'CPU %', data: sampled.map((p) => p.cpu_percent) },
          { name: 'Memory %', data: sampled.map(memPct) },
        ],
      },
      net: {
        categories: sampled.map(label),
        series: [{ name: 'Network KB/s', data: sampled.map(netKbps) }],
      },
    }
  }, [history, range])

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Metrics"
        subtitle="Resource usage over time from the monitoring history."
        actions={
          servers.length > 1 ? (
            <select className="input w-[220px] cursor-pointer" value={serverId} onChange={(e) => setServerId(e.target.value)}>
              {servers.map((s) => (
                <option key={s.id} value={s.id}>{s.name}</option>
              ))}
            </select>
          ) : undefined
        }
      />

      {/* Live snapshot strip (WS only — this is the ONLY place live + history meet, side by side but never mixed) */}
      <Card className="mb-4">
        <CardHeader
          className="mx-4 mt-4 !mb-0"
          title="Live now"
          subtitle="WebSocket stream — this is not history"
          right={frame ? <FreshnessBadge state={fresh.state} ageMs={fresh.ageMs} label="Live" /> : undefined}
        />
        {frame && node ? (
          <div className="grid grid-cols-2 gap-3.5 p-4 pt-1 sm:grid-cols-4">
            <MiniItem tone="blue" icon={<ActivityIcon size={14} strokeWidth={1.8} />} title={`${Math.round(node.cpu_percent)}% CPU`} sub={`load ${node.load1?.toFixed(2) ?? '—'}`} />
            <MiniItem tone="purple" icon={<ActivityIcon size={14} strokeWidth={1.8} />} title={node.memory_total_bytes ? fmtBytes(node.memory_used_bytes) : '—'} sub={node.memory_total_bytes ? `of ${fmtBytes(node.memory_total_bytes)}` : ''} />
            <MiniItem tone="green" icon={<ActivityIcon size={14} strokeWidth={1.8} />} title={disk && disk.total_bytes ? fmtBytes(disk.used_bytes) : '—'} sub={disk && disk.total_bytes ? `of ${fmtBytes(disk.total_bytes)}` : ''} />
            <MiniItem tone="amber" icon={<ActivityIcon size={14} strokeWidth={1.8} />} title={`${fmtBytes((node.net?.rx_bps ?? 0) + (node.net?.tx_bps ?? 0))}/s`} sub="network in+out" />
          </div>
        ) : (
          <div className="p-4 pt-1 text-[11.5px] text-muted">Waiting for live frames from the server agent…</div>
        )}
      </Card>

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
        <Card>
          <CardHeader
            className="mx-4 mt-4 !mb-0"
            title="CPU & memory history"
            subtitle={range === '24h' ? 'Raw samples (10s resolution, last 24h)' : '5-minute rollups'}
            right={<ChartControls value={range} onChange={setRange} options={[{ label: '24h', value: '24h' }, { label: '7d', value: '7d' }, { label: '30d', value: '30d' }]} />}
          />
          <div className="min-h-[260px] px-1 pb-2">
            {history === null ? (
              <div className="p-5"><SkeletonRows rows={3} height="h-14" /></div>
            ) : chart ? (
              <AreaChart categories={chart.traffic.categories} series={chart.traffic.series} />
            ) : (
              <EmptyState icon={<ActivityIcon size={20} />} title="No history yet" subtitle="Samples appear after the agent runs for a while." />
            )}
          </div>
        </Card>
        <Card>
          <CardHeader className="mx-4 mt-4 !mb-0" title="Network history" subtitle="Throughput in KB/s" />
          <div className="min-h-[260px] px-1 pb-2">
            {history === null ? (
              <div className="p-5"><SkeletonRows rows={3} height="h-14" /></div>
            ) : chart ? (
              <AreaChart categories={chart.net.categories} series={chart.net.series} height={260} format={(v) => `${Math.round(v)} KB/s`} />
            ) : (
              <EmptyState icon={<ActivityIcon size={20} />} title="No history yet" subtitle="Network samples appear after the agent runs for a while." />
            )}
          </div>
        </Card>
      </div>
    </div>
  )
}
