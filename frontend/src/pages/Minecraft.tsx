import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Gamepad2, Plus, RefreshCw, Play, Square, RotateCw, Trash2, ScrollText,
  FileCog, Save, Archive, Clock, Send, FolderOpen, Cpu, Users, Gauge,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { timeAgo } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface ProviderOffer {
  provider: string
  label: string
  versions: string[]
  default: string
  source: string
  supports_tps: boolean
}

interface Metrics {
  players?: number
  max_players?: number
  tps?: number
  mspt?: number
  tps_known?: boolean
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
  restart_policy: string
  restart_count: number
  last_error?: string
  unit_state?: string
  created_at: string
  metrics?: Metrics
}

interface Schedule { id: string; kind: string; cron: string; enabled: boolean; next_run_at: string }
interface Backup { id: string; backup_name: string; created_at: string; size_bytes?: number }
interface FileEntry { name: string; is_dir: boolean; size: number; mod_time: string }

const POLL_MS = 8000

export function MinecraftPage() {
  const { org } = useAuth()
  const [items, setItems] = useState<Instance[] | null>(null)
  const [offers, setOffers] = useState<ProviderOffer[]>([])
  const [servers, setServers] = useState<{ id: string; hostname: string; status: string }[]>([])
  const [err, setErr] = useState('')
  const [showNew, setShowNew] = useState(false)
  const [busy, setBusy] = useState('')
  const navigate = useNavigate()

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
    if (!org) return
    api.get<{ instances: Instance[] }>(`/v1/organizations/${org.id}/minecraft`)
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

  const act = async (inst: Instance, action: string) => {
    setBusy(inst.id + action)
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/${action}`)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy('')
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
        <div className="grid gap-3">
          {items.map((i) => (
            <Card key={i.id}>
              <div className="flex flex-wrap items-center gap-4 p-4">
                <div className="min-w-[220px] flex-1 cursor-pointer" onClick={() => navigate(`/minecraft/${i.id}`)}>
                  <div className="flex items-center gap-2 text-[14px] font-semibold text-ink">
                    {i.name} <StatusBadge status={i.status} />
                  </div>
                  <div className="mt-0.5 text-[11.5px] text-muted">
                    {i.provider} {i.version} · Java {i.java_major} · port {i.port} · RAM {i.xmx_mb} MB · created {timeAgo(i.created_at)}
                  </div>
                  {i.last_error && <div className="mt-1 text-[11px] text-danger">{i.last_error}</div>}
                </div>
                {i.metrics?.tps_known && (
                  <div className="flex items-center gap-1.5 text-[11.5px] text-muted">
                    <Gauge size={14} /> {i.metrics.tps?.toFixed(1)} TPS / {i.metrics.mspt?.toFixed(0)} MSPT
                  </div>
                )}
                {i.metrics && i.metrics.players !== undefined && (
                  <div className="flex items-center gap-1.5 text-[11.5px] text-muted">
                    <Users size={14} /> {i.metrics.players}/{i.metrics.max_players}
                  </div>
                )}
                <div className="flex items-center gap-1.5">
                  <button className="btn-ghost" disabled={busy !== ''} onClick={() => act(i, 'start')}><Play size={13} /> Start</button>
                  <button className="btn-ghost" disabled={busy !== ''} onClick={() => act(i, 'restart')}><RotateCw size={13} /> Restart</button>
                  <button className="btn-ghost" disabled={busy !== ''} onClick={() => act(i, 'stop')}><Square size={13} /> Stop</button>
                  <button className="icon-btn" title="Open panel" onClick={() => navigate(`/minecraft/${i.id}`)}><ScrollText size={15} /></button>
                  <button className="icon-btn" title="Delete"
                    onClick={async () => {
                      if (!(await confirmAction({ title: `Delete ${i.name}?`, message: "The instance and its world files are removed. This cannot be undone.", danger: true, confirmLabel: "Delete instance" }))) return
                      try { await api.del(`/v1/organizations/${org!.id}/minecraft/${i.id}`); await load() } catch (ex: any) { setErr(ex.message) }
                    }}><Trash2 size={15} /></button>
                </div>
              </div>
            </Card>
          ))}
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
  const [tab, setTab] = useState<'console' | 'properties' | 'files' | 'schedules' | 'backups'>('console')
  const [err, setErr] = useState('')

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

  const act = async (action: string) => {
    try { await api.post(`/v1/organizations/${org!.id}/minecraft/${inst!.id}/${action}`); await load() } catch (ex: any) { setErr(ex.message) }
  }

  return (
    <div className="p-6">
      <button className="btn-ghost mb-3" onClick={() => navigate('/minecraft')}>← All instances</button>
      <PageTitle
        title={<span className="flex items-center gap-2">{inst.name} <StatusBadge status={inst.status} /></span>}
        subtitle={`${inst.provider} ${inst.version} · Java ${inst.java_major} · port ${inst.port} · ${inst.xmx_mb} MB · restarts ${inst.restart_count}`}
        actions={
          <>
            <button className="btn-ghost" onClick={() => act('start')}><Play size={13} /> Start</button>
            <button className="btn-ghost" onClick={() => act('restart')}><RotateCw size={13} /> Restart</button>
            <button className="btn-ghost" onClick={() => act('stop')}><Square size={13} /> Stop</button>
          </>
        }
      />
      <ErrorNote message={err || inst.last_error} />
      <div className="mb-4 flex gap-1 border-b border-line">
        {(['console', 'properties', 'files', 'schedules', 'backups'] as const).map((t) => (
          <button key={t} onClick={() => setTab(t)}
            className={`-mb-px border-b-2 px-3 py-2 text-[12.5px] capitalize ${tab === t ? 'border-brand text-brand' : 'border-transparent text-muted hover:text-ink'}`}>
            {t}
          </button>
        ))}
      </div>
      {tab === 'console' && <ConsoleTab inst={inst} />}
      {tab === 'properties' && <PropertiesTab inst={inst} onSaved={load} />}
      {tab === 'files' && <FilesTab inst={inst} />}
      {tab === 'schedules' && <SchedulesTab inst={inst} />}
      {tab === 'backups' && <BackupsTab inst={inst} />}
    </div>
  )
}

function ConsoleTab({ inst }: { inst: Instance }) {
  const { org } = useAuth()
  const [lines, setLines] = useState<{ seq: number; ts: string; text: string }[]>([])
  const [cmd, setCmd] = useState('')
  const [err, setErr] = useState('')
  const boxRef = useRef<HTMLDivElement>(null)

  const pull = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ lines: { seq: number; ts: string; text: string }[] }>(
        `/v1/organizations/${org.id}/minecraft/${inst.id}/console?lines=300`)
      setLines(r.lines ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    pull()
    const t = setInterval(pull, 4000)
    return () => clearInterval(t)
  }, [pull])

  useEffect(() => {
    boxRef.current?.scrollTo({ top: boxRef.current.scrollHeight })
  }, [lines])

  const send = async () => {
    if (!cmd.trim()) return
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/console/command`, { command: cmd.trim() })
      setCmd('')
      setTimeout(pull, 800)
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <Card>
      <CardHeader title="Server console" subtitle="Read-only live tail; commands run through a strict allowlist (no shell)" />
      <div ref={boxRef} className="max-h-[420px] overflow-y-auto rounded bg-ink/95 p-3 font-mono text-[11.5px] leading-5 text-green-200">
        {lines.length === 0 && <div className="text-muted">no log lines yet — start the server to see output</div>}
        {lines.map((l) => (
          <div key={l.seq} className="whitespace-pre-wrap break-all">{l.text}</div>
        ))}
      </div>
      <ErrorNote message={err} />
      <div className="mt-3 flex gap-2">
        <input className="input flex-1 font-mono" value={cmd} onChange={(e) => setCmd(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && send()} placeholder="/say hello — or: list, save-all, op <player>" />
        <button className="btn-brand" onClick={send} disabled={!cmd.trim()}><Send size={13} /> Send</button>
      </div>
    </Card>
  )
}

