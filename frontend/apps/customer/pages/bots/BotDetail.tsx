import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ArrowLeft, KeyRound, Play, RotateCcw, RefreshCw, Save, Square, Terminal, Trash2, Clock, Upload, Zap } from 'lucide-react'
import { api, useAuth, useMetrics, timeAgo, fmtBytes } from '@epicpanel/core'
import type { FreshnessState, Job } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast } from '@epicpanel/ui'
import { FreshnessBadge } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, InfoNote, Select, FormRow, ConfirmDialog } from '@epicpanel/forms'
import type { BotInstance, BotMetrics, RuntimeOffer } from './BotsList'

interface ConsoleLine {
  seq: number
  ts: string
  text: string
}

interface BotSchedule {
  id: string
  bot_id: string
  kind: string
  cron: string
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

const TERMINAL_STATES = new Set(['stopped', 'crashed', 'failed'])
const CAN_READ = new Set(['installing', 'stopped', 'starting', 'running', 'stopping', 'crashed', 'failed'])

export function BotDetailPage() {
  const { org } = useAuth()
  const { bot_id: botId } = useParams()
  const { frames } = useMetrics()
  const [bot, setBot] = useState<BotInstance | null>(null)
  const [offers, setOffers] = useState<RuntimeOffer[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [tab, setTab] = useState<'console' | 'env' | 'files' | 'schedules' | 'settings'>('console')

  const load = useCallback(() => {
    if (!org || !botId) return
    api
      .get<BotInstance>(`/v1/organizations/${org.id}/bots/${botId}`)
      .then((b) => setBot(b))
      .catch((e) => setErr(e.message ?? 'Failed to load bot'))
  }, [org?.id, botId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    if (!org) return
    api
      .get<{ runtimes: RuntimeOffer[] }>(`/v1/organizations/${org.id}/bots/runtime-offers`)
      .then((r) => setOffers(r.runtimes ?? []))
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

  if (!org || !botId) return null
  if (!bot) {
    return (
      <div className="fade-up">
        <Link to="/bots" className="link mb-3 inline-flex items-center gap-1.5 text-[12px] text-muted">
          <ArrowLeft size={14} /> All bots
        </Link>
        <Card>
          <SkeletonRows rows={2} />
          <ErrorNote message={err} />
        </Card>
      </div>
    )
  }
  const base = `/v1/organizations/${org.id}/bots/${botId}`

  // Live metrics come from the WS stream (contract #4) — the app envelope is
  // keyed by bot id inside the server frame; the REST snapshot is the
  // fallback (same live store, no polling).
  const wsFrame = frames[bot.server_id]
  const wsApp = wsFrame?.apps?.find((a) => a.website_id === botId)
  const liveMetrics: BotMetrics | undefined = wsApp
    ? {
        kind: 'discord',
        status: wsApp.status,
        cpu_percent: wsApp.cpu_percent,
        memory_bytes: wsApp.memory_bytes,
        net_rx_bps: wsApp.net_rx_bps,
        net_tx_bps: wsApp.net_tx_bps,
        disk_used_mb: wsApp.disk_used_mb,
        uptime_s: wsApp.uptime_s,
        restart_count: wsApp.restart_count,
        freshness: wsFrame.freshness,
      }
    : bot.metrics

  return (
    <div className="fade-up">
      <Link to="/bots" className="link mb-3 inline-flex items-center gap-1.5 text-[12px] text-muted">
        <ArrowLeft size={14} /> All bots
      </Link>
      <>
        <PageTitle
            title={bot.name}
            subtitle={`${bot.runtime} ${bot.runtime_version} · ${bot.desired_state === 'running' ? 'wants to run' : 'wants to be stopped'} · restarts ${liveMetrics?.restart_count ?? bot.restart_count}`}
            actions={
              <>
                <button className="btn-ghost" onClick={load} aria-label="Refresh">
                  <RefreshCw size={15} /> Refresh
                </button>
                {bot.status !== 'running' && bot.status !== 'starting' && (
                  <button className="btn-brand" disabled={busy !== ''} onClick={() => action('Start', () => api.post(`${base}/start`))}>
                    <Play size={15} /> Start
                  </button>
                )}
                {bot.status === 'running' && (
                  <button className="btn-soft" disabled={busy !== ''} onClick={() => action('Restart', () => api.post(`${base}/restart`))}>
                    <RotateCcw size={15} /> Restart
                  </button>
                )}
                {(CAN_READ.has(bot.status) && bot.status !== 'stopped') && (
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

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-4">
            <Card>
              <CardHeader title="Status" />
              <div className="flex flex-col gap-2 px-4 pb-4">
                <StatusBadge status={bot.status} />
                <span className="text-[11px] text-muted">unit: {bot.unit_state || '—'}</span>
                {bot.last_error && <InfoNote message={bot.last_error} tone="warn" />}
                {(() => {
                  const f = liveMetrics?.freshness as { state?: string; age_ms?: number } | undefined
                  const state: FreshnessState = f?.state === 'LIVE' ? 'LIVE' : f?.state === 'STALE' ? 'STALE' : 'OFFLINE'
                  return <FreshnessBadge state={state} ageMs={f?.age_ms ?? 0} label="Metrics" />
                })()}
              </div>
            </Card>
            <Card>
              <CardHeader title="Live CPU" />
              <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">
                {liveMetrics?.cpu_percent != null ? `${liveMetrics.cpu_percent.toFixed(1)}%` : '—'}
              </div>
            </Card>
            <Card>
              <CardHeader title="Live memory" />
              <div className="px-4 pb-4 text-[22px] font-bold tracking-[-.02em] text-ink">
                {liveMetrics?.memory_bytes ? fmtBytes(liveMetrics.memory_bytes) : '—'}
              </div>
            </Card>
            <Card>
              <CardHeader title="Uptime / network" />
              <div className="px-4 pb-4 text-[13px] text-sub">
                {liveMetrics?.uptime_s ? formatUptime(liveMetrics.uptime_s) : '—'}
                <span className="block text-[11px] text-muted">
                  rx {liveMetrics?.net_rx_bps ? fmtBytes(liveMetrics.net_rx_bps) + '/s' : '—'} · tx {liveMetrics?.net_tx_bps ? fmtBytes(liveMetrics.net_tx_bps) + '/s' : '—'}
                </span>
              </div>
            </Card>
          </div>

          <div className="toolbar mt-5 gap-1">
            {(['console', 'env', 'files', 'schedules', 'settings'] as const).map((t) => (
              <button key={t} className={`btn-ghost capitalize ${tab === t ? 'bg-surface-2 text-ink' : 'text-muted'}`} onClick={() => setTab(t)}>
                {t}
              </button>
            ))}
          </div>

          {tab === 'console' && <ConsolePanel base={base} />}
          {tab === 'env' && <EnvPanel base={base} bot={bot} reload={load} />}
          {tab === 'files' && <FilesPanel base={base} />}
          {tab === 'schedules' && <SchedulesPanel base={base} />}
          {tab === 'settings' && <SettingsPanel base={base} bot={bot} offers={offers} reload={load} />}
        </>
      </div>
    )
}

function formatUptime(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

/** Console: REST snapshot + WS live tail. INPUT IS NOT SUPPORTED — there is
 * no command channel (no shell, no docker); lifecycle is API-only. */
function ConsolePanel({ base }: { base: string }) {
  const [lines, setLines] = useState<ConsoleLine[] | null>(null)
  const [live, setLive] = useState(false)
  const [note, setNote] = useState('')
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
    // Live tail over the bot console WS (read-only stream).
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
          if (msg?.type === 'bot_console') {
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

  return (
    <Card>
      <CardHeader
        title="Console"
        subtitle="stdout/stderr tail from the running bot"
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
          <span className="text-muted">no output yet — start the bot to see logs</span>
        ) : (
          lines.map((l) => (
            <div key={l.seq} className="whitespace-pre-wrap break-all">
              <span className="mr-2 select-none text-[10px] text-muted">{l.ts.slice(11, 19)}</span>
              {l.text}
            </div>
          ))
        )}
      </div>
      <div className="flex items-center justify-between border-t border-line px-4 py-2.5">
        <span className="text-[10.5px] text-muted">{note}</span>
        <button className="btn-ghost" onClick={snapshot}>
          <RefreshCw size={13} /> Load newer lines
        </button>
      </div>
      <div className="border-t border-line px-4 py-2.5">
        <InfoNote message="This console is read-only: bot input is not supported (no shell, no docker). Use the Start/Stop/Restart controls to manage the process." />
      </div>
    </Card>
  )
}

/** Env + secrets: write-only. The API returns keys only — values can be set
 * but never read back (they are encrypted at rest). */
function EnvPanel({ base, bot, reload }: { base: string; bot: BotInstance; reload: () => void }) {
  const [pairs, setPairs] = useState<{ key: string; value: string }[]>([])
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    setPairs((bot.env_keys ?? []).map((k) => ({ key: k, value: '' })))
  }, [bot.env_keys])

  const save = async () => {
    setErr('')
    setSaving(true)
    try {
      const vars: Record<string, string> = {}
      for (const p of pairs) {
        if (p.key.trim() === '') continue
        vars[p.key.trim()] = p.value
      }
      await api.put(`${base}/env`, { vars })
      pushToast('success', 'Environment saved (applies at next start/restart)')
      reload()
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  const removeKey = async (key: string) => {
    try {
      await api.del(`${base}/env/${encodeURIComponent(key)}`)
      pushToast('success', `Removed ${key}`)
      reload()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Remove failed')
    }
  }

  return (
    <Card>
      <CardHeader title="Environment & Secrets" subtitle="Values are write-only and encrypted at rest — they are never returned by the API or shown in logs" />
      <div className="px-4 pb-4">
        <ErrorNote message={err} />
        {pairs.length === 0 && <InfoNote message="No environment variables set yet." />}
        <div className="flex flex-col gap-2">
          {pairs.map((p, i) => (
            <div key={i} className="flex items-center gap-2">
              <input
                className="input font-mono"
                style={{ maxWidth: 220 }}
                value={p.key}
                onChange={(e) => setPairs(pairs.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}
                placeholder="DISCORD_TOKEN"
              />
              <input
                className="input flex-1 font-mono"
                type="password"
                value={p.value}
                onChange={(e) => setPairs(pairs.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                placeholder={p.value === '' && (bot.env_keys ?? []).includes(p.key) ? 'stored — type to replace' : 'value'}
              />
              <button className="icon-btn" aria-label={`Remove ${p.key || 'variable'}`} onClick={() => removeKey(p.key)}>
                <Trash2 size={14} />
              </button>
            </div>
          ))}
        </div>
        <div className="mt-3 flex gap-2">
          <button className="btn-ghost" onClick={() => setPairs([...pairs, { key: '', value: '' }])}>
            <KeyRound size={14} /> Add variable
          </button>
          <button className="btn-brand" onClick={save} disabled={saving}>
            <Save size={14} /> {saving ? 'Saving…' : 'Save environment'}
          </button>
        </div>
      </div>
    </Card>
  )
}

/** Files: upload into the bot tree + list. Content download is not offered
 * for bots (secrets stay on the node); deployment is git-first. */
function FilesPanel({ base }: { base: string }) {
  const [path, setPath] = useState('')
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [uploadPath, setUploadPath] = useState('index.js')
  const [uploadText, setUploadText] = useState('')
  const [busy, setBusy] = useState(false)

  const list = useCallback(async () => {
    try {
      const r = await api.get<{ entries?: FileEntry[] }>(`${base}/files?path=${encodeURIComponent(path)}`)
      // Listing is a job; the API may return entries inline when quick.
      setEntries((r as any).entries ?? null)
      if (!('entries' in r) || !Array.isArray((r as any).entries)) {
        pushToast('success', 'Listing queued — press Refresh in a moment')
      }
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
        <CardHeader title="Files" subtitle={`bot tree: /srv/epicpanel/bots/${''}`} right={<button className="btn-ghost" onClick={list}><RefreshCw size={13} /> Refresh</button>} />
        {entries === null ? (
          <SkeletonRows rows={2} />
        ) : entries.length === 0 ? (
          <EmptyState title="Empty (or listing queued)" subtitle="Upload files or deploy from git." />
        ) : (
          <div className="px-4 pb-4">
            {entries.map((f) => (
              <div key={f.name} className="flex items-center justify-between border-b border-line/60 py-2 text-[12.5px] last:border-0">
                <span className="text-sub">{f.is_dir ? '[dir] ' : ''}{f.name}</span>
                <span className="text-[10.5px] text-muted">{f.is_dir ? '' : fmtBytes(f.size)} · {timeAgo(f.mod_time)}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
      <Card>
        <CardHeader title="Upload file" subtitle="Small text files (config, entrypoint). Use git for full deployments." />
        <div className="px-4 pb-4">
          <Field label="Path" hint="relative to the bot root">
            <input className="input font-mono" value={uploadPath} onChange={(e) => setUploadPath(e.target.value)} placeholder="index.js" />
          </Field>
          <Field label="Content">
            <textarea className="input min-h-[160px] font-mono text-[12px]" value={uploadText} onChange={(e) => setUploadText(e.target.value)} placeholder="console.log('hello')" />
          </Field>
          <button className="btn-brand mt-2" onClick={upload} disabled={busy || !uploadPath}>
            <Upload size={14} /> {busy ? 'Uploading…' : 'Upload'}
          </button>
        </div>
      </Card>
    </div>
  )
}

/** Schedules: lifecycle only (restart/start/stop). Custom commands are
 * intentionally unsupported — that would be shell access by another name. */
function SchedulesPanel({ base }: { base: string }) {
  const [list, setList] = useState<BotSchedule[] | null>(null)
  const [kind, setKind] = useState('restart')
  const [cron, setCron] = useState('0 */6 * * *')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirm, setConfirm] = useState<BotSchedule | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const load = useCallback(() => {
    api
      .get<{ schedules: BotSchedule[] }>(`${base}/schedules`)
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
      await api.post(`${base}/schedules`, { kind, cron })
      pushToast('success', 'Schedule created')
      setCron('0 */6 * * *')
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
      <CardHeader title="Scheduled tasks" subtitle="Restart, start or stop on a cron schedule" />
      <div className="px-4 pb-4">
        <ErrorNote message={err} />
        <FormRow cols={3}>
          <Field label="Action">
            <Select value={kind} onChange={setKind} options={[
              { value: 'restart', label: 'Restart' },
              { value: 'start', label: 'Start' },
              { value: 'stop', label: 'Stop' },
            ]} />
          </Field>
          <Field label="Cron (5 fields)" hint="minute hour dom month dow">
            <input className="input font-mono" value={cron} onChange={(e) => setCron(e.target.value)} placeholder="0 */6 * * *" />
          </Field>
          <div className="flex items-end">
            <button className="btn-brand" onClick={create} disabled={busy || !cron}>
              <Clock size={14} /> Add schedule
            </button>
          </div>
        </FormRow>
        {list === null ? (
          <SkeletonRows rows={1} />
        ) : list.length === 0 ? (
          <InfoNote message="No schedules yet." />
        ) : (
          <div className="mt-2">
            {list.map((s) => (
              <div key={s.id} className="flex items-center justify-between border-b border-line/60 py-2 text-[12.5px] last:border-0">
                <span>
                  <span className="font-semibold capitalize text-ink">{s.kind}</span>
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

/** Settings: startup command, runtime version, restart policy, git deploy. */
function SettingsPanel({ base, bot, offers, reload }: { base: string; bot: BotInstance; offers: RuntimeOffer[]; reload: () => void }) {
  const [startupFile, setStartupFile] = useState(bot.startup_file)
  const [startupCommand, setStartupCommand] = useState(bot.startup_command)
  const [version, setVersion] = useState(bot.runtime_version)
  const [repo, setRepo] = useState(bot.git_repo_url)
  const [branch, setBranch] = useState(bot.git_branch)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const offer = offers.find((o) => o.runtime === bot.runtime)
  const versionOptions = (offer?.versions ?? []).map((v) => ({ value: v, label: v }))

  const saveSettings = async () => {
    setErr('')
    setBusy('save')
    try {
      await api.patch(base, { startup_file: startupFile, startup_command: startupCommand, runtime_version: version })
      pushToast('success', 'Saved — applies at next start/restart')
      reload()
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy('')
    }
  }

  const deploy = async () => {
    setErr('')
    setBusy('deploy')
    try {
      const body: Record<string, unknown> = {}
      if (repo) body.repo_url = repo
      if (branch) body.branch = branch
      if (token) body.token = token
      await api.post(`${base}/deploy-git`, body)
      pushToast('success', 'Git deployment queued')
      setToken('')
      reload()
    } catch (ex: any) {
      setErr(ex.message ?? 'Deploy failed')
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader title="Startup & runtime" subtitle="How the bot boots and which runtime version it uses" />
        <div className="px-4 pb-4">
          <ErrorNote message={err} />
          <Field label="Startup file" hint="entrypoint relative to the bot root">
            <input className="input font-mono" value={startupFile} onChange={(e) => setStartupFile(e.target.value)} placeholder="index.js" />
          </Field>
          <Field label="Startup command (optional)" hint="overrides the file, e.g. node dist/main.js">
            <input className="input font-mono" value={startupCommand} onChange={(e) => setStartupCommand(e.target.value)} placeholder="" />
          </Field>
          <Field label="Runtime version">
            <Select value={version} onChange={setVersion} options={versionOptions} placeholder={`current (${bot.runtime_version})`} />
          </Field>
          <Field label="Restart policy" hint="crash recovery is automatic while restarts remain in the episode budget">
            <div className="text-[12px] text-sub">
              {bot.restart_policy} · max {bot.max_restarts} per episode · lifetime restarts {bot.restart_count}
            </div>
          </Field>
          <button className="btn-brand mt-2" onClick={saveSettings} disabled={busy === 'save'}>
            <Save size={14} /> {busy === 'save' ? 'Saving…' : 'Save settings'}
          </button>
        </div>
      </Card>
      <Card>
        <CardHeader title="Git deployment" subtitle="Pull + dependency install on the node (timeout-bounded)" />
        <div className="px-4 pb-4">
          <Field label="Repository">
            <input className="input font-mono" value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/you/bot" />
          </Field>
          <FormRow cols={2}>
            <Field label="Branch">
              <input className="input font-mono" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
            </Field>
            <Field label="Token (optional)" hint="private repos; stored encrypted, never shown">
              <input className="input" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder="" />
            </Field>
          </FormRow>
          {bot.git_repo_url && (
            <InfoNote message={`Currently deployed from ${bot.git_repo_url} (${bot.git_branch || 'default branch'})`} />
          )}
          <button className="btn-brand mt-2" onClick={deploy} disabled={busy === 'deploy' || (!repo && !bot.git_repo_url)}>
            <Terminal size={14} /> {busy === 'deploy' ? 'Deploying…' : 'Deploy from git'}
          </button>
        </div>
      </Card>
    </div>
  )
}

// keep the Job import used (jobs surface is rendered via settings/console feeds)
export type { Job as BotJob }
