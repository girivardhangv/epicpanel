import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Bot, Plus, RefreshCw, Play, Square, RotateCw, Trash2, ScrollText,
  FolderOpen, Clock, GitBranch, KeyRound, FileUp, Cpu,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { timeAgo } from '@/lib/types'

interface VersionOffer { runtime: string; label: string; versions: string[]; default: string }
interface Metrics { rss_mb?: number; cpu_percent?: number; freshness?: { state?: string; age_ms?: number } }

interface Bot {
  id: string
  name: string
  runtime: string
  runtime_version: string
  status: string
  desired_state: string
  startup_file: string
  restart_policy: string
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

const POLL_MS = 8000

export function BotsPage() {
  const { org } = useAuth()
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

  const act = async (b: Bot, action: string) => {
    setErr('')
    try {
      await api.post(`/v1/organizations/${org!.id}/bots/${b.id}/${action}`)
      await load()
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
        <div className="grid gap-3">
          {items.map((b) => (
            <Card key={b.id}>
              <div className="flex flex-wrap items-center gap-4 p-4">
                <div className="min-w-[220px] flex-1 cursor-pointer" onClick={() => window.location.assign(`/bots/${b.id}`)}>
                  <div className="flex items-center gap-2 text-[14px] font-semibold text-ink">
                    {b.name} <StatusBadge status={b.status} />
                  </div>
                  <div className="mt-0.5 text-[11.5px] text-muted">
                    {b.runtime} {b.runtime_version} · {b.startup_file || b.git_repo_url || 'no code yet'} · restarts {b.restart_count} · {timeAgo(b.created_at)}
                  </div>
                  {b.last_error && <div className="mt-1 text-[11px] text-danger">{b.last_error}</div>}
                </div>
                {b.metrics?.cpu_percent !== undefined && (
                  <div className="flex items-center gap-1.5 text-[11.5px] text-muted">
                    <Cpu size={14} /> {b.metrics.cpu_percent?.toFixed(1)}% · RSS {b.metrics.rss_mb?.toFixed(0)} MB
                  </div>
                )}
                <div className="flex items-center gap-1.5">
                  <button className="btn-ghost" onClick={() => act(b, 'start')}><Play size={13} /> Start</button>
                  <button className="btn-ghost" onClick={() => act(b, 'restart')}><RotateCw size={13} /> Restart</button>
                  <button className="btn-ghost" onClick={() => act(b, 'stop')}><Square size={13} /> Stop</button>
                  <button className="icon-btn" title="Open panel" onClick={() => window.location.assign(`/bots/${b.id}`)}><ScrollText size={15} /></button>
                  <button className="icon-btn" title="Delete" onClick={async () => {
                    if (!(await import('@/lib/confirm').then((m) => m.confirmAction({ title: `Delete ${b.name}?`, message: "The bot is removed from the node. This cannot be undone.", danger: true, confirmLabel: "Delete bot" })))) return
                    try { await api.del(`/v1/organizations/${org!.id}/bots/${b.id}`); await load() } catch (ex: any) { setErr(ex.message) }
                  }}><Trash2 size={15} /></button>
                </div>
              </div>
            </Card>
          ))}
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
  const [tab, setTab] = useState<'console' | 'env' | 'files' | 'schedules' | 'deploy'>('console')
  const [err, setErr] = useState('')

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

  const act = async (action: string) => {
    try { await api.post(`/v1/organizations/${org!.id}/bots/${bot!.id}/${action}`); await load() } catch (ex: any) { setErr(ex.message) }
  }

  return (
    <div className="p-6">
      <button className="btn-ghost mb-3" onClick={() => navigate('/bots')}>← All bots</button>
      <PageTitle
        title={<span className="flex items-center gap-2">{bot.name} <StatusBadge status={bot.status} /></span>}
        subtitle={`${bot.runtime} ${bot.runtime_version} · ${bot.startup_file || 'no startup file'} · restarts ${bot.restart_count}`}
        actions={
          <>
            <button className="btn-ghost" onClick={() => act('start')}><Play size={13} /> Start</button>
            <button className="btn-ghost" onClick={() => act('restart')}><RotateCw size={13} /> Restart</button>
            <button className="btn-ghost" onClick={() => act('stop')}><Square size={13} /> Stop</button>
          </>
        }
      />
      <ErrorNote message={err || bot.last_error} />
      <div className="mb-4 flex gap-1 border-b border-line">
        {(['console', 'env', 'files', 'schedules', 'deploy'] as const).map((t) => (
          <button key={t} onClick={() => setTab(t)}
            className={`-mb-px border-b-2 px-3 py-2 text-[12.5px] uppercase ${tab === t ? 'border-brand text-brand' : 'border-transparent text-muted hover:text-ink'}`}>
            {t}
          </button>
        ))}
      </div>
      {tab === 'console' && <ConsoleTab bot={bot} />}
      {tab === 'env' && <EnvTab bot={bot} onSaved={load} />}
      {tab === 'files' && <FilesTab bot={bot} onSaved={load} />}
      {tab === 'schedules' && <SchedulesTab bot={bot} />}
      {tab === 'deploy' && <DeployTab bot={bot} onSaved={load} />}
    </div>
  )
}

function ConsoleTab({ bot }: { bot: Bot }) {
  const { org } = useAuth()
  const [lines, setLines] = useState<{ seq: number; ts: string; text: string }[]>([])
  const [err, setErr] = useState('')
  const boxRef = useRef<HTMLDivElement>(null)

  const pull = useCallback(async () => {
    if (!org) return
    try {
      const r = await api.get<{ lines: { seq: number; ts: string; text: string }[] }>(
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

  useEffect(() => { boxRef.current?.scrollTo({ top: boxRef.current.scrollHeight }) }, [lines])

  return (
    <Card>
      <CardHeader title="Console" subtitle="live tail (env values are scrubbed before leaving the node)" />
      <div ref={boxRef} className="max-h-[420px] overflow-y-auto rounded bg-ink/95 p-3 font-mono text-[11.5px] leading-5 text-sky-200">
        {lines.length === 0 && <div className="text-muted">no output yet — start the bot</div>}
        {lines.map((l) => <div key={l.seq} className="whitespace-pre-wrap break-all">{l.text}</div>)}
      </div>
      <ErrorNote message={err} />
    </Card>
  )
}

function EnvTab({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const { org } = useAuth()
  const [pairs, setPairs] = useState<Record<string, string>>({})
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const save = async (vars: Record<string, string>) => {
    setBusy(true)
    setErr('')
    try {
      await api.put(`/v1/organizations/${org!.id}/bots/${bot.id}/env`, { vars })
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const keys = bot.env_keys ?? []
  return (
    <Card>
      <CardHeader title="Environment & secrets" subtitle="values are AES-GCM encrypted at rest and never shown again" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {keys.map((k) => (
          <div key={k} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <KeyRound size={14} className="text-brand" />
            <span className="w-52 font-mono">{k}</span>
            <span className="flex-1 font-mono text-muted">••••••••</span>
            <button className="icon-btn" title="Delete" onClick={async () => {
              try { await api.del(`/v1/organizations/${org!.id}/bots/${bot.id}/env/${encodeURIComponent(k)}`); onSaved() } catch (ex: any) { setErr(ex.message) }
            }}>×</button>
          </div>
        ))}
        {keys.length === 0 && <div className="py-3 text-[12.5px] text-muted">no env vars yet</div>}
        <div className="mt-2 flex items-end gap-2">
          <Field label="Key"><input className="input font-mono" value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="DISCORD_TOKEN" /></Field>
          <Field label="Value"><input className="input font-mono" type="password" value={newVal} onChange={(e) => setNewVal(e.target.value)} /></Field>
          <button className="btn-brand" disabled={busy || !newKey || !newVal}
            onClick={() => { void save({ [newKey]: newVal }); setNewKey(''); setNewVal('') }}>Set</button>
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
  const fileRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async (p: string) => {
    if (!org) return
    setErr('')
    try {
      const r = await api.get<{ entries: FileEntry[] }>(`/v1/organizations/${org.id}/bots/${bot.id}/files?path=${encodeURIComponent(p)}`)
      setEntries(r.entries ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id, bot.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load(path) }, [load, path])

  const upload = async (f: File) => {
    setErr('')
    try {
      const fd = new FormData()
      fd.append('file', f)
      fd.append('path', path)
      await fetch(`/v1/organizations/${org!.id}/bots/${bot.id}/files/upload`, {
        method: 'POST', body: fd, credentials: 'include',
      })
      await load(path)
      onSaved()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <Card>
      <CardHeader title="Files" subtitle="bot directory — upload code, then restart"
        right={<>
          <input ref={fileRef} type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
          <button className="btn-ghost" onClick={() => fileRef.current?.click()}><FileUp size={13} /> Upload</button>
          <button className="btn-ghost" onClick={() => load(path)}><RefreshCw size={13} /> Refresh</button>
        </>} />
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
          <div key={f.name} className="flex items-center gap-2 rounded px-2 py-1.5 text-[12.5px] text-ink">
            {f.is_dir ? <FolderOpen size={14} className="text-brand" /> : <FileUp size={14} className="text-muted" />}
            {f.is_dir ? (
              <button className="flex-1 text-left hover:text-brand" onClick={() => setPath(path ? `${path}/${f.name}` : f.name)}>{f.name}</button>
            ) : (
              <span className="flex-1">{f.name}</span>
            )}
            <span className="text-[11px] text-muted">{f.is_dir ? 'dir' : `${(f.size / 1024).toFixed(1)} KB`}</span>
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

  return (
    <Card>
      <CardHeader title="Schedules" subtitle="cron-driven restarts (custom commands not schedulable)" />
      <ErrorNote message={err} />
      <div className="grid gap-2 p-4 pt-0">
        {items.map((s) => (
          <div key={s.id} className="flex items-center gap-3 rounded border border-line px-3 py-2 text-[12.5px]">
            <Clock size={14} className="text-brand" />
            <span className="w-20 font-semibold capitalize">{s.kind}</span>
            <span className="flex-1 font-mono text-muted">{s.cron}</span>
            <span className="text-[11px] text-muted">next {timeAgo(s.next_run_at)}</span>
            <button className="icon-btn" onClick={async () => {
              try { await api.del(`/v1/organizations/${org!.id}/bots/${bot.id}/schedules/${s.id}`); await load() } catch (ex: any) { setErr(ex.message) }
            }}><Trash2 size={14} /></button>
          </div>
        ))}
        <div className="mt-1 flex items-end gap-2">
          <Field label="Action">
            <Select value={form.kind} onChange={(v) => setForm({ ...form, kind: v })}
              options={[{ value: 'restart', label: 'Restart' }, { value: 'start', label: 'Start' }, { value: 'stop', label: 'Stop' }]} />
          </Field>
          <Field label="Cron (5-field)"><input className="input font-mono" value={form.cron} onChange={(e) => setForm({ ...form, cron: e.target.value })} /></Field>
          <button className="btn-brand" onClick={async () => {
            try { await api.post(`/v1/organizations/${org!.id}/bots/${bot.id}/schedules`, form); await load() } catch (ex: any) { setErr(ex.message) }
          }}><Plus size={13} /> Add</button>
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
      await api.post(`/v1/organizations/${org!.id}/bots/${bot.id}/deploy-git`, {
        repo_url: form.repo_url || undefined,
        branch: form.branch,
        token: form.token || undefined,
      })
      setOk('Deploy queued — watch the console for build output')
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
        <Field label="Repository" hint={bot.git_repo_url ? `current: ${bot.git_repo_url}` : 'required on first deploy'}>
          <input className="input" value={form.repo_url} onChange={(e) => setForm({ ...form, repo_url: e.target.value })} placeholder="https://github.com/you/bot" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Branch"><input className="input" value={form.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} /></Field>
          <Field label="Access token (private repos)"><input className="input" type="password" value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} placeholder="ghp_… (not stored)" /></Field>
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
