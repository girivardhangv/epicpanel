// Services drill-down — verbatim tree branch: Nginx / Apache / OLS / PHP-FPM
// / MariaDB / Docker. One matrix: nodes as rows, the six verbatim services as
// columns. Service truth comes from GET /v1/admin/observability/services;
// when a node's WS frame is live, the frame's own service list wins (fresher).
// OFFLINE nodes show "no data" per service — down is only claimed when the
// stream actually reported the unit inactive.
import { ServerCog } from 'lucide-react'
import { useMetrics } from '@epicpanel/core'
import { Card, EmptyState, SkeletonRows } from '@epicpanel/ui'
import { monitoring } from './client'
import type { ObsNode, ObsService } from './data'
import { liveState } from './data'
import { FailedNote, SectionShell, StateChip, usePolled } from './bits'
import type { UIState } from './bits'

const SERVICES = [
  { key: 'nginx', label: 'Nginx' },
  { key: 'apache', label: 'Apache' },
  { key: 'ols', label: 'OLS' },
  { key: 'php-fpm', label: 'PHP-FPM' },
  { key: 'mariadb', label: 'MariaDB' },
  { key: 'docker', label: 'Docker' },
]

export function ServicesSection() {
  const { data, failed, reload } = usePolled(async () => {
    const [svc, nodes] = await Promise.all([monitoring.obsServices(), monitoring.obsNodes()])
    return { services: svc.services ?? [], nodes: nodes.nodes ?? [] }
  }, [])
  const rows = data?.services ?? null
  const names = new Map((data?.nodes ?? []).map((n) => [n.server_id, n.name] as const))

  // matrix: server_id -> service -> up (obs) ; frame overlay applied per cell
  const byServer = new Map<string, Map<string, boolean>>()
  for (const r of rows ?? []) {
    if (!byServer.has(r.server_id)) byServer.set(r.server_id, new Map())
    byServer.get(r.server_id)!.set(r.service, r.up)
  }
  const nodeStates = new Map<string, UIState>()
  for (const r of rows ?? []) nodeStates.set(r.server_id, liveState(r.node_state))

  const order = [...byServer.keys()]

  return (
    <SectionShell
      crumb="Services"
      title="Services"
      subtitle="Web, runtime, database and container service units across the fleet."
    >
      {failed && <FailedNote what="Service observability" onRetry={reload} />}
      {rows === null ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : order.length === 0 ? (
        <Card>
          <EmptyState icon={<ServerCog size={20} />} title="No service data" subtitle="Service states appear once an enrolled node streams its first sample." />
        </Card>
      ) : (
        <Card className="!p-0">
          <div className="overflow-x-auto">
            <table className="w-full border-collapse" style={{ minWidth: 820 }}>
              <thead>
                <tr>
                  <th className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">Node</th>
                  <th className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">Health</th>
                  {SERVICES.map((s) => (
                    <th key={s.key} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">
                      {s.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {order.map((serverId) => {
                  const state = nodeStates.get(serverId) ?? 'OFFLINE'
                  return (
                    <tr key={serverId} className="transition hover:bg-[#fbfcff]">
                      <td className="border-b border-line px-4 py-[11px] text-[10.5px] font-bold text-ink">
                        {names.get(serverId) ?? <span className="font-mono">{serverId.slice(0, 8)}</span>}
                      </td>
                      <td className="border-b border-line px-4 py-[11px]"><StateChip state={state} /></td>
                      {SERVICES.map((s) => (
                        <ServiceCell key={s.key} serverId={serverId} service={s.key} state={state} reported={byServer.get(serverId)?.get(s.key)} />
                      ))}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </SectionShell>
  )
}

function ServiceCell({ serverId, service, state, reported }: {
  serverId: string
  service: string
  state: UIState
  reported: boolean | undefined
}) {
  const { frames } = useMetrics()
  const frame = frames[serverId] ?? null
  let up: boolean | null = reported ?? null
  if (frame && state === 'LIVE' && frame.sample?.node?.services) {
    const svc = frame.sample.node.services.find((s) => s.name === service)
    if (svc) up = svc.state === 'active'
  }
  if (state === 'OFFLINE' && !frame) {
    return <span className="badge-neutral" title="node offline — service state unknown">no data</span>
  }
  if (up === null || up === undefined) {
    return <span className="badge-neutral" title="not present in the node sample">n/a</span>
  }
  return up ? (
    <span className="badge-ok" title={`${service} active`}><span className="h-1.5 w-1.5 rounded-full bg-current" />Active</span>
  ) : (
    <span className="badge-off" title={`${service} not active`}><span className="h-1.5 w-1.5 rounded-full bg-current" />Down</span>
  )
}