function PropertiesTab({ inst, onSaved }: { inst: Instance; onSaved: () => void }) {
  const { org } = useAuth()
  const [props, setProps] = useState<Record<string, string> | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.get<{ properties: Record<string, string> }>(`/v1/organizations/${org!.id}/minecraft/${inst.id}/properties`)
      .then((r) => setProps(r.properties ?? {}))
      .catch((ex) => setErr(ex.message))
  }, [org?.id, inst.id]) // eslint-disable-line react-hooks/exhaustive-deps

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
  const keys = Object.keys(props).sort()

  return (
    <Card>
      <CardHeader title="server.properties" subtitle="Applies at the next start/restart"
        right={<button className="btn-brand" onClick={save} disabled={busy}><Save size={13} /> {busy ? 'Saving…' : 'Save'}</button>} />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 md:grid-cols-2">
        {keys.map((k) => (
          <Field key={k} label={k}>
            <input className="input" value={props[k]} onChange={(e) => setProps({ ...props, [k]: e.target.value })} />
          </Field>
        ))}
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

  return (
    <Card>
      <CardHeader title="Files" subtitle="world/, plugins/, mods/, server.properties — browsed via the agent"
        right={<button className="btn-ghost" onClick={() => load(path)}><RefreshCw size={13} /> Refresh</button>} />
      <ErrorNote message={err} />
      <div className="flex items-center gap-1 px-4 pb-2 font-mono text-[12px] text-muted">
        <FolderOpen size={14} />
        <button className="hover:text-brand" onClick={() => setPath('')}>/</button>
        {path.split('/').filter(Boolean).map((seg, i, arr) => (
          <span key={i}>
            <button className="hover:text-brand" onClick={() => setPath(arr.slice(0, i + 1).join('/'))}>{seg}</button>
            {i < arr.length - 1 && '/'}
          </span>
        ))}
      </div>
      <div className="grid gap-1 p-4 pt-1">
        {(entries ?? []).map((f) => (
          <button key={f.name} disabled={!f.is_dir} onClick={() => setPath(path ? `${path}/${f.name}` : f.name)}
            className="flex items-center gap-2 rounded px-2 py-1.5 text-left text-[12.5px] text-ink hover:bg-line/40 disabled:opacity-100">
            <FileCog size={14} className={f.is_dir ? 'text-brand' : 'text-muted'} />
            <span className="flex-1">{f.name}</span>
            <span className="text-[11px] text-muted">{f.is_dir ? 'dir' : `${(f.size / 1024).toFixed(1)} KB`}</span>
          </button>
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
      <CardHeader title="Schedules" subtitle="cron-driven restarts / saves — validated by the panel" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {items.map((s) => (
          <div key={s.id} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <Clock size={14} className="text-brand" />
            <span className="w-20 font-semibold capitalize">{s.kind}</span>
            <span className="flex-1 font-mono text-muted">{s.cron}</span>
            <span className="text-[11px] text-muted">next {timeAgo(s.next_run_at)}</span>
            <button className="icon-btn" onClick={async () => {
              try { await api.del(`/v1/organizations/${org!.id}/minecraft/${inst.id}/schedules/${s.id}`); await load() } catch (ex: any) { setErr(ex.message) }
            }}><Trash2 size={14} /></button>
          </div>
        ))}
        <div className="mt-1 flex items-end gap-2">
          <Field label="Action">
            <Select value={form.kind} onChange={(v) => setForm({ ...form, kind: v })}
              options={[{ value: 'restart', label: 'Restart' }, { value: 'start', label: 'Start' }, { value: 'stop', label: 'Stop' }, { value: 'command', label: 'Command' }]} />
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
    try {
      await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/backups`, {})
      setTimeout(load, 1500)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="World backups" subtitle="Full world snapshots stored by the agent"
        right={<button className="btn-brand" onClick={create} disabled={busy}><Archive size={13} /> {busy ? 'Starting…' : 'Backup now'}</button>} />
      <ErrorNote message={err} />
      <div className="grid gap-1 p-4 pt-0">
        {items.map((b) => (
          <div key={b.id} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <Archive size={14} className="text-brand" />
            <span className="flex-1 font-mono">{b.backup_name}</span>
            {b.size_bytes !== undefined && <span className="text-[11px] text-muted">{(b.size_bytes / 1048576).toFixed(1)} MB</span>}
            <span className="text-[11px] text-muted">{timeAgo(b.created_at)}</span>
            <button className="btn-ghost" onClick={async () => {
              if (!(await confirmAction({ title: `Restore ${b.backup_name}?`, message: "The current world is overwritten by the backup. This cannot be undone.", danger: true, confirmLabel: "Restore" }))) return
              try { await api.post(`/v1/organizations/${org!.id}/minecraft/${inst.id}/backups/${b.id}/restore`, {}); setErr('') } catch (ex: any) { setErr(ex.message) }
            }}>Restore</button>
          </div>
        ))}
        {items.length === 0 && <div className="py-4 text-[12.5px] text-muted">no backups yet</div>}
      </div>
    </Card>
  )
}
