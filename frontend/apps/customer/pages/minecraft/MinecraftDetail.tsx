import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  ArrowLeft, Clock, Download, KeyRound, Play, RotateCcw, RefreshCw, Save,
  Send, Square, Trash2, Upload, Zap, Settings2, TerminalSquare,
} from 'lucide-react'
import { api, useAuth, useMetrics, timeAgo, fmtBytes } from '@epicpanel/core'
import type { FreshnessState } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast } from '@epicpanel/ui'
import { FreshnessBadge } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, InfoNote, Select, FormRow, ConfirmDialog } from '@epicpanel/forms'
import { freshnessOf, formatTPS } from './Minecraft'
import type { MCInstance, MCMetrics, MCProviderOffer } from './Minecraft'

interface ConsoleLine {
  seq: number
  ts: string
  text: string
}

interface MCSchedule {
  id: string
  instance_id: string
  kind: string
  cron: string
  command?: string
  enabled: boolean
  last_run_at?: string
  next_run_at: string
}

interface FileEntry {
  name: string
  is_dir: boolean
  size: number
  mod_time: string
}

interface WorldBackup {
  id: string
  instance_id: string
  name: string
  size_bytes: number
  sha256: string
  created_at: string
}

const CAN_READ = new Set(['installing', 'stopped', 'starting', 'running', 'stopping', 'crashed', 'failed'])

