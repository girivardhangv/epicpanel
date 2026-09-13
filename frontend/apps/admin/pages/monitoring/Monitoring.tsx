// Monitoring hub (route /monitoring): the verbatim overview tree, alert
// stat summary and the alert feed with lifecycle actions. Drill-down
// sections live at /monitoring/:section (see MonitoringOutlet).
import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { Bell, BellOff, Server as ServerIcon, Users } from 'lucide-react'
import { useMetrics, seedFrames, normalizeBatch } from '@epicpanel/core'
import { PageTitle, StatCard } from '@epicpanel/ui'
import { adminview } from '../../adminview'
import type { Overview } from '../../adminview'
import { monitoring } from './client'
import { AlertFeed } from './Alerts'
import { OverviewTree } from './tree'
import { NodesSection } from './Nodes'
import { ServicesSection } from './Services'
import { CustomersSection } from './Customers'
import { RulesSection } from './Rules'
import { AlertsSection } from './Alerts'
import { StateChip } from './bits'

/** Route table for /monitoring/:section (rendered inside the app shell). */
export function MonitoringOutlet() {
  const { section } = useParams()
  switch (section) {
    case 'alerts':
      return <AlertsSection />
    case 'rules':
      return <RulesSection />
    case 'nodes':
      return <NodesSection />
    case 'services':
      return <ServicesSection />
    case 'customers':
      return <CustomersSection />
    default:
      return <MonitoringHub />
  }
}

/**
 * Hub: verbatim overview tree + alert feed with stat summary. Stat cards use
 * the P6 adminview overview rollup; the one-shot live seed keeps fleet cards
 * painting before the first WS frame (P6 pattern) — afterwards the stream is
 * the only source.
 */
function MonitoringHub() {
  const [ov, setOv] = useState<Overview | null>(null)
  const [counts, setCounts] = useState<{ active: number; ack: number } | null>(null)
  const { connected } = useMetrics()

  useEffect(() => {
    adminview.overview().then(setOv).catch(() => setOv(null))
    adminview
      .live()
      .then((r) => seedFrames(normalizeBatch({ metrics: r.frames })))
      .catch(() => undefined)
  }, [])

  // Tab counts for the stat cards; refreshed on a slow interval (counts are
  // server-side rollups, not per-frame values).
  useEffect(() => {
    let alive = true
    const load = () => {
      Promise.all([monitoring.alerts('active', 1), monitoring.alerts('ack', 1)])
        .then(([a, k]) => {
          if (!alive) return
          setCounts({ active: (a.alerts ?? []).length, ack: (k.alerts ?? []).length })
        })
        .catch(() => undefined)
    }
    load()
    const t = window.setInterval(load, 30000)
    return () => {
      alive = false
      window.clearInterval(t)
    }
  }, [])

  return (
    <div className="fade-up">
      <PageTitle
        title="Monitoring"
        subtitle="Platform observability: the overview tree, the alert lifecycle and rule configuration."
        actions={<StateChip state={connected ? 'LIVE' : 'OFFLINE'} label="Stream" ageMs={connected ? 0 : undefined} />}
      />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard
          label="Active alerts"
          value={counts ? counts.active : ''}
          tone={counts && counts.active > 0 ? 'red' : 'green'}
          loading={!counts}
          icon={<Bell size={15} strokeWidth={1.8} />}
        />
        <StatCard
          label="Acknowledged"
          value={counts ? counts.ack : ''}
          tone={counts && counts.ack > 0 ? 'amber' : 'blue'}
          loading={!counts}
          icon={<BellOff size={15} strokeWidth={1.8} />}
        />
        <StatCard
          label="Nodes online"
          value={ov ? `${ov.nodes.online}/${ov.nodes.total}` : ''}
          tone={ov && ov.nodes.offline > 0 ? 'red' : 'green'}
          loading={!ov}
          icon={<ServerIcon size={15} strokeWidth={1.8} />}
        />
        <StatCard
          label="Customers"
          value={ov ? ov.customers.orgs : ''}
          tone="blue"
          loading={!ov}
          icon={<Users size={15} strokeWidth={1.8} />}
        />
      </div>

      <div className="mb-2 flex items-center gap-2">
        <h2 className="text-[13px] font-bold tracking-[-.01em] text-ink">Observability tree</h2>
        <span className="badge-neutral">verbatim layout</span>
      </div>
      <div className="mb-5">
        <OverviewTree />
      </div>

      <div className="mb-2 flex items-center gap-2">
        <h2 className="text-[13px] font-bold tracking-[-.01em] text-ink">Alerts</h2>
        <span className="badge-neutral">active | ack | resolved</span>
      </div>
      <AlertFeed />
    </div>
  )
}
