import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Gamepad2, Plus, RefreshCw, Trash2, Terminal, SlidersHorizontal, FolderOpen,
  Clock, Archive, Settings, Gauge, Users, MemoryStick, Timer, Save, File,
} from 'lucide-react'
import { api, fmtBytes } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { PteroServerPage, PteroConsole, PteroStat, PteroTab, ConsoleLine } from '@/components/ptero'
import { confirmAction } from '@/lib/confirm'
import { timeAgo } from '@/lib/types'

interface ProviderOffer {
  provider: string
  label: string
  versions: string[]
  default: string
  source: string
  supports_tps: boolean
}

// liveMetrics shape (phase7_minecraft.go liveMetrics): TPS/MSPT carry a
// per-provider tps_source — vanilla never exposes TPS over RCON, so '—' is
// the honest render. max_players is typed defensively (live envelope does
// not emit it today; the properties editor owns the authoritative value).
interface Metrics {
  status?: string
  cpu_percent?: number
  memory_bytes?: number
  uptime_s?: number
  players?: number
  max_players?: number
  tps?: number
  mspt?: number
  tps_source?: string
  freshness?: { state?: string; age_ms?: number }
}

interface Instance {
  id: string
  name: string
  provider: string
  version: string
  java_major: number
  status: string
  desired_state: string
  port: number
  xmx_mb: number
  extra_args?: string[]
  restart_policy: string
  max_restarts?: number
  restart_count: number
  last_error?: string
  unit_state?: string
  created_at: string
  metrics?: Metrics
}

interface Schedule { id: string; kind: string; cron: string; command?: string; enabled: boolean; next_run_at: string }
interface Backup { id: string; backup_name: string; created_at: string; size_bytes?: number }
interface FileEntry { name: string; is_dir: boolean; size: number; mod_time: string }

const POLL_MS = 8000
const CONSOLE_POLL_MS = 4000

const TABS: PteroTab[] = [
  { id: 'console', label: 'Console', icon: <Terminal size={14} /> },
  { id: 'properties', label: 'Properties', icon: <SlidersHorizontal size={14} /> },
  { id: 'files', label: 'Files', icon: <FolderOpen size={14} /> },
  { id: 'schedules', label: 'Schedules', icon: <Clock size={14} /> },
  { id: 'backups', label: 'Backups', icon: <Archive size={14} /> },
  { id: 'settings', label: 'Settings', icon: <Settings size={14} /> },
]

// Mirrors backend/internal/minecraft/commands.go ConsoleAllowlist — the API
// rejects everything else before it ever reaches the node.
const COMMAND_PLACEHOLDER =
  '/say hello — allowlist: list, say, whitelist, kick, ban, pardon, op, deop, save-all, save-on, save-off, tps, difficulty, weather, time, gamemode, stop'