export function MinecraftDetailPage() {
  const { org } = useAuth()
  const { instance_id: instanceId } = useParams()
  const { frames } = useMetrics()
  const [inst, setInst] = useState<MCInstance | null>(null)
  const [offers, setOffers] = useState<MCProviderOffer[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [tab, setTab] = useState<'console' | 'files' | 'properties' | 'versions' | 'schedules' | 'metrics' | 'backups'>('console')

  const load = useCallback(() => {
    if (!org || !instanceId) return
    api
      .get<MCInstance>(`/v1/organizations/${org.id}/minecraft/${instanceId}`)
      .then((m) => setInst(m))
      .catch((e) => setErr(e.message ?? 'Failed to load instance'))
  }, [org?.id, instanceId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    if (!org) return
    api
      .get<{ providers: MCProviderOffer[] }>(`/v1/organizations/${org.id}/minecraft/provider-offers`)
      .then((r) => setOffers(r.providers ?? []))
      .catch(() => setOffers([]))
  }, [load, org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const action = async (name: string, fn: () => Promise<unknown>) => {
    setBusy(name)
    try {
      await fn()
      pushToast('success', `${name} queued`)
      setTimeout(load, 400)
    } catch (ex: any) {
      pushToast('error', ex.message ?? `${name} failed`)
    } finally {
      setBusy('')
    }
  }

  if (!org || !instanceId) return null
  if (!inst) {
    return (
      <div className="fade-up">
        <Link to="/minecraft" className="link mb-3 inline-flex items-center gap-1.5 text-[12px] text-muted">
          <ArrowLeft size={14} /> All instances
        </Link>
        <Card>
          <SkeletonRows rows={2} />
          <ErrorNote message={err} />
        </Card>
      </div>
    )
  }
  const base = `/v1/organizations/${org.id}/minecraft/${instanceId}`

  // Live metrics come from the WS stream (contract #4) — the minecraft
  // envelope is keyed by instance id inside the server frame; the REST
  // snapshot is the fallback (same live store, no polling).
  const wsFrame = frames[inst.server_id]
  const wsApp = wsFrame?.apps?.find((a) => a.website_id === instanceId && a.kind === 'minecraft')
  const liveMetrics: MCMetrics | undefined = wsApp
    ? {
        kind: 'minecraft',
        status: wsApp.status,
        cpu_percent: wsApp.cpu_percent,
        memory_bytes: wsApp.memory_bytes,
        net_rx_bps: wsApp.net_rx_bps,
        net_tx_bps: wsApp.net_tx_bps,
        disk_used_mb: wsApp.disk_used_mb,
        uptime_s: wsApp.uptime_s,
        players: (wsApp as any).players,
        tps: (wsApp as any).tps,
        mspt: (wsApp as any).mspt,
        freshness: wsFrame.freshness,
      }
    : inst.metrics

  const f = freshnessOf(inst)

  return (
    <div className="fade-up">
      <Link to="/minecraft" className="link mb-3 inline-flex items-center gap-1.5 text-[12px] text-muted">
        <ArrowLeft size={14} /> All instances
      </Link>
      <PageTitle
        title={inst.name}
        subtitle={`${inst.provider} ${inst.version} · Java ${inst.java_major} · :${inst.port} · ${inst.desired_state === 'running' ? 'wants to run' : 'wants to be stopped'} · restarts ${liveMetrics?.restart_count ?? inst.restart_count}`}
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            {inst.status !== 'running' && inst.status !== 'starting' && (
              <button className="btn-brand" disabled={busy !== ''} onClick={() => action('Start', () => api.post(`${base}/start`))}>
                <Play size={15} /> Start
              </button>
            )}
            {inst.status === 'running' && (
              <button className="btn-soft" disabled={busy !== ''} onClick={() => action('Restart', () => api.post(`${base}/restart`))}>
                <RotateCcw size={15} /> Restart
              </button>
            )}
            {(CAN_READ.has(inst.status) && inst.status !== 'stopped') && (
              <button className="btn-soft" disabled={busy !== ''} onClick={() => action('Stop', () => api.post(`${base}/stop`))}>
                <Square size={15} /> Stop
              </button>
            )}
            <button className="btn-danger" disabled={busy !== ''} onClick={() => action('Kill', () => api.post(`${base}/kill`))}>
              <Zap size={15} /> Kill
            </button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-6">
        <Card>
          <CardHeader title="Status" />
          <div className="flex flex-col gap-2 px-4 pb-4">
            <StatusBadge status={inst.status} />
            <span className="text-[11px] text-muted">unit: {inst.unit_state || '—'}</span>
            <FreshnessBadge state={f.state} ageMs={f.ageMs} label="Metrics" />
            {inst.last_error && <InfoNote message={inst.last_error} tone="warn" />}
          </div>
        </Card>
        <Card>
          <CardHeader title="Players" />
          <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">
            {liveMetrics?.players != null ? String(liveMetrics.players) : '—'}
          </div>
        </Card>
        <Card>
          <CardHeader title="TPS" />
          <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">{formatTPS(liveMetrics)}</div>
          <span className="block px-4 pb-3 text-[10.5px] text-muted">
            {liveMetrics?.tps_source === 'unsupported' ? 'server type does not expose TPS over RCON' : 'best-effort via RCON'}
          </span>
        </Card>
        <Card>
          <CardHeader title="CPU" />
          <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">
            {liveMetrics?.cpu_percent != null ? `${liveMetrics.cpu_percent.toFixed(1)}%` : '—'}
          </div>
        </Card>
        <Card>
          <CardHeader title="Memory" />
          <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">
            {liveMetrics?.memory_bytes ? fmtBytes(liveMetrics.memory_bytes) : '—'}
          </div>
        </Card>
        <Card>
          <CardHeader title="Disk / uptime" />
          <div className="px-4 pb-4 text-[13px] text-sub">
            {liveMetrics?.disk_used_mb != null ? `${liveMetrics.disk_used_mb} MB` : '—'}
            <span className="block text-[11px] text-muted">{liveMetrics?.uptime_s ? formatUptime(liveMetrics.uptime_s) : '—'}</span>
          </div>
        </Card>
      </div>

      <div className="toolbar mt-5 gap-1">
        {(['console', 'files', 'properties', 'versions', 'schedules', 'backups', 'metrics'] as const).map((t) => (
          <button key={t} className={`btn-ghost capitalize ${tab === t ? 'bg-surface-2 text-ink' : 'text-muted'}`} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </div>

      {tab === 'console' && <ConsolePanel base={base} />}
      {tab === 'files' && <FilesPanel base={base} />}
      {tab === 'properties' && <PropertiesPanel base={base} keys={inst.property_keys ?? []} reload={load} />}
      {tab === 'versions' && <VersionsPanel base={base} inst={inst} offers={offers} reload={load} />}
      {tab === 'schedules' && <SchedulesPanel base={base} />}
      {tab === 'backups' && <BackupsPanel base={base} />}
      {tab === 'metrics' && <MetricsPanel base={base} inst={inst} />}
    </div>
  )
}

function formatUptime(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

/** Console: REST snapshot + WS live tail + ALLOWLISTED command input. The
 * server rejects anything outside the allowlist (never shell) — the input
 * box sends through POST /console/command and the RCON reply lands in the
 * ring. */
function ConsolePanel({ base }: { base: string }) {
  const [lines, setLines] = useState<ConsoleLine[] | null>(null)
  const [live, setLive] = useState(false)
  const [cmd, setCmd] = useState('')
  const [cmdBusy, setCmdBusy] = useState(false)
  const [err, setErr] = useState('')
  const boxRef = useRef<HTMLDivElement>(null)
  const cursorRef = useRef(0)

  const snapshot = useCallback(async () => {
    try {
      const r = await api.get<{ lines: ConsoleLine[]; next_seq: number }>(`${base}/console?lines=300`)
      setLines(r.lines ?? [])
      cursorRef.current = r.next_seq ?? 0
    } catch {
      setLines([])
    }
  }, [base])

  useEffect(() => {
    snapshot()
    // Live tail over the console WS (read-only stream).
    const apiBase = (import.meta as any).env?.VITE_API_URL || `${location.protocol}//${location.hostname}:8080`
    const proto = apiBase.startsWith('https') ? 'wss' : 'ws'
    let ws: WebSocket | null = null
    let closed = false
    let retry: number | null = null
    const connect = () => {
      if (closed) return
      try {
        ws = new WebSocket(`${proto}://${apiBase.replace(/^https?:\/\//, '')}${base}/console/ws`)
      } catch {
        return
      }
      ws.onopen = () => setLive(true)
      ws.onmessage = (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const msg = JSON.parse(ev.data)
          if (msg?.type === 'mc_console') {
            if (Array.isArray(msg.tail)) {
              setLines(msg.tail)
              cursorRef.current = msg.tail.length ? msg.tail[msg.tail.length - 1].seq : cursorRef.current
            } else if (msg.line) {
              cursorRef.current = Math.max(cursorRef.current, msg.line.seq)
              setLines((prev) => [...(prev ?? []).slice(-999), msg.line])
            }
          }
        } catch { /* ignore malformed frames */ }
      }
      ws.onclose = () => {
        setLive(false)
        if (!closed) retry = window.setTimeout(connect, 3000)
      }
    }
    connect()
    return () => {
      closed = true
      if (retry) window.clearTimeout(retry)
      ws?.close()
    }
  }, [base, snapshot])

  useEffect(() => {
    const box = boxRef.current
    if (box) box.scrollTop = box.scrollHeight
  }, [lines])

  const send = async () => {
    if (!cmd.trim()) return
    setErr('')
    setCmdBusy(true)
    try {
      await api.post(`${base}/console/command`, { command: cmd.trim() })
      setCmd('')
      // The response flows through the ring; refresh shortly.
      setTimeout(snapshot, 1200)
    } catch (ex: any) {
      setErr(ex.message ?? 'Command refused')
    } finally {
      setCmdBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader
        title="Console"
        subtitle="stdout/stderr tail + allowlisted commands (RCON)"
        right={
          <span className={`status-chip ${live ? 'status-live' : 'status-off'}`}>
            <span className="h-1.5 w-1.5 rounded-full bg-current" />
            {live ? 'LIVE' : 'OFFLINE'}
          </span>
        }
      />
      <div ref={boxRef} className="tool-panel max-h-[420px] overflow-y-auto px-4 py-3 font-mono text-[11.5px] leading-[1.7] text-[#c9d4e5]">
        {lines === null ? (
          <span className="text-muted">loading…</span>
        ) : lines.length === 0 ? (
          <span className="text-muted">no output yet — start the instance to see logs</span>
        ) : (
          lines.map((l) => (
            <div key={l.seq} className="whitespace-pre-wrap break-all">
              <span className="mr-2 select-none text-[10px] text-muted">{l.ts.slice(11, 19)}</span>
              {l.text}
            </div>
          ))
        )}
      </div>
      <div className="flex items-center gap-2 border-t border-line px-4 py-2.5">
        <TerminalSquare size={15} className="text-muted" />
        <input
          className="input flex-1 font-mono"
          value={cmd}
          onChange={(e) => setCmd(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && !cmdBusy && send()}
          placeholder="allowlisted command, e.g. list, say hello, save-all"
        />
        <button className="btn-brand" onClick={send} disabled={cmdBusy || !cmd.trim()}>
          <Send size={13} /> Send
        </button>
      </div>
      <div className="border-t border-line px-4 py-2.5">
        <ErrorNote message={err} />
        <span className="text-[10.5px] text-muted">
          Only allowlisted console commands are accepted (list, say, whitelist, kick, ban, pardon, op, deop, save-all, tps, …).
          Shell and arbitrary input are refused server-side.
        </span>
      </div>
    </Card>
  )
}

/** Files: browse + upload (config, plugins, mods). server.properties and
 * eula.txt are panel-managed and cannot be overwritten through uploads. */
function FilesPanel({ base }: { base: string }) {
  const [path, setPath] = useState('')
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [uploadPath, setUploadPath] = useState('plugins/README.txt')
  const [uploadText, setUploadText] = useState('')
  const [busy, setBusy] = useState(false)

  const list = useCallback(async () => {
    try {
      const r = await api.get<{ entries?: FileEntry[] }>(`${base}/files?path=${encodeURIComponent(path)}`)
      setEntries(r.entries ?? [])
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Listing failed')
      setEntries([])
    }
  }, [base, path])

  useEffect(() => {
    list()
  }, [list])

  const upload = async () => {
    setBusy(true)
    try {
      const b64 = btoa(unescape(encodeURIComponent(uploadText)))
      await api.post(`${base}/files/upload`, { path: uploadPath, content_b64: b64 })
      pushToast('success', 'Upload queued')
      setUploadText('')
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Upload failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader title="Files" subtitle="server root — worlds, configs, plugins/mods" right={<button className="btn-ghost" onClick={list}><RefreshCw size={13} /> Refresh</button>} />
        <div className="px-4 pb-2">
          <input className="input font-mono text-[12px]" value={path} onChange={(e) => setPath(e.target.value)} placeholder="path (blank = root)" />
        </div>
        {entries === null ? (
          <SkeletonRows rows={2} />
        ) : entries.length === 0 ? (
          <EmptyState title="Empty (or still installing)" subtitle="Upload files or start the server to generate the layout." />
        ) : (
          <div className="px-4 pb-4">
            {entries.map((file) => (
              <div key={file.name} className="flex items-center justify-between border-b border-line/60 py-2 text-[12.5px] last:border-0">
                <span className="text-sub">{file.is_dir ? '[dir] ' : ''}{file.name}</span>
                <span className="text-[10.5px] text-muted">{file.is_dir ? '' : fmtBytes(file.size)} · {timeAgo(file.mod_time)}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
      <Card>
        <CardHeader title="Upload file" subtitle="Small text files (config, plugin list). Plugins/mods go into plugins/ or mods/." />
        <div className="px-4 pb-4">
          <Field label="Path" hint="relative to the server root">
            <input className="input font-mono" value={uploadPath} onChange={(e) => setUploadPath(e.target.value)} placeholder="plugins/README.txt" />
          </Field>
          <Field label="Content">
            <textarea className="input min-h-[160px] font-mono text-[12px]" value={uploadText} onChange={(e) => setUploadText(e.target.value)} placeholder="file content" />
          </Field>
          <button className="btn-brand mt-2" onClick={upload} disabled={busy || !uploadPath}>
            <Upload size={14} /> {busy ? 'Uploading…' : 'Upload'}
          </button>
        </div>
      </Card>
    </div>
  )
}

/** Properties: server.properties editor (protected keys are panel-owned). */
function PropertiesPanel({ base, keys, reload }: { base: string; keys: string[]; reload: () => void }) {
  const [props, setProps] = useState<Record<string, string> | null>(null)
  const [newKey, setNewKey] = useState('view-distance')
  const [newValue, setNewValue] = useState('10')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    api
      .get<{ properties?: Record<string, string> }>(`${base}/properties`)
      .then((r) => setProps(r.properties ?? {}))
      .catch((e) => setErr(e.message ?? 'Failed to load properties'))
  }, [base])

  useEffect(() => {
    load()
  }, [load])

  const save = async (patch: Record<string, string>) => {
    setErr('')
    setBusy(true)
    try {
      await api.put(`${base}/properties`, { properties: patch })
      pushToast('success', 'Properties queued for write (applies at next restart for most keys)')
      reload()
      setTimeout(load, 1200)
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="server.properties" subtitle="Ports, RCON and server-ip are panel-managed and read-only here" />
      <div className="px-4 pb-4">
        <ErrorNote message={err} />
        {props === null ? (
          <SkeletonRows rows={3} />
        ) : Object.keys(props).length === 0 ? (
          <InfoNote message="No properties readable yet — the file is written at install/start." />
        ) : (
          <div className="flex flex-col gap-2">
            {Object.entries(props).map(([k, v]) => (
              <div key={k} className="flex items-center gap-2">
                <span className="w-56 shrink-0 truncate font-mono text-[11.5px] text-sub">{k}</span>
                <input
                  className="input flex-1 font-mono text-[12px]"
                  defaultValue={v}
                  onBlur={(e) => e.target.value !== v && save({ [k]: e.target.value })}
                />
              </div>
            ))}
          </div>
        )}
        <div className="section-title mt-4">Add / override a key</div>
        <FormRow cols={3}>
          <Field label="Key">
            <input className="input font-mono" value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="view-distance" />
          </Field>
          <Field label="Value">
            <input className="input font-mono" value={newValue} onChange={(e) => setNewValue(e.target.value)} placeholder="10" />
          </Field>
          <div className="flex items-end">
            <button className="btn-brand" onClick={() => save({ [newKey]: newValue })} disabled={busy || !newKey}>
              <Save size={14} /> Save property
            </button>
          </div>
        </FormRow>
        {keys.length > 0 && (
          <span className="text-[10.5px] text-muted">configured keys: {keys.join(', ')}</span>
        )}
      </div>
    </Card>
  )
}

/** Versions: change the server type/version (validated against the live
 * provider manifest; applies at next reinstall/start via update). */
function VersionsPanel({ base, inst, offers, reload }: { base: string; inst: MCInstance; offers: MCProviderOffer[]; reload: () => void }) {
  const [provider, setProvider] = useState(inst.provider)
  const [version, setVersion] = useState(inst.version)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const offer = offers.find((o) => o.provider === provider)
  const versionOptions = (offer?.versions ?? []).map((v) => ({ value: v, label: v }))

  const apply = async () => {
    setErr('')
    setBusy('save')
    try {
      await api.patch(base, { version })
      pushToast('success', 'Version saved — restart (or reinstall) to apply')
      reload()
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy('')
    }
  }

  return (
    <Card>
      <CardHeader title="Server versions" subtitle="Version lists follow the provider's live manifest (cached; offline falls back honestly)" />
      <div className="px-4 pb-4">
        <ErrorNote message={err} />
        <FormRow cols={3}>
          <Field label="Server type">
            <Select value={provider} onChange={setProvider} options={offers.map((o) => ({ value: o.provider, label: o.label }))} />
          </Field>
          <Field label="Version" hint={offer ? `source: ${offer.source}` : undefined}>
            <Select value={version} onChange={setVersion} options={versionOptions} placeholder="pick version" disabled={!offer} />
          </Field>
          <div className="flex items-end">
            <button className="btn-brand" onClick={apply} disabled={busy !== ''}>
              <Download size={14} /> {busy !== '' ? 'Saving…' : 'Save version'}
            </button>
          </div>
        </FormRow>
        <InfoNote message={`Java requirement is derived from the version (current: Java ${inst.java_major}); the agent resolves the JVM on the node.`} />
      </div>
    </Card>
  )
}

/** Schedules: lifecycle + allowlisted console commands only. */
function SchedulesPanel({ base }: { base: string }) {
  const [list, setList] = useState<MCSchedule[] | null>(null)
  const [kind, setKind] = useState('restart')
  const [cron, setCron] = useState('0 */6 * * *')
  const [command, setCommand] = useState('save-all')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirm, setConfirm] = useState<MCSchedule | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const load = useCallback(() => {
    api
      .get<{ schedules: MCSchedule[] }>(`${base}/schedules`)
      .then((r) => setList(r.schedules ?? []))
      .catch(() => setList([]))
  }, [base])

  useEffect(() => {
    load()
  }, [load])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, string> = { kind, cron }
      if (kind === 'command') body.command = command
      await api.post(`${base}/schedules`, body)
      pushToast('success', 'Schedule created')
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Create failed')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!confirm) return
    setConfirmBusy(true)
    try {
      await api.del(`${base}/schedules/${confirm.id}`)
      pushToast('success', 'Schedule removed')
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Remove failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="Scheduled tasks" subtitle="Restart, start, stop or run an allowlisted command on a cron schedule" />
      <div className="px-4 pb-4">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Action">
            <Select value={kind} onChange={setKind} options={[
              { value: 'restart', label: 'Restart' },
              { value: 'start', label: 'Start' },
              { value: 'stop', label: 'Stop' },
              { value: 'command', label: 'Console command (allowlisted)' },
            ]} />
          </Field>
          <Field label="Cron (5 fields)" hint="minute hour dom month dow">
            <input className="input font-mono" value={cron} onChange={(e) => setCron(e.target.value)} placeholder="0 */6 * * *" />
          </Field>
        </FormRow>
        {kind === 'command' && (
          <Field label="Command" hint="validated against the console allowlist">
            <input className="input font-mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="save-all" />
          </Field>
        )}
        <button className="btn-brand mt-2" onClick={create} disabled={busy || !cron}>
          <Clock size={14} /> Add schedule
        </button>
        {list === null ? (
          <SkeletonRows rows={1} />
        ) : list.length === 0 ? (
          <InfoNote message="No schedules yet." />
        ) : (
          <div className="mt-3">
            {list.map((s) => (
              <div key={s.id} className="flex items-center justify-between border-b border-line/60 py-2 text-[12.5px] last:border-0">
                <span>
                  <span className="font-semibold capitalize text-ink">{s.kind}</span>
                  {s.command && <span className="ml-2 font-mono text-sub">{s.command}</span>}
                  <span className="ml-2 font-mono text-sub">{s.cron}</span>
                  <span className="ml-2 text-[10.5px] text-muted">next {timeAgo(s.next_run_at)}</span>
                </span>
                <RowActions>
                  <button className="icon-btn" aria-label="Remove schedule" onClick={() => setConfirm(s)}><Trash2 size={14} /></button>
                </RowActions>
              </div>
            ))}
          </div>
        )}
      </div>
      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={remove}
        title="Remove schedule"
        message={`Remove the ${confirm?.kind ?? ''} schedule (${confirm?.cron ?? ''})?`}
        busy={confirmBusy}
      />
    </Card>
  )
}

/** Backups: world-scoped snapshots (save-off → copy → save-on on the node;
 * restore stops the instance, swaps the world, leaves it stopped). */
function BackupsPanel({ base }: { base: string }) {
  const [list, setList] = useState<WorldBackup[] | null>(null)
  const [busy, setBusy] = useState('')
  const [confirm, setConfirm] = useState<WorldBackup | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const load = useCallback(() => {
    api
      .get<{ backups: WorldBackup[] }>(`${base}/backups`)
      .then((r) => setList(r.backups ?? []))
      .catch(() => setList([]))
  }, [base])

  useEffect(() => {
    load()
  }, [load])

  const create = async () => {
    setBusy('create')
    try {
      await api.post(`${base}/backups`, { name: 'world-' + new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-') })
      pushToast('success', 'Backup queued (save-off → snapshot → save-on)')
      setTimeout(load, 3000)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Backup failed')
    } finally {
      setBusy('')
    }
  }

  const restore = async () => {
    if (!confirm) return
    setConfirmBusy(true)
    try {
      await api.post(`${base}/backups/${confirm.id}/restore`, {})
      pushToast('success', 'Restore queued — the instance will be stopped')
      setConfirm(null)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Restore failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="World backups" subtitle="World-scoped snapshots with SHA-256 verification (the full backup engine arrives in a later phase)" />
      <div className="px-4 pb-4">
        <button className="btn-brand" onClick={create} disabled={busy !== ''}>
          <Download size={14} /> {busy === 'create' ? 'Queuing…' : 'Back up world now'}
        </button>
        {list === null ? (
          <SkeletonRows rows={1} />
        ) : list.length === 0 ? (
          <InfoNote message="No backups yet — the server must have run at least once to have a world." />
        ) : (
          <div className="mt-3">
            {list.map((b) => (
              <div key={b.id} className="flex items-center justify-between border-b border-line/60 py-2 text-[12.5px] last:border-0">
                <span>
                  <span className="font-mono text-ink">{b.name}</span>
                  <span className="ml-2 text-[10.5px] text-muted">{fmtBytes(b.size_bytes)} · {timeAgo(b.created_at)}</span>
                </span>
                <RowActions>
                  <button className="btn-ghost" onClick={() => setConfirm(b)}><RotateCcw size={13} /> Restore</button>
                </RowActions>
              </div>
            ))}
          </div>
        )}
      </div>
      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={restore}
        title="Restore world"
        message={`"${confirm?.name ?? ''}" replaces the current world. The instance is stopped first and left stopped; start it after the restore.`}
        busy={confirmBusy}
      />
    </Card>
  )
}

/** Metrics detail: live envelope only (WS-streamed; never polled), with
 * honest TPS/MSPT provenance. */
function MetricsPanel({ base, inst }: { base: string; inst: MCInstance }) {
  const [remote, setRemote] = useState<{ tps_known?: boolean; mspt_known?: boolean; detail?: string; players?: number } | null>(null)
  useEffect(() => {
    // On-demand honest poll (job → RCON) for the provenance detail.
    api
      .post(`${base.replace(/\/minecraft\/.+/, '')}/minecraft/${inst.id}/metrics`, {})
      .catch(() => null)
    // The metrics endpoint itself is GET (live store view).
    api
      .get(`${base}/metrics`)
      .then((r: any) => setRemote(r))
      .catch(() => setRemote(null))
  }, [base, inst.id])

  return (
    <Card>
      <CardHeader title="Metrics provenance" subtitle="CPU/RAM/disk/net stream from the node collector; players/TPS/MSPT come from RCON best-effort" />
      <div className="px-4 pb-4 text-[12.5px] text-sub">
        <ul className="list-disc pl-5">
          <li>CPU, memory, disk, network, uptime: node collector (cgroup + /proc), LIVE/STALE/OFFLINE freshness above.</li>
          <li>Players: RCON <span className="font-mono">list</span> — shown when the server answers.</li>
          <li>TPS: RCON <span className="font-mono">tps</span> on Paper/Purpur; vanilla/Fabric/Forge do not expose it (marked n/a, never faked).</li>
          <li>MSPT: RCON <span className="font-mono">paper mspt</span> when available.</li>
        </ul>
        {remote?.detail && <InfoNote message={remote.detail} tone="warn" />}
        <div className="mt-3 flex items-center gap-2 text-[11px] text-muted">
          <Settings2 size={13} /> Plan-derived heap: {inst.xmx_mb} MB · restart policy {inst.restart_policy} (max {inst.max_restarts} per episode)
        </div>
      </div>
    </Card>
  )
}

// keep the KeyRound import used for the env parity note
export const _mcEnvNote = { icon: KeyRound }
