// The verbatim overview tree from the master doc, rendered as drill-down
// navigation. Groups link to their observability sections; each leaf mirrors
// the documented branch exactly:
//   Nodes     +-- CPU / RAM / Disk / Network / Health
//   Services  +-- Nginx / Apache / OLS / PHP-FPM / MariaDB / Docker
//   Customers +-- CPU / RAM / Disk / Bandwidth
//   Minecraft +-- TPS / MSPT / Players
//   Discord   +-- CPU / RAM / Uptime
import { Link } from 'react-router-dom'
import { ChevronRight, Server, ServerCog, Users, Gamepad2, Bot } from 'lucide-react'
import { Card } from '@epicpanel/ui'

export interface TreeGroup {
  key: string
  label: string
  to: string
  icon: React.ReactNode
  leaves: string[]
}

export const TREE: TreeGroup[] = [
  { key: 'nodes', label: 'Nodes', to: '/monitoring/nodes', icon: <Server size={15} strokeWidth={1.8} />, leaves: ['CPU', 'RAM', 'Disk', 'Network', 'Health'] },
  { key: 'services', label: 'Services', to: '/monitoring/services', icon: <ServerCog size={15} strokeWidth={1.8} />, leaves: ['Nginx', 'Apache', 'OLS', 'PHP-FPM', 'MariaDB', 'Docker'] },
  { key: 'customers', label: 'Customers', to: '/monitoring/customers', icon: <Users size={15} strokeWidth={1.8} />, leaves: ['CPU', 'RAM', 'Disk', 'Bandwidth'] },
  { key: 'minecraft', label: 'Minecraft', to: '/monitoring/minecraft', icon: <Gamepad2 size={15} strokeWidth={1.8} />, leaves: ['TPS', 'MSPT', 'Players'] },
  { key: 'discord', label: 'Discord', to: '/monitoring/discord', icon: <Bot size={15} strokeWidth={1.8} />, leaves: ['CPU', 'RAM', 'Uptime'] },
]

export function OverviewTree() {
  return (
    <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
      {TREE.map((g) => (
        <Link key={g.key} to={g.to} className="card !p-0 text-left transition hover:border-[#cddaff] hover:shadow-pop">
          <div className="flex items-center gap-2.5 border-b border-line px-4 py-3">
            <span className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-brand-soft text-brand">{g.icon}</span>
            <strong className="flex-1 text-[12px] text-ink">{g.label}</strong>
            <ChevronRight size={14} className="text-muted" />
          </div>
          <div className="flex flex-wrap gap-1.5 px-4 py-3">
            {g.leaves.map((leaf, i) => (
              <span key={leaf} className="flex items-center gap-1.5">
                {i > 0 && <span className="text-[9px] text-[#c3cbd8]">·</span>}
                <span className="rounded-full border border-line bg-surface-2 px-2 py-[2px] text-[9.5px] font-bold text-sub">{leaf}</span>
              </span>
            ))}
          </div>
        </Link>
      ))}
      <Card className="flex flex-col justify-center">
        <div className="text-[10px] font-bold uppercase tracking-[.06em] text-muted">Alert engine</div>
        <p className="mt-1.5 text-[11px] leading-[1.5] text-sub">
          Rules evaluate asynchronously on stream events and sweeps: threshold (hysteresis + dedup), state (offline, down,
          crashed, failed) and time (SSL windows). Manage them under{' '}
          <Link to="/monitoring/rules" className="font-bold text-brand hover:underline">Alert rules</Link>.
        </p>
      </Card>
    </div>
  )
}