const humanUptime = (s?: number) => {
  if (!s || s <= 0) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function MinecraftPage() {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [items, setItems] = useState<Instance[] | null>(null)
  const [offers, setOffers] = useState<ProviderOffer[]>([])
  const [servers, setServers] = useState<{ id: string; hostname: string; status: string }[]>([])
  const [err, setErr] = useState('')
  const [showNew, setShowNew] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ instances: Instance[] }>(`/v1/organizations/${org.id}/minecraft`)
      setItems(r.instances ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    const t = setInterval(load, POLL_MS)
    return () => clearInterval(t)
  }, [load, org?.id])

  const openNew = async () => {
    setErr('')
    setShowNew(true)
    try {
      const [off, srv] = await Promise.all([
        api.get<{ providers: ProviderOffer[] }>(`/v1/organizations/${org!.id}/minecraft/provider-offers`),
        api.get<{ servers: { id: string; hostname: string; status: string }[] }>(`/v1/organizations/${org!.id}/servers`).catch(() => ({ servers: [] })),
      ])
      setOffers(off.providers ?? [])
      setServers((srv.servers ?? []).filter((s) => s.status !== 'offline'))
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  if (items === null) {
    return <div className="p-6"><div className="h-8 w-40 animate-pulse rounded bg-line" /></div>
  }

  return (
    <div className="p-6">
      <PageTitle
        title="Minecraft"
        subtitle="Game server instances — console, properties, schedules, backups"
        actions={
          <>
            <button className="btn-ghost" onClick={load}><RefreshCw size={14} /> Refresh</button>
            <button className="btn-brand" onClick={openNew}><Plus size={14} /> New instance</button>
          </>
        }
      />
      <ErrorNote message={err} />
      {items.length === 0 ? (
        <Card><EmptyState icon={<Gamepad2 size={22} />} title="No Minecraft instances"
          subtitle="Spin up a vanilla, Paper, Fabric or Forge server in seconds."
          action={<button className="btn-brand" onClick={openNew}><Plus size={14} /> New instance</button>} /></Card>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {items.map((i) => {
            const tps = i.metrics?.tps
            const players = i.metrics?.players
            return (
              <div key={i.id}
                className="group cursor-pointer rounded-lg border border-line bg-card p-4 transition-colors hover:border-brand/40"
                onClick={() => navigate(`/minecraft/${i.id}`)}>
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <Gamepad2 size={16} className="shrink-0 text-brand" />
                    <span className="truncate text-[14px] font-semibold text-ink">{i.name}</span>
                  </div>
                  <StatusBadge status={i.status} />
                </div>
                <div className="mt-1 truncate text-[11.5px] text-muted">
                  {i.provider} {i.version} · Java {i.java_major} · port {i.port}
                </div>
                {(tps !== undefined || players !== undefined) && (
                  <div className="mt-2 flex items-center gap-4 text-[11.5px] text-muted">
                    {tps !== undefined && tps > 0 && (
                      <span className="flex items-center gap-1"><Gauge size={12} className="text-brand" /> {tps.toFixed(1)} TPS</span>
                    )}
                    {players !== undefined && (
                      <span className="flex items-center gap-1"><Users size={12} className="text-brand" /> {players} online</span>
                    )}
                  </div>
                )}
                {i.last_error && <div className="mt-1.5 truncate text-[11px] text-danger">{i.last_error}</div>}
                <div className="mt-2 flex items-center justify-between text-[10.5px] text-muted">
                  <span>heap {i.xmx_mb} MB · restarts {i.restart_count}</span>
                  <span>{timeAgo(i.created_at)}</span>
                </div>
              </div>
            )
          })}
        </div>
      )}

      <NewInstanceModal open={showNew} onClose={() => setShowNew(false)} offers={offers} servers={servers} onDone={() => { setShowNew(false); load() }} />
    </div>
  )
}

function NewInstanceModal({ open, onClose, offers, servers, onDone }: {
  open: boolean
  onClose: () => void
  offers: ProviderOffer[]
  servers: { id: string; hostname: string }[]
  onDone: () => void
}) {
  const { org } = useAuth()
  const [form, setForm] = useState({ name: '', provider: '', version: '', max_players: 20, accept_eula: false, restart_policy: 'always', server_id: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const sel = useMemo(() => offers.find((o) => o.provider === form.provider) ?? offers[0], [offers, form.provider])

  useEffect(() => {
    if (open && sel) setForm((f) => ({ ...f, provider: sel.provider, version: sel.default }))
  }, [open, sel])

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const body: any = { ...form, max_players: Number(form.max_players) }
      if (form.server_id) body.server_id = form.server_id
      const r = await api.post<Instance>(`/v1/organizations/${org!.id}/minecraft`, body)
      onDone()
      if (r?.id) window.location.hash = ''
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New Minecraft instance" subtitle="Provisioning runs on the agent; the instance appears here and converges to agent truth.">
      <Field label="Instance name" hint="lowercase letters, digits, - and _">
        <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="survival-1" />
      </Field>
      <Field label="Provider">
        <Select value={form.provider} onChange={(v) => { const o = offers.find((x) => x.provider === v); setForm({ ...form, provider: v, version: o?.default ?? '' }) }}
          options={offers.map((o) => ({ value: o.provider, label: `${o.label} (${o.source})` }))} />
      </Field>
      <Field label="Version" hint={sel?.supports_tps ? 'This provider exposes TPS/MSPT metrics' : 'Vanilla does not expose TPS over RCON'}>
        <Select value={form.version} onChange={(v) => setForm({ ...form, version: v })}
          options={(sel?.versions ?? []).map((v) => ({ value: v, label: v }))} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Max players"><input className="input" type="number" value={form.max_players} onChange={(e) => setForm({ ...form, max_players: Number(e.target.value) })} /></Field>
        <Field label="Restart policy">
          <Select value={form.restart_policy} onChange={(v) => setForm({ ...form, restart_policy: v })}
            options={[{ value: 'always', label: 'Always' }, { value: 'on-failure', label: 'On failure' }, { value: 'never', label: 'Never' }]} />
        </Field>
      </div>
      {servers.length > 1 && (
        <Field label="Server">
          <Select value={form.server_id} onChange={(v) => setForm({ ...form, server_id: v })}
            options={[{ value: '', label: 'Auto-place (least loaded)' }, ...servers.map((s) => ({ value: s.id, label: s.hostname }))]} />
        </Field>
      )}
      <label className="flex cursor-pointer items-center gap-2 py-1 text-[12.5px] text-ink">
        <input type="checkbox" checked={form.accept_eula} onChange={(e) => setForm({ ...form, accept_eula: e.target.checked })} />
        I accept the Minecraft EULA (accounts for the server owner are required)
      </label>
      <ErrorNote message={err} />
      <div className="mt-4 flex justify-end gap-2">
        <button className="btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn-brand" onClick={submit} disabled={busy || !form.name || !form.version || !form.accept_eula}>
          {busy ? 'Creating…' : 'Create instance'}
        </button>
      </div>
    </Modal>
  )
}

export function MinecraftDetailPage() {
  const { id } = useParams()
  const { org } = useAuth()
  const navigate = useNavigate()
  const [inst, setInst] = useState<Instance | null>(null)
  const [tab, setTab] = useState('console')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org || !id) return
    try {
      setInst(await api.get<Instance>(`/v1/organizations/${org.id}/minecraft/${id}`))
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    const t = setInterval(load, POLL_MS)
    return () => clearInterval(t)
  }, [load])

  if (!inst) {
    return <div className="p-6"><ErrorNote message={err} /><div className="h-8 w-52 animate-pulse rounded bg-line" /></div>
  }

  const act = async (action: 'start' | 'restart' | 'stop' | 'kill') => {
    setBusy(true)
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/${action}`)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const m = inst.metrics
  const tps = m?.tps
  const tpsLive = tps !== undefined && tps > 0
  const mspt = m?.mspt
  const mem = m?.memory_bytes
  const memPct = mem !== undefined && mem > 0 && inst.xmx_mb > 0 ? (mem / (inst.xmx_mb * 1048576)) * 100 : null
  const fresh = m?.freshness?.state?.toLowerCase()

  const stats: PteroStat[] = [
    {
      label: 'Players',
      value: m?.players !== undefined ? String(m.players) : '—',
      hint: m?.max_players !== undefined ? `of ${m.max_players} max` : 'connected now',
    },
    {
      label: 'TPS',
      value: tpsLive ? tps.toFixed(1) : '—',
      percent: tpsLive ? Math.min(100, (tps / 20) * 100) : null,
      tone: tpsLive ? (tps >= 19 ? 'brand' : tps >= 15 ? 'warn' : 'danger') : undefined,
      hint: m?.tps_source === 'rcon' ? 'via RCON · target 20'
        : m?.tps_source === 'unsupported' ? 'vanilla does not expose TPS over RCON'
        : m?.tps_source === 'waiting' ? 'waiting for first sample'
        : 'target 20 ticks/s',
    },
    {
      label: 'MSPT',
      value: mspt !== undefined && mspt > 0 ? `${mspt.toFixed(1)} ms` : '—',
      percent: mspt !== undefined && mspt > 0 ? Math.min(100, (mspt / 50) * 100) : null,
      tone: mspt !== undefined && mspt > 0 ? (mspt >= 50 ? 'danger' : mspt >= 25 ? 'warn' : 'brand') : undefined,
      hint: 'target < 50 ms per tick',
    },
    {
      label: 'Memory',
      value: mem !== undefined && mem > 0 ? fmtBytes(mem) : `${inst.xmx_mb} MB`,
      percent: memPct,
      tone: memPct !== null ? (memPct > 90 ? 'danger' : memPct > 75 ? 'warn' : undefined) : undefined,
      hint: `heap -Xmx${inst.xmx_mb} MB allocated`,
    },
    {
      label: 'Uptime',
      value: humanUptime(m?.uptime_s),
      hint: inst.unit_state ? `unit ${inst.unit_state}` : `restarts ${inst.restart_count}`,
    },
  ]

  return (
    <PteroServerPage
      onBack={{ to: () => navigate('/minecraft'), label: 'All instances' }}
      name={inst.name}
      tagline={`${inst.provider} ${inst.version} · Java ${inst.java_major} · port ${inst.port} · created ${timeAgo(inst.created_at)}`}
      status={inst.status}
      power={{
        onStart: () => act('start'),
        onRestart: () => act('restart'),
        onStop: () => act('stop'),
        onKill: () => act('kill'),
        busy,
      }}
      stats={stats}
      tabs={TABS}
      active={tab}
      onTab={setTab}
    >
      <ErrorNote message={err || inst.last_error} />
      {tab === 'console' && <ConsoleTab inst={inst} />}
      {tab === 'properties' && <PropertiesTab inst={inst} onSaved={load} />}
      {tab === 'files' && <FilesTab inst={inst} />}
      {tab === 'schedules' && <SchedulesTab inst={inst} />}
      {tab === 'backups' && <BackupsTab inst={inst} />}
      {tab === 'settings' && <SettingsTab inst={inst} onSaved={load} />}
    </PteroServerPage>
  )
}

function ConsoleTab({ inst }: { inst: Instance }) {
  const { org } = useAuth()
  const [lines, setLines] = useState<ConsoleLine[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const pull = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ lines: ConsoleLine[] }>(
        `/v1/organizations/${org.id}/minecraft/${inst.id}/console?lines=300`)
      setLines(r.lines ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    pull()
    const t = setInterval(pull, CONSOLE_POLL_MS)
    return () => clearInterval(t)
  }, [pull])

  const send = async (cmd: string) => {
    setBusy(true)
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/console/command`, { command: cmd })
      setTimeout(pull, 800)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <div className="mb-2 text-[11.5px] text-muted">
        commands run through a strict allowlist (no shell) — anything else is refused before it reaches the node
      </div>
      <PteroConsole lines={lines} tone="green" onSend={send} placeholder={COMMAND_PLACEHOLDER} sendLabel="Send" busy={busy} />
      <ErrorNote message={err} />
    </div>
  )
}

