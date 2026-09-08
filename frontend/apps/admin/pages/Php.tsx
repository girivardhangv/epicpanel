import { useEffect, useMemo, useState } from 'react'
import { Layers, Activity } from 'lucide-react'
import { Card, CardHeader, EmptyState, PageTitle, SkeletonRows } from '@epicpanel/ui'
import { useMetrics } from '@epicpanel/core'
import type { ServiceSample } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminServer } from '../adminview'

/** PHP: PHP runtimes per node + live php-fpm states. */
export function PhpPage() {
  const [rows, setRows] = useState<AdminServer[] | null>(null)
  const { frames } = useMetrics()

  useEffect(() => {
    adminview.servers().then((r) => setRows(r.servers ?? [])).catch(() => setRows([]))
  }, [])

  const fpmStates = useMemo(() => {
    const map = new Map<string, string>()
    for (const f of Object.values(frames)) {
      if (f.node_state !== 'ONLINE' || !f.sample?.node?.services) continue
      const fpm = (f.sample.node.services as ServiceSample[]).find((s) => s.name.includes('php-fpm'))
      if (fpm) map.set(f.server_id, fpm.state)
    }
    return map
  }, [frames])

  const phpRuntimes = (rows ?? []).flatMap((s) =>
    s.runtimes.filter((r) => r.type === 'php').map((r) => ({ ...r, server: s.name, serverId: s.id })),
  )

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="PHP" subtitle="PHP runtimes installed per node and live PHP-FPM health." />

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
        <Card>
          <CardHeader title="PHP runtimes" subtitle="Installed versions by node" />
          {rows === null ? (
            <SkeletonRows rows={2} height="h-9" />
          ) : phpRuntimes.length === 0 ? (
            <EmptyState
              icon={<Layers size={20} />}
              title="No PHP runtimes installed"
              subtitle="Runtimes appear here after installation from the platform or a customer site."
            />
          ) : (
            <div className="divide-y divide-line">
              {phpRuntimes.map((r, i) => (
                <div key={i} className="flex items-center gap-3 py-2.5">
                  <span className="grid h-[28px] w-[28px] place-items-center rounded-[8px] bg-purple-soft text-[9px] font-extrabold text-purple">
                    {r.version}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="text-[11px] font-bold text-ink">PHP {r.version}</div>
                    <div className="text-[9px] text-muted">{r.server}</div>
                  </div>
                  <span className={`status-chip ${r.status === 'available' ? 'status-live' : 'status-warning'}`}>{r.status}</span>
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader title="PHP-FPM service" subtitle="Live states from the agent stream" />
          {fpmStates.size === 0 ? (
            <EmptyState
              icon={<Activity size={20} />}
              title="No live PHP-FPM samples"
              subtitle="States appear while nodes stream. Node coverage is honest — absent means not reporting."
            />
          ) : (
            <div className="divide-y divide-line">
              {[...fpmStates.entries()].map(([serverId, state]) => (
                <div key={serverId} className="flex items-center gap-3 py-2.5">
                  <span className={`h-1.5 w-1.5 rounded-full ${state === 'active' ? 'bg-ok' : 'bg-danger'}`} />
                  <span className="min-w-0 flex-1 truncate text-[11px] font-bold text-ink">
                    {(rows ?? []).find((s) => s.id === serverId)?.name ?? serverId.slice(0, 8)}
                  </span>
                  <span className={`status-chip ${state === 'active' ? 'status-live' : 'status-down'}`}>{state}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
