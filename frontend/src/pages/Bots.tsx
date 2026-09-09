import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Bot, Plus, RefreshCw, Trash2, Terminal, KeyRound, FolderOpen, Clock,
  GitBranch, Settings, FileUp, Cpu, MemoryStick,
} from 'lucide-react'
import { api, fmtBytes } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { PteroServerPage, PteroConsole, PteroStat, PteroTab, ConsoleLine } from '@/components/ptero'
import { confirmAction } from '@/lib/confirm'
import { timeAgo } from '@/lib/types'

interface VersionOffer { runtime: string; label: string; versions: string[]; default: string }

// liveMetrics shape (phase8_discord.go botView/liveMetrics): memory_bytes is
// the process RSS; no memory limit is exposed over the API, so no mem bar.
interface Metrics {
  status?: string
  cpu_percent?: number
  memory_bytes?: number
  uptime_s?: number
  restart_count?: number
  freshness?: { state?: string; age_ms?: number }
}

interface Bot {
  id: string
  name: string
  runtime: string
  runtime_version: string
  status: string
  desired_state: string
  startup_file: string
  startup_command?: string
  build_command?: string
  restart_policy: string
  max_restarts?: number
  restart_count: number
  git_repo_url: string
  git_branch: string
  env_keys: string[]
  last_error?: string
  unit_state?: string
  created_at: string
  metrics?: Metrics
}

interface Schedule { id: string; kind: string; cron: string; enabled: boolean; next_run_at: string }
interface FileEntry { name: string; is_dir: boolean; size: number; mod_time: string }

// bot_files listing rides the jobs pipeline (listFiles enqueues; the entries
// come back on the job result — phase8_discord.go listFiles is async).
interface BotJob {
  id: string
  type: string
  status: string
  payload?: { path?: string } | null
  result?: { entries?: FileEntry[] | null } | null
  error?: string
  created_at: string
  finished_at?: string | null
}

const POLL_MS = 8000
const TABS: PteroTab[] = [
  { id: 'console', label: 'Console', icon: <Terminal size={14} /> },
  { id: 'env', label: 'Environment', icon: <KeyRound size={14} /> },
  { id: 'files', label: 'Files', icon: <FolderOpen size={14} /> },
  { id: 'schedules', label: 'Schedules', icon: <Clock size={14} /> },
  { id: 'deploy', label: 'Deploy', icon: <GitBranch size={14} /> },
  { id: 'settings', label: 'Settings', icon: <Settings size={14} /> },
]

const humanUptime = (s?: number) => {
  if (!s || s <= 0) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function BotsPage() {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [items, setItems] = useState<Bot[] | null>(null)
  const [offers, setOffers] = useState<VersionOffer[]>([])
  const [err, setErr] = useState('')
  const [showNew, setShowNew] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ bots: Bot[] }>(`/v1/organizations/${org.id}/bots`)
      setItems(r.bots ?? [])
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
      const r = await api.get<{ runtimes: VersionOffer[] }>(`/v1/organizations/${org!.id}/bots/runtime-offers`)
      setOffers(r.runtimes ?? [])
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
        title="Discord Bots"
        subtitle="Node.js / Python / Go bot hosting — deploy, secrets, console, schedules"
        actions={
          <>
            <button className="btn-ghost" onClick={load}><RefreshCw size={14} /> Refresh</button>
            <button className="btn-brand" onClick={openNew}><Plus size={14} /> New bot</button>
          </>
        }
      />
      <ErrorNote message={err} />
      {items.length === 0 ? (
        <Card><EmptyState icon={<Bot size={22} />} title="No bots yet"
          subtitle="Deploy from git or upload files — env vars stay encrypted at rest."
          action={<button className="btn-brand" onClick={openNew}><Plus size={14} /> New bot</button>} /></Card>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {items.map((b) => {
            const cpu = b.metrics?.cpu_percent
            const mem = b.metrics?.memory_bytes
            return (
              <div key={b.id}
                className="group cursor-pointer rounded-lg border border-line bg-card p-4 transition-colors hover:border-brand/40"
                onClick={() => navigate(`/bots/${b.id}`)}>
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <Bot size={16} className="shrink-0 text-brand" />
                    <span className="truncate text-[14px] font-semibold text-ink">{b.name}</span>
                  </div>
                  <StatusBadge status={b.status} />
                </div>
                <div className="mt-1 truncate text-[11.5px] text-muted">
                  {b.runtime} {b.runtime_version} · {b.startup_file || b.git_repo_url || 'no code yet'}
                </div>
                {(cpu !== undefined || mem !== undefined) && (
                  <div className="mt-2 flex items-center gap-4 text-[11.5px] text-muted">
                    {cpu !== undefined && (
                      <span className="flex items-center gap-1"><Cpu size={12} className="text-brand" /> {cpu.toFixed(1)}%</span>
                    )}
                    {mem !== undefined && (
                      <span className="flex items-center gap-1"><MemoryStick size={12} className="text-brand" /> {fmtBytes(mem)} RSS</span>
                    )}
                  </div>
                )}
                {b.last_error && <div className="mt-1.5 truncate text-[11px] text-danger">{b.last_error}</div>}
                <div className="mt-2 flex items-center justify-between text-[10.5px] text-muted">
                  <span>restarts {b.restart_count}</span>
                  <span>{timeAgo(b.created_at)}</span>
                </div>
              </div>
            )
          })}
        </div>
      )}
      <NewBotModal open={showNew} onClose={() => setShowNew(false)} offers={offers} onDone={() => { setShowNew(false); load() }} />
    </div>
  )
}