// The most useful server.properties keys get labeled form fields; every other
// key lands in the Advanced list. PUT merges server-side, so the whole map is
// always sent. Protected keys (server-port, rcon.*, …) are excluded by GET and
// rejected by PUT — they never round-trip through this form.
const LABELED_PROPS: { key: string; label: string; hint?: string; kind: 'text' | 'number' | 'select'; options?: string[] }[] = [
  { key: 'motd', label: 'MOTD', hint: 'shown in the server list', kind: 'text' },
  { key: 'max-players', label: 'Max players', kind: 'number' },
  { key: 'difficulty', label: 'Difficulty', kind: 'select', options: ['peaceful', 'easy', 'normal', 'hard'] },
  { key: 'gamemode', label: 'Game mode', kind: 'select', options: ['survival', 'creative', 'adventure', 'spectator'] },
  { key: 'level-name', label: 'World name', kind: 'text' },
  { key: 'view-distance', label: 'View distance', hint: 'chunks (2-32)', kind: 'number' },
  { key: 'simulation-distance', label: 'Simulation distance', hint: 'chunks', kind: 'number' },
  { key: 'pvp', label: 'PvP', kind: 'select', options: ['true', 'false'] },
  { key: 'online-mode', label: 'Online mode', hint: 'require Mojang accounts', kind: 'select', options: ['true', 'false'] },
  { key: 'white-list', label: 'Whitelist', kind: 'select', options: ['true', 'false'] },
  { key: 'allow-flight', label: 'Allow flight', kind: 'select', options: ['true', 'false'] },
  { key: 'spawn-protection', label: 'Spawn protection', hint: 'radius in blocks, 0 = off', kind: 'number' },
]

