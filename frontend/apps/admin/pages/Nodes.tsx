import { useCallback, useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { Link, useParams } from 'react-router-dom'
import {
  Server as ServerIcon, Plus, Power, Settings2, ArrowLeft, ShieldAlert, Cpu, MemoryStick,
  HardDrive, RefreshCw,
} from 'lucide-react'
import {
  api, fmtBytes, timeAgo, useMetrics, useFreshness, seedFrames, normalizeBatch, primaryDisk, formatAge,
} from '@epicpanel/core'
import type { Server, SnapshotFrame } from '@epicpanel/core'
import { Card, CardHeader, StatCard, EmptyState, SkeletonRows, FreshnessBadge, StatusBadge } from '@epicpanel/ui'
import { PageTitle, MiniItem } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, ConfirmDialog } from '@epicpanel/forms'
import { pushToast } from '@epicpanel/ui'
import { adminview } from '../adminview'
import type { AdminServer } from '../adminview'

/** Nodes: fleet health grid with per-node drill-down (live WS + LIVE/STALE/OFFLINE). */
export function NodesPage() {
  const [rows, setRows] = useState<AdminServer[] | null>(null)
  const { frames } = useMetrics()

  const load = useCallback(async () => {
    // Seed once from REST, then the WS stream keeps everything current.
    const [srvs, live] = await Promise.all([
      adminview.servers(),
      adminview.live().catch(() => ({ frames: [] as unknown[] })),
    ])
    setRows(srvs.servers ?? [])
    if (live.frames.length > 0) {
      seedFrames(normalizeBatch({ metrics: live.frames }))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const online = (rows ?? []).filter((s) => {
    const f = frames[s.id]
    return f ? f.node_state !== 'OFFLINE' : s.status === 'online'
  }).length
  const maintenance = (rows ?? []).filter((s) => s.maintenance_mode).length

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Nodes" subtitle="Fleet health at a glance — live stream only, never polled." />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Total" value={rows?.length ?? ''} loading={!rows} icon={<ServerIcon size={15} strokeWidth={1.8} />} />
        <StatCard label="Online" value={rows ? online : ''} tone="green" loading={!rows} icon={<ServerIcon size={15} strokeWidth={1.8} />} />
        <StatCard label="Maintenance" value={rows ? maintenance : ''} tone={maintenance ? 'amber' : 'blue'} loading={!rows} icon={<Settings2 size={15} strokeWidth={1.8} />} />
        <StatCard label="Offline" value={rows ? rows.length - online : ''} tone={rows && rows.length - online > 0 ? 'red' : 'blue'} loading={!rows} icon={<Power size={15} strokeWidth={1.8} />} />
      </div>

      {rows === null ? (
        <Card><SkeletonRows rows={2} /></Card>
      ) : rows.length === 0 ? (
        <Card>
          <EmptyState
            icon={<ServerIcon size={20} />}
            title="No nodes enrolled"
            subtitle="Add a server from the Servers page, then run the agent on it."
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
          {rows.map((s) => (
            <NodeCard key={s.id} row={s} frame={frames[s.id] ?? null} />
          ))}
        </div>
      )}
    </div>
  )
}

function NodeCard({ row, frame }: { row: AdminServer; frame: SnapshotFrame | null }) {
  const fresh = useFreshness(frame)
  const node = frame?.sample?.node
  const disk = primaryDisk(node)
  const state = fresh.state
  return (
    <Card className="!p-0">
      <div className="flex items-center gap-2.5 border-b border-line px-4 py-3">
        <span className="grid h-[32px] w-[32px] place-items-center rounded-[9px] bg-brand-soft text-brand">
          <ServerIcon size={15} strokeWidth={1.8} />
        </span>
        <div className="min-w-0 flex-1">
          <Link to={`/nodes/${row.id}`} className="block truncate text-[12px] font-bold text-ink hover:text-brand">{row.name}</Link>
          <span className="block truncate text-[9px] text-muted">{row.hostname || row.os_info || 'no hostname yet'}</span>
        </div>
        {row.maintenance_mode && <span className="status-chip status-warning">Maintenance</span>}
      </div>
      <div className="px-4 py-3">
        <div className="mb-2.5 flex items-center justify-between">
          <span className={`status-chip ${state === 'LIVE' ? 'status-live' : state === 'STALE' ? 'status-warning' : 'status-down'}`}>{state}</span>
          <span className="text-[9px] text-muted">last seen {timeAgo(row.last_seen_at ?? undefined)}</span>
        </div>
        {state === 'OFFLINE' || !node ? (
          <MiniItem tone="red" icon={<Power size={13} />} title={state === 'OFFLINE' ? 'Offline' : 'Awaiting data'} sub={`Last frame ${frame ? formatAge(fresh.ageMs) + ' ago' : 'never'}`} />
        ) : (
          <div className="space-y-2.5">
            <Bar label="CPU" value={`${Math.round(node.cpu_percent)}%`} pct={node.cpu_percent} color="#2563eb" />
            <Bar label="MEM" value={node.memory_total_bytes ? `${fmtBytes(node.memory_used_bytes)} / ${fmtBytes(node.memory_total_bytes)}` : '—'} pct={node.memory_total_bytes ? (node.memory_used_bytes / node.memory_total_bytes) * 100 : 0} color="#0f9d6e" />
            {disk && <Bar label="Disk" value={`${fmtBytes(disk.used_bytes)} / ${fmtBytes(disk.total_bytes)}`} pct={disk.total_bytes ? (disk.used_bytes / disk.total_bytes) * 100 : 0} color="#7c4dff" />}
          </div>
        )}
        <div className="mt-3 flex items-center justify-between border-t border-line pt-2.5 text-[9.5px] text-muted">
          <span>{row.websites} sites · {row.databases} db · {row.jobs_active} jobs</span>
          <Link to={`/nodes/${row.id}`} className="font-bold text-brand hover:underline">Drill down</Link>
        </div>
      </div>
    </Card>
  )
}

function Bar({ label, value, pct, color }: { label: string; value: string; pct: number; color: string }) {
  return (
    <div>
      <div className="mb-[5px] flex items-baseline justify-between gap-3">
        <strong className="text-[10px] text-ink">{label}</strong>
        <span className="text-[9.5px] font-bold text-muted">{value}</span>
      </div>
      <div className="h-[6px] w-full overflow-hidden rounded-full bg-line-soft">
        <div className="h-full rounded-full" style={{ width: `${Math.max(2, Math.min(100, pct))}%`, background: color }} />
      </div>
    </div>
  )
}

/** Per-node drill-down: live host metrics + service states + runtimes. */
export function NodeDetailPage() {
  const { server_id } = useParams()
  const [row, setRow] = useState<AdminServer | null>(null)
  const [missing, setMissing] = useState(false)
  const { frames } = useMetrics()

  useEffect(() => {
    adminview.servers().then((r) => {
      const found = (r.servers ?? []).find((s) => s.id === server_id)
      if (found) setRow(found)
      else setMissing(true)
    }).catch(() => setMissing(true))
  }, [server_id])

  const frame = server_id ? frames[server_id] ?? null : null

  if (missing) {
    return (
      <div className="mx-auto max-w-[1000px] px-4 py-6 lg:px-6">
        <Card><EmptyState icon={<ShieldAlert size={20} />} title="Node not found" subtitle="It may have been removed." /></Card>
      </div>
    )
  }
  if (!row) {
    return <div className="mx-auto max-w-[1000px] px-4 py-6 lg:px-6"><Card><SkeletonRows rows={3} /></Card></div>
  }

  const node = frame?.sample?.node

  return (
    <div className="mx-auto max-w-[1200px] px-4 py-6 lg:px-6">
      <Link to="/nodes" className="mb-3 inline-flex items-center gap-1.5 text-[11px] font-bold text-brand hover:underline">
        <ArrowLeft size={13} /> All nodes
      </Link>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2.5 text-[23px] font-bold tracking-[-.025em] text-ink">
            {row.name} <StatusBadge status={row.status} />
            {row.maintenance_mode && <span className="status-chip status-warning">Maintenance</span>}
          </h1>
          <p className="mt-[5px] text-[12px] text-muted">
            {row.hostname || 'hostname pending'} · {row.os_info || 'unknown OS'} · agent {row.agent_version || '?'}
          </p>
        </div>
        {frame && <NodeFreshness frame={frame} />}
      </div>

      {!frame || !node ? (
        <Card>
          <EmptyState
            icon={<ServerIcon size={20} />}
            title="No live sample"
            subtitle="The agent has not reported yet, or the stream is down. Values stay hidden rather than stale."
          />
        </Card>
      ) : (
        <>
          <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
            <StatCard icon={<Cpu size={15} strokeWidth={1.8} />} label="CPU" value={`${Math.round(node.cpu_percent)}%`} change={`${node.cpu_cores} cores · load ${node.load1?.toFixed(2) ?? '—'}`} up={false} />
            <StatCard icon={<MemoryStick size={15} strokeWidth={1.8} />} label="Memory" value={node.memory_total_bytes ? fmtBytes(node.memory_used_bytes) : '—'} change={node.memory_total_bytes ? `of ${fmtBytes(node.memory_total_bytes)}` : undefined} up={false} />
            <StatCard icon={<HardDrive size={15} strokeWidth={1.8} />} label="Disk" value={primaryDisk(node) ? fmtBytes(primaryDisk(node)!.used_bytes) : '—'} change={primaryDisk(node) ? `of ${fmtBytes(primaryDisk(node)!.total_bytes)}` : undefined} up={false} />
            <StatCard icon={<RefreshCw size={15} strokeWidth={1.8} />} label="Uptime" value={`${Math.floor((node.uptime_s ?? 0) / 86400)}d`} change={`${node.processes} processes`} up={false} />
          </div>

          <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
            <Card>
              <CardHeader title="Services" subtitle="systemd states from the live sample" />
              {(node.services ?? []).length === 0 ? (
                <EmptyState title="No service samples" subtitle="The collector did not report service states on this node." />
              ) : (
                <div className="divide-y divide-line">
                  {(node.services ?? []).map((s) => (
                    <div key={s.name} className="flex items-center gap-3 py-2.5">
                      <span className={`h-1.5 w-1.5 rounded-full ${s.state === 'active' ? 'bg-ok' : 'bg-danger'}`} />
                      <span className="min-w-0 flex-1 truncate font-mono text-[11px] font-bold text-ink">{s.name}</span>
                      <span className={`status-chip ${s.state === 'active' ? 'status-live' : 'status-down'}`}>{s.state}</span>
                    </div>
                  ))}
                </div>
              )}
            </Card>
            <Card>
              <CardHeader title="Runtimes" subtitle="Installed language runtimes" />
              {row.runtimes.length === 0 ? (
                <EmptyState title="No runtimes installed" subtitle="PHP/Node/Python runtimes appear here once installed." />
              ) : (
                <div className="divide-y divide-line">
                  {row.runtimes.map((r, i) => (
                    <div key={i} className="flex items-center gap-3 py-2.5">
                      <span className="min-w-0 flex-1 truncate text-[11px] font-bold text-ink">{r.type} {r.version}</span>
                      <StatusBadge status={r.status} />
                    </div>
                  ))}
                </div>
              )}
            </Card>
          </div>
        </>
      )}
    </div>
  )
}

function NodeFreshness({ frame }: { frame: SnapshotFrame }) {
  const isOnline = frame.node_state !== 'OFFLINE'
  return <span className={`status-chip ${isOnline ? 'status-live' : 'status-down'}`}>{isOnline ? 'Live' : 'Offline'}</span>
}

/** Servers: enrollment + maintenance mode (platform-admin mutations). */
export function ServersPage() {
  const [rows, setRows] = useState<AdminServer[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [name, setName] = useState('')
  const [panelUrl, setPanelUrl] = useState(window.location.origin)
  const [regToken, setRegToken] = useState<{ server: string; token: string } | null>(null)
  const [confirm, setConfirm] = useState<AdminServer | null>(null)
  const { frames } = useMetrics()

  const load = async () => {
    const r = await adminview.servers()
    setRows(r.servers ?? [])
  }
  useEffect(() => {
    void load()
  }, [])

  const register = async () => {
    if (!rows) return
    setErr('')
    setBusy(true)
    try {
      // Server registration stays on the org-scoped Phase 2 endpoint
      // (platform admins pass any org; the fleet is shared infrastructure).
      const res = await api.post<{ server: Server; registration_token: string }>(
        `/v1/organizations/${rows[0].organization_id}/servers`, { name },
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

  const toggleMaintenance = async (s: AdminServer) => {
    await api.patch(`/v1/organizations/${s.organization_id}/servers/${s.id}/maintenance`, { enabled: !s.maintenance_mode })
    pushToast('success', s.maintenance_mode ? `${s.name} is out of maintenance` : `${s.name} placed in maintenance`)
    setConfirm(null)
    await load()
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Servers"
        subtitle="Enroll nodes, rotate tokens and toggle maintenance mode."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add server</button>}
      />
      <Card className="!p-0">
        {rows === null ? (
          <SkeletonRows rows={2} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<ServerIcon size={20} />} title="No servers" subtitle="Register the first node to begin." />
        ) : (
          <div className="divide-y divide-line">
            {rows.map((s) => (
              <div key={s.id} className="flex flex-wrap items-center gap-3 px-4 py-3.5">
                <span className="grid h-[34px] w-[34px] place-items-center rounded-[10px] bg-brand-soft text-brand">
                  <ServerIcon size={16} strokeWidth={1.8} />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-[12px] font-bold text-[#243047]">{s.name}</span>
                    <StatusBadge status={s.status} />
                    {s.maintenance_mode && <span className="status-chip status-warning">Maintenance</span>}
                  </div>
                  <div className="mt-0.5 truncate text-[9.5px] text-muted">
                    {s.hostname || '—'} · agent {s.agent_version || '?'} · last seen {timeAgo(s.last_seen_at ?? undefined)} · {s.websites} sites
                  </div>
                </div>
                {frames[s.id] && <NodeFreshness frame={frames[s.id]} />}
                <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => setConfirm(s)}>
                  <Power size={12} /> {s.maintenance_mode ? 'Resume' : 'Maintenance'}
                </button>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add Server" subtitle="Register a node and connect it with a one-time token.">
        <ErrorNote message={err} />
        <Field label="Server name" hint="A friendly name, e.g. web-01.">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="web-01" />
        </Field>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={register} disabled={busy || !name}>
            {busy ? (<><Spinner size={13} /> Registering…</>) : 'Generate Registration Token'}
          </button>
        </div>
      </Modal>

      <Modal open={!!regToken} onClose={() => setRegToken(null)} title="Connect your server" width="max-w-lg" subtitle="The token is single-use and expires in 24h.">
        {regToken && (
          <div className="space-y-3">
            <p className="text-[11.5px] leading-relaxed text-muted">
              Run on <b className="text-ink">{regToken.server}</b> as root:
            </p>
            <pre className="overflow-x-auto rounded-[10px] bg-navy p-3.5 font-mono text-[11px] leading-relaxed text-slate-200">
{`epicpanel-agent enroll --url ${panelUrl} --token ${regToken.token}
epicpanel-agent run`}
            </pre>
            <div className="flex justify-end">
              <button className="btn-brand" onClick={() => setRegToken(null)}>Done</button>
            </div>
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={() => confirm && toggleMaintenance(confirm)}
        title={confirm?.maintenance_mode ? 'Leave maintenance mode' : 'Enter maintenance mode'}
        message={
          confirm?.maintenance_mode
            ? `${confirm?.name} will accept new jobs and site placements again.`
            : `${confirm?.name} will stop accepting new jobs and placements until maintenance ends.`
        }
        confirmLabel={confirm?.maintenance_mode ? 'Resume' : 'Maintain'}
        danger={!confirm?.maintenance_mode}
      />
    </div>
  )
}
