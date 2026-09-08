import { useMemo } from 'react'
import { Server as ServerIcon, Activity } from 'lucide-react'
import { Card, CardHeader, EmptyState, PageTitle, SkeletonRows } from '@epicpanel/ui'
import { useMetrics } from '@epicpanel/core'
import type { ServiceSample } from '@epicpanel/core'
import { adminview } from '../adminview'
import type { AdminServer } from '../adminview'
import { useEffect, useState } from 'react'

/**
 * Web Servers: per-service fleet view (nginx / apache / openlitespeed /
 * php-fpm / mariadb / docker) built purely from live stream service states.
 */
export function WebServersPage() {
  const [rows, setRows] = useState<AdminServer[] | null>(null)
  const { frames } = useMetrics()

  useEffect(() => {
    adminview.servers().then((r) => setRows(r.servers ?? [])).catch(() => setRows([]))
  }, [])

  const serviceNames = ['nginx', 'apache2', 'httpd', 'openlitespeed', 'lsws', 'php-fpm', 'mariadb', 'mysqld', 'docker']
  const perService = useMemo(() => {
    const map = new Map<string, { name: string; state: string; server: string }[]>()
    for (const f of Object.values(frames)) {
      if (f.node_state !== 'ONLINE' || !f.sample?.node?.services) continue
      for (const s of f.sample.node.services as ServiceSample[]) {
        if (!serviceNames.some((n) => s.name.includes(n))) continue
        const list = map.get(s.name) ?? []
        list.push({ name: s.name, state: s.state, server: f.server_id })
        map.set(s.name, list)
      }
    }
    return map
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [frames])

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Web Servers" subtitle="Web server stack health across live nodes." />
      {Object.keys(perService).length === 0 ? (
        <Card>
          <EmptyState
            icon={<Activity size={20} />}
            title="No service samples"
            subtitle="Service states (nginx, Apache, OpenLiteSpeed, PHP-FPM, MariaDB, Docker) arrive with the live agent stream."
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
          {[...perService.entries()].map(([name, instances]) => {
            const down = instances.filter((i) => i.state !== 'active').length
            return (
              <Card key={name}>
                <CardHeader
                  title={name}
                  subtitle="systemd service"
                  right={<span className={`status-chip ${down > 0 ? 'status-warning' : 'status-live'}`}>{down > 0 ? `${down} down` : 'Running'}</span>}
                />
                <div className="divide-y divide-line">
                  {instances.map((i) => (
                    <div key={i.server} className="flex items-center gap-2.5 py-2">
                      <span className={`h-1.5 w-1.5 rounded-full ${i.state === 'active' ? 'bg-ok' : 'bg-danger'}`} />
                      <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted">{i.server.slice(0, 8)}</span>
                      <span className={`status-chip ${i.state === 'active' ? 'status-live' : 'status-down'}`}>{i.state}</span>
                    </div>
                  ))}
                </div>
              </Card>
            )
          })}
        </div>
      )}

      {rows === null && <Card><SkeletonRows rows={2} /></Card>}
      {rows !== null && rows.length === 0 && (
        <Card className="mt-3.5">
          <EmptyState icon={<ServerIcon size={20} />} title="No nodes" subtitle="Enroll a node to see its web server stack." />
        </Card>
      )}
    </div>
  )
}