function PropertiesTab({ inst, onSaved }: { inst: Instance; onSaved: () => void }) {
  const { org } = useAuth()
  const [props, setProps] = useState<Record<string, string> | null>(null)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.get<{ properties: Record<string, string> }>(`/v1/organizations/${org!.id}/minecraft/${inst.id}/properties`)
      .then((r) => setProps(r.properties ?? {}))
      .catch((ex) => setErr(ex.message))
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const set = (k: string, v: string) => setProps((p) => ({ ...p!, [k]: v }))

  const rename = (oldK: string, newK: string) =>
    setProps((p) => {
      const next: Record<string, string> = {}
      for (const [k, v] of Object.entries(p!)) {
        if (k === oldK) {
          const target = newK.trim() || oldK
          next[target] = v
        } else {
          next[k] = v
        }
      }
      return next
    })

  const remove = (k: string) =>
    setProps((p) => {
      const next = { ...p }
      delete next[k]
      return next
    })

  const add = () => {
    const k = newKey.trim()
    if (!k || props?.[k] !== undefined) return
    setProps((p) => ({ ...p!, [k]: newVal }))
    setNewKey('')
    setNewVal('')
  }

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      await api.put(`/v1/organizations/${org!.id}/minecraft/${inst.id}/properties`, { properties: props })
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  if (props === null) return <Card><div className="p-4 text-[12.5px] text-muted">loading properties…</div></Card>

  const advanced = Object.keys(props).filter((k) => !LABELED_PROPS.some((f) => f.key === k)).sort()
  const optFor = (f: (typeof LABELED_PROPS)[number]) => {
    const v = props[f.key] ?? ''
    const opts = f.options && f.options.includes(v) ? f.options : [v, ...(f.options ?? [])].filter(Boolean)
    return opts.map((o) => ({ value: o, label: o }))
  }

  return (
    <Card>
      <CardHeader title="server.properties" subtitle="applies at the next start/restart — ports and RCON keys are panel-managed"
        right={<button className="btn-brand" onClick={save} disabled={busy}><Save size={13} /> {busy ? 'Saving…' : 'Save'}</button>} />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 md:grid-cols-2">
        {LABELED_PROPS.map((f) => (
          <Field key={f.key} label={f.label} hint={f.hint}>
            {f.kind === 'select' ? (
              <Select value={props[f.key] ?? ''} onChange={(v) => set(f.key, v)} options={optFor(f)} placeholder="(unset)" />
            ) : (
              <input
                className="input" type={f.kind === 'number' ? 'number' : 'text'}
                value={props[f.key] ?? ''} onChange={(e) => set(f.key, e.target.value)} />
            )}
          </Field>
        ))}
      </div>
      <div className="border-t border-line px-4 py-3">
        <div className="mb-2 text-[10.5px] font-semibold uppercase tracking-wider text-muted">Advanced — raw key/value pairs</div>
        <div className="grid gap-1.5">
          {advanced.map((k) => (
            <div key={k} className="flex items-center gap-2">
              <input className="input font-mono text-[12px]" value={k} onChange={(e) => rename(k, e.target.value)} />
              <input className="input flex-1 font-mono text-[12px]" value={props[k]} onChange={(e) => set(k, e.target.value)} />
              <button className="icon-btn shrink-0" title="Remove from payload" onClick={() => remove(k)}><Trash2 size={14} /></button>
            </div>
          ))}
          {advanced.length === 0 && <div className="py-1 text-[12.5px] text-muted">no extra properties</div>}
          <div className="mt-1 flex items-center gap-2">
            <input className="input w-48 font-mono text-[12px]" value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="new-key" />
            <input className="input flex-1 font-mono text-[12px]" value={newVal} onChange={(e) => setNewVal(e.target.value)} placeholder="value" />
            <button className="btn-ghost shrink-0" disabled={!newKey.trim() || props[newKey.trim()] !== undefined} onClick={add}><Plus size={13} /> Add</button>
          </div>
        </div>
      </div>
    </Card>
  )
}