function NewBotModal({ open, onClose, offers, onDone }: {
  open: boolean; onClose: () => void; offers: VersionOffer[]; onDone: () => void
}) {
  const { org } = useAuth()
  const [form, setForm] = useState({ name: '', runtime: '', runtime_version: '', startup_file: '', build_command: '', git_repo_url: '', git_branch: '', restart_policy: 'on-failure' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const sel = offers.find((o) => o.runtime === form.runtime) ?? offers[0]

  useEffect(() => {
    if (open && sel) setForm((f) => ({ ...f, runtime: sel.runtime, runtime_version: sel.default }))
  }, [open, sel])

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/bots`, form)
      onDone()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New Discord bot" subtitle="Code can be uploaded after creation, or pulled from git on first deploy.">
      <Field label="Bot name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="mod-bot" /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Runtime">
          <Select value={form.runtime} onChange={(v) => { const o = offers.find((x) => x.runtime === v); setForm({ ...form, runtime: v, runtime_version: o?.default ?? '' }) }}
            options={offers.map((o) => ({ value: o.runtime, label: o.label }))} />
        </Field>
        <Field label="Version">
          <Select value={form.runtime_version} onChange={(v) => setForm({ ...form, runtime_version: v })}
            options={(sel?.versions ?? []).map((v) => ({ value: v, label: v }))} />
        </Field>
      </div>
      <Field label="Startup file" hint="entry point, e.g. index.js / main.py"><input className="input" value={form.startup_file} onChange={(e) => setForm({ ...form, startup_file: e.target.value })} placeholder="index.js" /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Git repo (optional)"><input className="input" value={form.git_repo_url} onChange={(e) => setForm({ ...form, git_repo_url: e.target.value })} placeholder="https://github.com/you/bot" /></Field>
        <Field label="Branch"><input className="input" value={form.git_branch} onChange={(e) => setForm({ ...form, git_branch: e.target.value })} placeholder="main" /></Field>
      </div>
      <ErrorNote message={err} />
      <div className="mt-4 flex justify-end gap-2">
        <button className="btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn-brand" onClick={submit} disabled={busy || !form.name}>{busy ? 'Creating…' : 'Create bot'}</button>
      </div>
    </Modal>
  )
}

export function BotDetailPage() {
  const { id } = useParams()
  const { org } = useAuth()
  const navigate = useNavigate()
  const [bot, setBot] = useState<Bot | null>(null)
  const [tab, setTab] = useState('console')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org || !id) return
    try {
      setBot(await api.get<Bot>(`/v1/organizations/${org.id}/bots/${id}`))
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    const t = setInterval(load, POLL_MS)
    return () => clearInterval(t)
  }, [load])

  if (!bot) {
    return <div className="p-6"><ErrorNote message={err} /><div className="h-8 w-52 animate-pulse rounded bg-line" /></div>
  }

  const act = async (action: 'start' | 'restart' | 'stop') => {
    setBusy(true)
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/bots/${bot.id}/${action}`)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  // No memory-limit bar: the API never exposes bot limits (anti-drift rule —
  // the same numbers the bars show must come from the backend).
  const m = bot.metrics
  const cpu = m?.cpu_percent
  const fresh = m?.freshness?.state?.toLowerCase()
  const stats: PteroStat[] = [
    {
      label: 'CPU',
      value: cpu !== undefined ? `${cpu.toFixed(1)}%` : '—',
      percent: cpu ?? null,
      hint: fresh ? `sample ${fresh}` : undefined,
      tone: cpu !== undefined ? (cpu > 90 ? 'danger' : cpu > 70 ? 'warn' : undefined) : undefined,
    },
    {
      label: 'Memory',
      value: m?.memory_bytes !== undefined ? fmtBytes(m.memory_bytes) : '—',
      hint: fresh ? `rss · sample ${fresh}` : 'rss',
    },
    { label: 'Uptime', value: m?.uptime_s !== undefined ? humanUptime(m.uptime_s) : '—', hint: bot.unit_state ? `unit ${bot.unit_state}` : undefined },
    { label: 'Restarts', value: String(m?.restart_count ?? bot.restart_count), hint: `${bot.restart_policy} · max ${bot.max_restarts ?? '—'}` },
  ]

  return (
    <PteroServerPage
      onBack={{ to: () => navigate('/bots'), label: 'All bots' }}
      name={bot.name}
      tagline={`${bot.runtime} ${bot.runtime_version} · ${bot.startup_file || bot.startup_command || 'no startup file'} · created ${timeAgo(bot.created_at)}`}
      status={bot.status}
      power={{ onStart: () => act('start'), onRestart: () => act('restart'), onStop: () => act('stop'), busy }}
      stats={stats}
      tabs={TABS}
      active={tab}
      onTab={setTab}
    >
      <ErrorNote message={err || bot.last_error} />
      {tab === 'console' && <ConsoleTab bot={bot} />}
      {tab === 'env' && <EnvTab bot={bot} onSaved={load} />}
      {tab === 'files' && <FilesTab bot={bot} onSaved={load} />}
      {tab === 'schedules' && <SchedulesTab bot={bot} />}
      {tab === 'deploy' && <DeployTab bot={bot} onSaved={load} />}
      {tab === 'settings' && <SettingsTab bot={bot} onSaved={load} />}
    </PteroServerPage>
  )
}

function ConsoleTab({ bot }: { bot: Bot }) {
  const { org } = useAuth()
  const [lines, setLines] = useState<ConsoleLine[]>([])
  const [err, setErr] = useState('')

  const pull = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ lines: ConsoleLine[] }>(
        `/v1/organizations/${org.id}/bots/${bot.id}/console?lines=300`)
      setLines(r.lines ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, bot.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    pull()
    const t = setInterval(pull, 5000)
    return () => clearInterval(t)
  }, [pull])

  return (
    <div>
      <div className="mb-2 text-[11.5px] text-muted">
        read-only — bots are systemd-managed on the node, so there is no command input · env values are scrubbed before leaving the node
      </div>
      <PteroConsole lines={lines} tone="sky" />
      <ErrorNote message={err} />
    </div>
  )
}

