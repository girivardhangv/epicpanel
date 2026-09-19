import { useEffect, useState } from 'react'
import { Server as ServerIcon, Plus, Power, Activity, Settings2 } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, MiniItem } from '@/components/ref'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { FreshnessBadge } from '@/components/FreshnessBadge'
import { fmtBytes, timeAgo } from '@/lib/types'
import type { Server } from '@/lib/types'
import {
  useMetrics, useFreshness, seedFrames, normalizeBatch, primaryDisk, formatAge,
} from '@/lib/metrics'
import type { SnapshotFrame } from '@/lib/metrics'

export function ServersPage() {
  const { org, user } = useAuth()
  const [servers, setServers] = useState<Server[] | null>(null)
  const { frames } = useMetrics()
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [name, setName] = useState('')
  const [panelUrl, setPanelUrl] = useState(window.location.origin)
  const [regToken, setRegToken] = useState<{ server: string; token: string } | null>(null)

  const isAdmin = !!user?.is_platform_admin

  const load = async () => {
    if (!org) return
    const res = await api.get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`)
    setServers(res.servers ?? [])
    await api
      .get<{ metrics: unknown }>(`/v1/organizations/${org.id}/servers/metrics`)
      .then((r) => seedFrames(normalizeBatch(r)))
      .catch(() => undefined)
  }

  useEffect(() => {
    if (isAdmin) void load()
  }, [org?.id, isAdmin]) // eslint-disable-line react-hooks/exhaustive-deps

  const register = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const res = await api.post<{ server: Server; registration_token: string }>(
        `/v1/organizations/${org.id}/servers`, { name },
      )
      setRegToken({ server: res.server.name, token: res.registration_token })
      setShow(false)
      setName('')
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to register server')
    } finally {
      setBusy(false)
    }
  }

  const toggleMaintenance = async (s: Server) => {
    if (!org) return
    await api.patch(`/v1/organizations/${org.id}/servers/${s.id}/maintenance`, { enabled: !s.maintenance_mode })
    await load()
  }

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <PageTitle title="Servers" subtitle="Connected nodes and live health." />
        <Card>
          <EmptyState
            icon={<ServerIcon size={22} />}
            title="Server management is administrator-only"
            subtitle="Ask your platform administrator to connect servers. Your sites are placed automatically."
          />
        </Card>
      </div>
    )
  }

  const online = (servers ?? []).filter((s) => {
    const f = frames[s.id]
    return (f ? f.node_state !== 'OFFLINE' : s.status === 'online')
  }).length
  const totalMem = (servers ?? []).reduce((acc, s) => {
    const node = frames[s.id]?.sample?.node
    return acc + (node?.memory_total_bytes ?? 0)
  }, 0)

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Servers"
        subtitle="Manage nodes, capacity and service availability."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add server</button>}
      />

      {/* Stats */}
      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <Stat label="Online" value={servers === null ? '—' : `${online}`} icon={<ServerIcon size={15} strokeWidth={1.8} />} tone="green" />
        <Stat label="Warnings" value={servers === null ? '—' : `${(servers ?? []).filter((s) => s.maintenance_mode).length}`} icon={<Settings2 size={15} strokeWidth={1.8} />} tone="amber" />
        <Stat label="Offline" value={servers === null ? '—' : `${(servers ?? []).length - online}`} icon={<Activity size={15} strokeWidth={1.8} />} tone="blue" />
        <Stat label="Total memory" value={servers === null ? '—' : totalMem ? fmtBytes(totalMem) : '—'} icon={<ServerIcon size={15} strokeWidth={1.8} />} tone="purple" />
      </div>

      {servers === null ? (
        <Card><SkeletonRows rows={2} /></Card>
      ) : servers.length === 0 ? (
        <Card>
          <EmptyState
            icon={<ServerIcon size={22} />}
            title="No servers connected"
            subtitle="Register a server, then run the epicpanel-agent binary on it with the one-time token."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Connect Server</button>}
          />
        </Card>
      ) : (
        <div className="space-y-3.5">
          {servers.map((s) => {
            const frame: SnapshotFrame | undefined = frames[s.id]
            return (
              <Card key={s.id} className="!p-0 overflow-hidden">
                <div className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-3.5">
                  <div className="grid h-[34px] w-[34px] place-items-center rounded-[10px] bg-brand-soft text-brand">
                    <ServerIcon size={16} strokeWidth={1.8} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2.5">
                      <span className="text-[12px] font-bold text-[#243047]">{s.name}</span>
                      <StatusBadge status={s.status} />
                      {s.maintenance_mode && <span className="status-chip status-warning">Maintenance</span>}
                      {frame?.degraded && (
                        <span className="status-chip status-warning" title={frame.degraded_reason ?? undefined}>
                          Degraded collection
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 truncate text-[9.5px] text-muted">
                      {s.hostname || '—'} · {s.os_info || 'unknown OS'} · agent {s.agent_version || '?'} · last seen {timeAgo(s.last_seen_at)}
                    </div>
                  </div>
                  <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => toggleMaintenance(s)} title="Blocks new jobs and placement">
                    <Power size={12} /> Maintenance
                  </button>
                </div>
                <div className="px-4 py-4">
                  <ServerMetrics frame={frame ?? null} />
                </div>
              </Card>
            )
          })}
        </div>
      )}

      <Modal open={show} onClose={() => setShow(false)} title="Add Server" subtitle="Register a node and connect it with a one-time token.">
        <ErrorNote message={err} />
        <Field label="Server name" hint="A friendly name, e.g. web-01.">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="web-01" />
        </Field>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={register} disabled={busy || !name}>
            {busy ? 'Registering...' : 'Generate Registration Token'}
          </button>
        </div>
      </Modal>

      <Modal open={!!regToken} onClose={() => setRegToken(null)} title="Connect your server" width="max-w-lg" subtitle="The token is single-use and expires in 24h.">
        {regToken && (
          <div className="space-y-4">
            <p className="text-[11.5px] leading-relaxed text-muted">
              Run these commands <b className="text-ink">as root</b> on <b className="text-ink">{regToken.server}</b>.
            </p>
            <Field label="Panel URL (must be reachable from the server)">
              <input className="input font-mono text-[12px]" value={panelUrl} onChange={(e) => setPanelUrl(e.target.value)} />
            </Field>
            <div>
              <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">1 · Copy the agent binary to the server</div>
              <pre className="overflow-x-auto rounded-[10px] bg-navy p-3.5 font-mono text-[11px] leading-relaxed text-slate-200">
{`# on the panel machine (repo root):
cd backend && go build -o epicpanel-agent ./cmd/agent
scp epicpanel-agent root@${regToken.server}:/usr/local/bin/`}
              </pre>
            </div>
            <div>
              <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">2 · Enroll (exchanges token, persists config)</div>
              <pre className="overflow-x-auto rounded-[10px] bg-navy p-3.5 font-mono text-[11px] leading-relaxed text-slate-200">
{`epicpanel-agent enroll --url ${panelUrl} --token ${regToken.token}`}
              </pre>
            </div>
            <div>
              <div className="mb-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">3 · Run it (systemd recommended)</div>
              <pre className="overflow-x-auto rounded-[10px] bg-navy p-3.5 font-mono text-[11px] leading-relaxed text-slate-200">
{`epicpanel-agent run   # foreground

# or install as a service:
cat >/etc/systemd/system/epicpanel-agent.service <<UNIT
[Unit]
Description=EpicPanel Agent
After=network-online.target

[Service]
ExecStart=/usr/local/bin/epicpanel-agent run
Restart=always

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload && systemctl enable --now epicpanel-agent`}
              </pre>
            </div>
            <div className="flex justify-end">
              <button className="btn-brand" onClick={() => setRegToken(null)}>Done</button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}


function ServerMetrics({ frame }: { frame: SnapshotFrame | null }) {
  const fresh = useFreshness(frame)

  if (!frame) {
    return (
      <MiniItem
        tone="amber"
        icon={<Activity size={14} strokeWidth={1.8} />}
        title="Waiting for the first heartbeat"
        sub="The agent has not reported metrics yet"
      />
    )
  }

  if (fresh.state === 'OFFLINE') {
    return (
      <MiniItem
        tone="red"
        icon={<Activity size={14} strokeWidth={1.8} />}
        title="Server offline"
        sub={`Last sample ${formatAge(fresh.ageMs)} ago — values hidden until the agent reports again`}
      />
    )
  }

  const node = frame.sample?.node
  const disk = primaryDisk(node)

  return (
    <div>
      {frame.degraded && (
        <div className="mb-3.5 flex flex-wrap items-center gap-2">
          <span className="status-chip status-warning" title={frame.degraded_reason ?? undefined}>
            Degraded collection
          </span>
        </div>
      )}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <ResBar label="CPU" value={`${Math.round(node?.cpu_percent ?? 0)}%`} pct={node?.cpu_percent ?? 0} color="#2563eb" />
        <ResBar
          label="Memory"
          value={node?.memory_total_bytes ? `${fmtBytes(node.memory_used_bytes)} / ${fmtBytes(node.memory_total_bytes)}` : '—'}
          pct={node?.memory_total_bytes ? (node.memory_used_bytes / node.memory_total_bytes) * 100 : 0}
          color="#0f9d6e"
        />
        <ResBar
          label="Disk"
          value={disk && disk.total_bytes ? `${fmtBytes(disk.used_bytes)} / ${fmtBytes(disk.total_bytes)}` : '—'}
          pct={disk && disk.total_bytes ? (disk.used_bytes / disk.total_bytes) * 100 : 0}
          color="#7c4dff"
        />
      </div>
    </div>
  )
}

function ResBar({ label, value, pct, color }: { label: string; value: string; pct: number; color: string }) {
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

function Stat({ label, value, icon, tone }: { label: string; value: React.ReactNode; icon: React.ReactNode; tone: 'blue' | 'green' | 'amber' | 'purple' }) {
  const toneCls: Record<string, string> = {
    blue: 'bg-brand-soft text-brand',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    purple: 'bg-purple-soft text-purple',
  }
  return (
    <div className="card p-[15px]">
      <div className="flex items-start justify-between gap-2.5">
        <div>
          <div className="text-[10px] font-bold uppercase tracking-[.06em] text-muted">{label}</div>
          <div className="mt-[5px] text-[24px] font-extrabold leading-[1.1] tracking-[-.04em] text-ink">{value}</div>
        </div>
        <div className={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px] ${toneCls[tone]}`}>{icon}</div>
      </div>
    </div>
  )
}