function FilesTab({ inst }: { inst: Instance }) {
  const { org } = useAuth()
  const [path, setPath] = useState('')
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [err, setErr] = useState('')

  const load = useCallback(async (p: string) => {
    if (!org) return
    setErr('')
    try {
      const r = await api.get<{ entries: FileEntry[]; path: string }>(
        `/v1/organizations/${org.id}/minecraft/${inst.id}/files?path=${encodeURIComponent(p)}`)
      setEntries(r.entries ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load(path) }, [load, path])

  const segs = path.split('/').filter(Boolean)
  const sorted = [...(entries ?? [])].sort((a, b) =>
    a.is_dir === b.is_dir ? a.name.localeCompare(b.name) : a.is_dir ? -1 : 1)

  return (
    <Card>
      <CardHeader title="Files" subtitle="world/, plugins/, mods/, server.properties — browsed via the agent"
        right={<button className="btn-ghost" onClick={() => load(path)}><RefreshCw size={13} /> Refresh</button>} />
      <ErrorNote message={err} />
      <div className="flex items-center gap-1 px-4 pb-2 font-mono text-[12px] text-muted">
        <FolderOpen size={14} />
        <button className={path === '' ? 'text-brand' : 'hover:text-brand'} onClick={() => setPath('')}>/</button>
        {segs.map((seg, i) => (
          <span key={i}>
            <span className="mx-0.5">/</span>
            <button className={i === segs.length - 1 ? 'text-brand' : 'hover:text-brand'}
              onClick={() => setPath(segs.slice(0, i + 1).join('/'))}>{seg}</button>
          </span>
        ))}
      </div>
      <div className="px-4 pb-4">
        <div className="grid grid-cols-[1fr_90px_110px] gap-2 border-b border-line px-2 pb-1.5 text-[10.5px] font-semibold uppercase tracking-wider text-muted">
          <span>Name</span><span className="text-right">Size</span><span className="text-right">Modified</span>
        </div>
        {sorted.map((f) => (
          <div key={f.name} className="grid grid-cols-[1fr_90px_110px] items-center gap-2 rounded px-2 py-1.5 text-[12.5px] text-ink hover:bg-line/40">
            {f.is_dir ? (
              <button className="flex min-w-0 items-center gap-2 text-left hover:text-brand" onClick={() => setPath(segs.concat(f.name).join('/'))}>
                <FolderOpen size={14} className="shrink-0 text-brand" /> <span className="truncate">{f.name}</span>
              </button>
            ) : (
              <span className="flex min-w-0 items-center gap-2"><File size={14} className="shrink-0 text-muted" /> <span className="truncate">{f.name}</span></span>
            )}
            <span className="text-right text-[11px] text-muted">{f.is_dir ? '—' : fmtBytes(f.size)}</span>
            <span className="text-right text-[11px] text-muted">{timeAgo(f.mod_time)}</span>
          </div>
        ))}
        {entries !== null && entries.length === 0 && <div className="py-4 text-[12.5px] text-muted">empty directory</div>}
      </div>
    </Card>
  )
}

function SchedulesTab({ inst }: { inst: Instance }) {
  const { org } = useAuth()
  const [items, setItems] = useState<Schedule[]>([])
  const [form, setForm] = useState({ kind: 'restart', cron: '0 4 * * *', command: '' })
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ schedules: Schedule[] }>(`/v1/organizations/${org.id}/minecraft/${inst.id}/schedules`)
      setItems(r.schedules ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load() }, [load])

  const add = async () => {
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/schedules`, form)
      setForm({ kind: 'restart', cron: '0 4 * * *', command: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <Card>
      <CardHeader title="Schedules" subtitle="cron-driven restarts / saves — validated by the panel (5-field cron)" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {items.map((s) => (
          <div key={s.id} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <span title={s.enabled ? 'enabled' : 'disabled'}
              className={`relative inline-flex h-4 w-7 shrink-0 cursor-default items-center rounded-full transition-colors ${s.enabled ? 'bg-brand' : 'bg-line'}`}>
              <span className={`inline-block h-3 w-3 transform rounded-full bg-white transition-transform ${s.enabled ? 'translate-x-3.5' : 'translate-x-0.5'}`} />
            </span>
            <span className="w-20 font-semibold capitalize text-ink">{s.kind}</span>
            {s.command && <span className="hidden min-w-0 flex-1 truncate font-mono text-muted md:block">{s.command}</span>}
            <span className="flex-1 font-mono text-muted">{s.cron}</span>
            <span className="shrink-0 text-[11px] text-muted">next {timeAgo(s.next_run_at)}</span>
            <button className="icon-btn" title="Delete" onClick={async () => {
              try { await api.del(`/v1/organizations/${org!.id}/minecraft/${inst.id}/schedules/${s.id}`); await load() } catch (ex: any) { setErr(ex.message) }
            }}><Trash2 size={14} /></button>
          </div>
        ))}
        {items.length === 0 && <div className="py-3 text-[12.5px] text-muted">no schedules yet</div>}
        <div className="mt-1 flex flex-wrap items-end gap-2">
          <Field label="Action">
            <Select value={form.kind} onChange={(v) => setForm({ ...form, kind: v })}
              options={[{ value: 'restart', label: 'Restart' }, { value: 'start', label: 'Start' }, { value: 'stop', label: 'Stop' }, { value: 'command', label: 'Command (allowlisted)' }]} />
          </Field>
          {form.kind === 'command' && (
            <Field label="Command"><input className="input" value={form.command} onChange={(e) => setForm({ ...form, command: e.target.value })} placeholder="say backup starting" /></Field>
          )}
          <Field label="Cron (5-field)"><input className="input font-mono" value={form.cron} onChange={(e) => setForm({ ...form, cron: e.target.value })} /></Field>
          <button className="btn-brand" disabled={form.kind === 'command' && !form.command} onClick={add}><Plus size={13} /> Add</button>
        </div>
      </div>
    </Card>
  )
}

function BackupsTab({ inst }: { inst: Instance }) {
  const { org } = useAuth()
  const [items, setItems] = useState<Backup[]>([])
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ backups: Backup[] }>(`/v1/organizations/${org.id}/minecraft/${inst.id}/backups`)
      setItems(r.backups ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load() }, [load])

  const create = async () => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/backups`, {})
      setOk('backup queued — snapshots appear here when the agent finishes')
      setTimeout(load, 1500)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="World backups" subtitle="full world snapshots stored by the agent"
        right={<button className="btn-brand" onClick={create} disabled={busy}><Archive size={13} /> {busy ? 'Starting…' : 'Backup now'}</button>} />
      <ErrorNote message={err} />
      {ok && <div className="mx-4 rounded border border-brand/30 bg-brand/10 px-3 py-2 text-[12.5px] text-brand">{ok}</div>}
      <div className="grid gap-2 p-4 pt-2">
        {items.map((b) => (
          <div key={b.id} className="flex items-center gap-3 rounded border border-line px-3 py-2.5">
            <Archive size={15} className="shrink-0 text-brand" />
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[12.5px] text-ink">{b.backup_name}</div>
              <div className="text-[10.5px] text-muted">
                {b.size_bytes !== undefined && b.size_bytes > 0 ? `${fmtBytes(b.size_bytes)} · ` : ''}created {timeAgo(b.created_at)}
              </div>
            </div>
            <button className="btn-ghost shrink-0" onClick={async () => {
              if (!(await confirmAction({ title: `Restore ${b.backup_name}?`, message: 'The current world is overwritten by the backup. This cannot be undone.', confirmLabel: 'Restore' }))) return
              setErr(''); setOk('')
              try {
                await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/backups/${b.id}/restore`, {})
                setOk('restore queued — the instance will be stopped during the restore')
              } catch (ex: any) { setErr(ex.message) }
            }}>Restore</button>
          </div>
        ))}
        {items.length === 0 && <div className="py-3 text-[12.5px] text-muted">no backups yet</div>}
      </div>
    </Card>
  )
}

function SettingsTab({ inst, onSaved }: { inst: Instance; onSaved: () => void }) {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [form, setForm] = useState({
    version: inst.version,
    xmx_mb: String(inst.xmx_mb),
    extra_args: (inst.extra_args ?? []).join(' '),
  })
  const [versions, setVersions] = useState<string[]>([inst.version])
  const [source, setSource] = useState('')
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const [busy, setBusy] = useState(false)

  // Versions + provider source come from provider-offers so PATCH only ever
  // submits values the provider validator accepts.
  useEffect(() => {
    if (!org) return
    api.get<{ providers: ProviderOffer[] }>(`/v1/organizations/${org.id}/minecraft/provider-offers`)
      .then((r) => {
        const o = (r.providers ?? []).find((p) => p.provider === inst.provider)
        if (!o) return
        setSource(o.source)
        setVersions(o.versions.includes(inst.version) ? o.versions : [inst.version, ...o.versions])
      })
      .catch(() => {})
  }, [org?.id, inst.provider, inst.version]) // eslint-disable-line react-hooks/exhaustive-deps

  const xmx = Number(form.xmx_mb)
  const save = async () => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      // PATCH accepts exactly: version, properties, xmx_mb, extra_args
      // (phase7_minecraft.go updateMCRequest). Properties live in their own
      // tab; restart_policy / max_restarts are create-time only. The form is
      // initialized from the instance, so always sending xmx_mb/extra_args is
      // idempotent. Applies at the next start/restart.
      const body: Record<string, unknown> = {
        xmx_mb: Number.isFinite(xmx) && xmx > 0 ? xmx : 0,
        extra_args: form.extra_args.split(/\s+/).filter(Boolean),
      }
      if (form.version !== inst.version) body.version = form.version
      await api.patch(`/v1/organizations/${org!.id}/minecraft/${inst.id}`, body)
      setOk('Saved — applies at the next start/restart')
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const del = async () => {
    if (!(await confirmAction({
      title: `Delete ${inst.name}?`,
      message: 'The instance and its world files are removed from the node. This cannot be undone.',
      confirmLabel: 'Delete instance',
    }))) return
    setErr('')
    try {
      await api.del(`/v1/organizations/${org!.id}/minecraft/${inst.id}`)
      navigate('/minecraft')
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader title="Instance settings" subtitle="changes apply at the next start/restart" />
        <ErrorNote message={err} />
        {ok && <div className="mx-4 rounded border border-brand/30 bg-brand/10 px-3 py-2 text-[12.5px] text-brand">{ok}</div>}
        <div className="grid gap-3 p-4 pt-2">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Provider</div><div className="mt-0.5 text-[12.5px] text-ink">{inst.provider}{source ? ` (${source})` : ''}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Game port</div><div className="mt-0.5 text-[12.5px] text-ink">{inst.port}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Created</div><div className="mt-0.5 text-[12.5px] text-ink">{timeAgo(inst.created_at)}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Restart policy</div><div className="mt-0.5 text-[12.5px] text-ink">{inst.restart_policy} · max {inst.max_restarts ?? '—'}</div></div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Memory (-Xmx)" hint="minimum 512 MB">
              <input className="input" type="number" min={512} value={form.xmx_mb} onChange={(e) => setForm({ ...form, xmx_mb: e.target.value })} />
            </Field>
            <Field label="Version" hint="changing the version re-provisions the jar at the next start">
              <Select value={form.version} onChange={(v) => setForm({ ...form, version: v })}
                options={versions.map((v) => ({ value: v, label: v }))} />
            </Field>
          </div>
          <Field label="Extra JVM args" hint="space-separated single tokens, max 16 (e.g. -XX:+UseG1GC)">
            <input className="input font-mono" value={form.extra_args} onChange={(e) => setForm({ ...form, extra_args: e.target.value })} placeholder="-XX:+UseG1GC" />
          </Field>
          <div className="flex justify-end">
            <button className="btn-brand" onClick={save}
              disabled={busy || (xmx > 0 && xmx < 512)}>
              {busy ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </div>
      </Card>
      <Card>
        <CardHeader title="Danger zone" subtitle="deletion is queued on the node and cannot be undone" />
        <ErrorNote message={err} />
        <div className="flex items-center justify-between gap-3 p-4 pt-2">
          <div className="text-[12.5px] text-muted">Delete this instance, its world files and its schedules.</div>
          <button className="btn-danger" onClick={del}><Trash2 size={13} /> Delete instance</button>
        </div>
      </Card>
    </div>
  )
}