function EnvTab({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const { org } = useAuth()
  const [keys, setKeys] = useState<string[] | null>(null)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    try {
      // GET env returns {keys, count, note} — values never leave the server.
      const r = await api.get<{ keys: string[] }>(`/v1/organizations/${org.id}/bots/${bot.id}/env`)
      setKeys(r.keys ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, bot.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load() }, [load])

  const save = async (vars: Record<string, string>) => {
    setBusy(true)
    setErr('')
    try {
      // PUT env: {vars: {KEY: value}} — set/replace; applies at next start/restart.
      await api.put(`/v1/organizations/${org!.id}/bots/${bot.id}/env`, { vars })
      setNewKey(''); setNewVal('')
      await load()
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const del = async (k: string) => {
    setErr('')
    try {
      await api.del(`/v1/organizations/${org!.id}/bots/${bot.id}/env/${encodeURIComponent(k)}`)
      await load()
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <Card>
      <CardHeader title="Environment & secrets" subtitle="values are AES-GCM encrypted at rest, write-only — set a key again to replace it" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {(keys ?? []).map((k) => (
          <div key={k} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <KeyRound size={14} className="text-brand" />
            <span className="w-52 truncate font-mono text-ink">{k}</span>
            <span className="flex-1 font-mono text-muted">••••••••</span>
            <button className="icon-btn" title="Delete" onClick={() => del(k)}><Trash2 size={14} /></button>
          </div>
        ))}
        {keys !== null && keys.length === 0 && <div className="py-3 text-[12.5px] text-muted">no env vars yet</div>}
        <div className="mt-2 flex items-end gap-2">
          <Field label="Key"><input className="input font-mono" value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="DISCORD_TOKEN" /></Field>
          <Field label="Value"><input className="input font-mono" type="password" value={newVal} onChange={(e) => setNewVal(e.target.value)} /></Field>
          <button className="btn-brand" disabled={busy || !newKey || !newVal} onClick={() => save({ [newKey]: newVal })}>
            {newKey && (keys ?? []).includes(newKey) ? 'Replace' : 'Set'}
          </button>
        </div>
      </div>
    </Card>
  )
}

function FilesTab({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const { org } = useAuth()
  const [path, setPath] = useState('')
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const seqRef = useRef(0)

  const openPath = useCallback(async (p: string) => {
    const my = ++seqRef.current
    setErr('')
    setBusy(true)
    const ts = (j: BotJob) => j.finished_at ?? j.created_at
    try {
      const r = await api.get<{ entries?: FileEntry[] }>(
        `/v1/organizations/${org!.id}/bots/${bot.id}/files?path=${encodeURIComponent(p)}`)
      if (seqRef.current !== my) return
      // Sync contract (response carries entries directly).
      if (Array.isArray(r.entries)) { setEntries(r.entries); return }
      // Async contract: bot_files job was enqueued — poll /jobs for its result.
      for (let i = 0; i < 10; i++) {
        await new Promise((res) => setTimeout(res, 800))
        if (seqRef.current !== my) return
        const jl = await api.get<{ jobs: BotJob[] }>(`/v1/organizations/${org!.id}/bots/${bot.id}/jobs?limit=10`)
        const mine = (jl.jobs ?? [])
          .filter((j) => j.type === 'bot_files' && (j.payload?.path ?? '') === p)
          .sort((a, b) => (ts(a) < ts(b) ? 1 : ts(a) > ts(b) ? -1 : 0))
        if (!mine[0]) continue
        if (mine[0].status === 'success') { setEntries(mine[0].result?.entries ?? []); return }
        if (mine[0].status === 'failed') { setErr(mine[0].error || 'file listing failed on the node'); return }
      }
      if (seqRef.current === my) setErr('listing timed out — try again')
    } catch (ex: any) {
      if (seqRef.current === my) setErr(ex.message)
    } finally {
      if (seqRef.current === my) setBusy(false)
    }
  }, [org?.id, bot.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { openPath(path) }, [openPath, path])

  // Upload contract (phase8_discord.go uploadFile): JSON body
  // {path: "<dir>/<filename>", content_b64: base64} — the write is queued on
  // the node, so the fresh listing may lag a moment.
  const upload = async (f: File) => {
    setErr('')
    try {
      const b64 = await new Promise<string>((resolve, reject) => {
        const fr = new FileReader()
        fr.onload = () => resolve(String(fr.result).split(',')[1] ?? '')
        fr.onerror = () => reject(new Error('could not read file'))
        fr.readAsDataURL(f)
      })
      await api.post(`/v1/organizations/${org!.id}/bots/${bot.id}/files/upload`, {
        path: path ? `${path}/${f.name}` : f.name,
        content_b64: b64,
      })
      onSaved()
      setTimeout(() => openPath(path), 1200)
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const segs = path.split('/').filter(Boolean)
  return (
    <Card>
      <CardHeader title="Files" subtitle="bot working directory — upload code, then restart to apply"
        right={<>
          <input ref={fileRef} type="file" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; if (f) { upload(f); e.target.value = '' } }} />
          <button className="btn-ghost" disabled={busy} onClick={() => fileRef.current?.click()}><FileUp size={13} /> Upload</button>
          <button className="btn-ghost" disabled={busy} onClick={() => openPath(path)}><RefreshCw size={13} /> Refresh</button>
        </>} />
      <ErrorNote message={err} />
      <div className="flex items-center gap-1 px-4 pb-2 font-mono text-[12px] text-muted">
        <FolderOpen size={14} />
        <button className={path === '' ? 'text-brand' : 'hover:text-brand'} onClick={() => setPath('')}>root</button>
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
        {(entries ?? []).map((f) => (
          <div key={f.name} className="grid grid-cols-[1fr_90px_110px] items-center gap-2 rounded px-2 py-1.5 text-[12.5px] text-ink hover:bg-line/40">
            {f.is_dir ? (
              <button className="flex min-w-0 items-center gap-2 text-left hover:text-brand" onClick={() => setPath(segs.concat(f.name).join('/'))}>
                <FolderOpen size={14} className="shrink-0 text-brand" /> <span className="truncate">{f.name}</span>
              </button>
            ) : (
              <span className="flex min-w-0 items-center gap-2"><FileUp size={14} className="shrink-0 text-muted" /> <span className="truncate">{f.name}</span></span>
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

function SchedulesTab({ bot }: { bot: Bot }) {
  const { org } = useAuth()
  const [items, setItems] = useState<Schedule[]>([])
  const [form, setForm] = useState({ kind: 'restart', cron: '*/30 * * * *' })
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ schedules: Schedule[] }>(`/v1/organizations/${org.id}/bots/${bot.id}/schedules`)
      setItems(r.schedules ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, bot.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load() }, [load])

  const add = async () => {
    setErr('')
    try {
      // POST schedules: {kind: restart|start|stop, cron: 5-field} — custom
      // commands are not schedulable by design.
      await api.post(`/v1/organizations/${org!.id}/bots/${bot.id}/schedules`, form)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <Card>
      <CardHeader title="Schedules" subtitle="cron-driven restart / start / stop — custom commands are not schedulable" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {items.map((s) => (
          <div key={s.id} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <Clock size={14} className="text-brand" />
            <span className="w-20 font-semibold capitalize text-ink">{s.kind}</span>
            <span className="flex-1 font-mono text-muted">{s.cron}</span>
            <span className="text-[11px] text-muted">next {timeAgo(s.next_run_at)}</span>
            <button className="icon-btn" title="Delete" onClick={async () => {
              try { await api.del(`/v1/organizations/${org!.id}/bots/${bot.id}/schedules/${s.id}`); await load() } catch (ex: any) { setErr(ex.message) }
            }}><Trash2 size={14} /></button>
          </div>
        ))}
        {items.length === 0 && <div className="py-3 text-[12.5px] text-muted">no schedules yet</div>}
        <div className="mt-1 flex items-end gap-2">
          <Field label="Action">
            <Select value={form.kind} onChange={(v) => setForm({ ...form, kind: v })}
              options={[{ value: 'restart', label: 'Restart' }, { value: 'start', label: 'Start' }, { value: 'stop', label: 'Stop' }]} />
          </Field>
          <Field label="Cron (5-field)"><input className="input font-mono" value={form.cron} onChange={(e) => setForm({ ...form, cron: e.target.value })} /></Field>
          <button className="btn-brand" onClick={add}><Plus size={13} /> Add</button>
        </div>
      </div>
    </Card>
  )
}

function DeployTab({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const { org } = useAuth()
  const [form, setForm] = useState({ repo_url: bot.git_repo_url, branch: bot.git_branch || 'main', token: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [ok, setOk] = useState('')

  const deploy = async () => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      // POST deploy-git: {repo_url?, branch, token?} — repo_url required on
      // first deploy; the token is encrypted and never returned.
      const r = await api.post<{ status: string; repo_url: string; branch: string }>(
        `/v1/organizations/${org!.id}/bots/${bot.id}/deploy-git`, {
          repo_url: form.repo_url || undefined,
          branch: form.branch,
          token: form.token || undefined,
        })
      setOk(`${r.status} — ${r.repo_url} @ ${r.branch}; watch the console for build output`)
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="Deploy from git" subtitle="HTTPS clone on the node — tokens are encrypted, never stored in plaintext" />
      <ErrorNote message={err} />
      {ok && <div className="mx-4 rounded border border-brand/30 bg-brand/10 px-3 py-2 text-[12.5px] text-brand">{ok}</div>}
      <div className="grid gap-3 p-4 pt-2">
        <div className="rounded border border-line px-3 py-2 text-[12px]">
          <span className="text-muted">current: </span>
          <span className="font-mono text-ink">{bot.git_repo_url || 'no repository yet'}</span>
          {bot.git_branch && <span className="font-mono text-muted"> @ {bot.git_branch}</span>}
        </div>
        <Field label="Repository" hint="leave empty to redeploy the current repository">
          <input className="input" value={form.repo_url} onChange={(e) => setForm({ ...form, repo_url: e.target.value })} placeholder="https://github.com/you/bot" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Branch"><input className="input" value={form.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} /></Field>
          <Field label="Access token (private repos)"><input className="input" type="password" value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} placeholder="ghp_… (not stored)" /></Field>
        </div>
        <div className="rounded border border-line bg-line/30 px-3 py-2 text-[11.5px] text-muted">
          Secrets like DISCORD_TOKEN belong in the Environment tab — deploys never read or write env values.
        </div>
        <div className="flex justify-end">
          <button className="btn-brand" onClick={deploy} disabled={busy || !form.repo_url}>
            <GitBranch size={13} /> {busy ? 'Deploying…' : 'Deploy'}
          </button>
        </div>
      </div>
    </Card>
  )
}

function SettingsTab({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [form, setForm] = useState({
    startup_file: bot.startup_file,
    startup_command: bot.startup_command ?? '',
    build_command: bot.build_command ?? '',
    runtime_version: bot.runtime_version,
  })
  const [versions, setVersions] = useState<string[]>([bot.runtime_version])
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const [busy, setBusy] = useState(false)

  // Versions come from the runtime-offers endpoint so PATCH only ever
  // submits values the runtime validator accepts.
  useEffect(() => {
    if (!org) return
    api.get<{ runtimes: VersionOffer[] }>(`/v1/organizations/${org.id}/bots/runtime-offers`)
      .then((r) => {
        const o = (r.runtimes ?? []).find((x) => x.runtime === bot.runtime)
        if (o) setVersions(o.versions)
      })
      .catch(() => {})
  }, [org?.id, bot.runtime]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      // PATCH accepts exactly: startup_file, startup_command, build_command,
      // runtime_version (phase8_discord.go updateBotRequest). Applies at next
      // start/restart; restart_policy / max_restarts are create-time only.
      await api.patch(`/v1/organizations/${org!.id}/bots/${bot.id}`, {
        startup_file: form.startup_file,
        startup_command: form.startup_command,
        build_command: form.build_command,
        runtime_version: form.runtime_version,
      })
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
      title: `Delete ${bot.name}?`,
      message: 'The bot, its files and its encrypted environment are removed from the node. This cannot be undone.',
      confirmLabel: 'Delete bot',
    }))) return
    setErr('')
    try {
      await api.del(`/v1/organizations/${org!.id}/bots/${bot.id}`)
      navigate('/bots')
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader title="Startup & runtime" subtitle="changes apply at the next start/restart" />
        <ErrorNote message={err} />
        {ok && <div className="mx-4 rounded border border-brand/30 bg-brand/10 px-3 py-2 text-[12.5px] text-brand">{ok}</div>}
        <div className="grid gap-3 p-4 pt-2">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Runtime</div><div className="mt-0.5 text-[12.5px] text-ink">{bot.runtime}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Restart policy</div><div className="mt-0.5 text-[12.5px] text-ink">{bot.restart_policy}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Max restarts</div><div className="mt-0.5 text-[12.5px] text-ink">{bot.max_restarts ?? '—'}</div></div>
            <div><div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">Restarts</div><div className="mt-0.5 text-[12.5px] text-ink">{bot.restart_count}</div></div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Startup file" hint="entry point, e.g. index.js / main.py">
              <input className="input font-mono" value={form.startup_file} onChange={(e) => setForm({ ...form, startup_file: e.target.value })} placeholder="index.js" />
            </Field>
            <Field label="Runtime version">
              <Select value={form.runtime_version} onChange={(v) => setForm({ ...form, runtime_version: v })}
                options={versions.map((v) => ({ value: v, label: v }))} />
            </Field>
          </div>
          <Field label="Startup command" hint="overrides the startup file when set">
            <input className="input font-mono" value={form.startup_command} onChange={(e) => setForm({ ...form, startup_command: e.target.value })} placeholder="node index.js" />
          </Field>
          <Field label="Build command" hint="runs after each git deploy, e.g. npm install">
            <input className="input font-mono" value={form.build_command} onChange={(e) => setForm({ ...form, build_command: e.target.value })} placeholder="npm install" />
          </Field>
          <div className="flex justify-end">
            <button className="btn-brand" onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save changes'}</button>
          </div>
        </div>
      </Card>
      <Card>
        <CardHeader title="Danger zone" subtitle="deletion is queued on the node and cannot be undone" />
        <ErrorNote message={err} />
        <div className="flex items-center justify-between gap-3 p-4 pt-2">
          <div className="text-[12.5px] text-muted">Delete this bot, its files and its encrypted environment.</div>
          <button className="btn-danger" onClick={del}><Trash2 size={13} /> Delete bot</button>
        </div>
      </Card>
    </div>
  )
}
