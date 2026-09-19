import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Boxes, ChevronRight, Layers, RefreshCw, Rocket, Puzzle, Trash2, CircleArrowUp,
  Loader2, X, CircleCheck, CircleX, CircleDashed, Terminal, Server as ServerIcon, Zap,
} from 'lucide-react'
import { api } from '@/lib/api'
import { subscribe } from '@epicpanel/core'
import { useAuth } from '@/context/AuthContext'
import { Card, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { pushToast } from '@/components/ref'
import { confirmAction } from '@/lib/confirm'
import { software, PHP_EXTENSIONS } from '@/lib/software'
import type { SoftwareItem, SoftwareTask, SoftwareMethod } from '@/lib/software'
import type { Server } from '@/lib/types'

/**
 * Software Installer (aaPanel-style) — catalog cards per server with
 * install / remove / upgrade + version picker, a one-click LNMP stack banner,
 * a per-task progress drawer with stepped status and live log tail (2s poll,
 * cursor deltas), and a PHP extensions sub-view. Wired to the contract in
 * docs/research/aapanel-software-installer.md §4.4; the control-plane side is
 * still on the roadmap, so every fetch degrades to an honest empty/error
 * state — this page never renders placeholder data.
 */

// ---------------------------------------------------------------- catalog UX

const CATEGORY_TABS: { key: string; label: string; match: (c: string, name: string) => boolean }[] = [
  { key: 'all', label: 'All', match: () => true },
  { key: 'webserver', label: 'Web servers', match: (c) => c === 'webserver' },
  { key: 'database', label: 'Databases', match: (c) => c === 'database' || c === 'cache' },
  { key: 'php', label: 'PHP', match: (_c, n) => n === 'php' },
  { key: 'runtime', label: 'Runtimes', match: (c, n) => c === 'language' && n !== 'php' },
  { key: 'tooling', label: 'Tools', match: (c) => c === 'tooling' || c === 'cms' },
]

const CATEGORY_LABEL: Record<string, string> = {
  webserver: 'Web server',
  database: 'Database',
  language: 'Runtime',
  cache: 'Cache',
  tooling: 'Tooling',
  cms: 'Application',
}

/** Pick the installable versions: everything available, minus versions that
 * are already installed (aaPanel hides what's in; upgrade covers the rest). */
function pickableVersions(item: SoftwareItem): string[] {
  const have = new Set((item.installed ?? []).map((i) => i.version))
  return (item.available_versions ?? []).map((v) => v.version).filter((v) => !have.has(v))
}

function bestAvailable(item: SoftwareItem): string | null {
  const list = pickableVersions(item)
  if (item.default_version && list.includes(item.default_version)) return item.default_version
  // Newest first: catalogs list versions oldest→newest.
  return list[list.length - 1] ?? null
}

function hasUpgrade(item: SoftwareItem): { from: string; to: string } | null {
  const avail = (item.available_versions ?? []).map((v) => v.version)
  const installed = (item.installed ?? []).map((i) => i.version)
  if (installed.length === 0 || avail.length === 0) return null
  const newest = avail[avail.length - 1]
  const current = installed[installed.length - 1]
  const num = (v: string) => parseFloat(v.replace(/[^0-9.].*$/, '')) || 0
  return num(newest) > num(current) ? { from: current, to: newest } : null
}

// ---------------------------------------------------------------- task drawer

const TASK_POLL_MS = 2000
const LOG_BATCH = 400

function StepIcon({ status }: { status: string }) {
  if (status === 'success' || status === 'done') return <CircleCheck size={14} className="text-ok" />
  if (status === 'failed') return <CircleX size={14} className="text-danger" />
  if (status === 'running') return <Loader2 size={14} className="animate-spin text-brand" />
  return <CircleDashed size={14} className="text-[#c3cbd8]" />
}

function TaskDrawer({ orgID, serverID, taskID, onClose, onSettled }: {
  orgID: string
  serverID: string
  taskID: string
  onClose: () => void
  onSettled: () => void
}) {
  const [task, setTask] = useState<SoftwareTask | null>(null)
  const [lines, setLines] = useState<string[]>([])
  const [logErr, setLogErr] = useState('')
  const cursorRef = useRef(0)
  const doneRef = useRef(false)
  const [logDone, setLogDone] = useState(false)
  const logBoxRef = useRef<HTMLDivElement>(null)

  const finished = task?.status === 'success' || task?.status === 'failed'

  useEffect(() => {
    let alive = true
    const unsub = subscribe((frame: any) => {
      if (!alive) return
      const t = frame?.type === 'job' || frame?.type === 'task' ? frame : null
      if (!t || t.id !== taskID) return
      if (t.data) {
        setTask(t.data)
        if (Array.isArray(t.data.log_lines)) setLines(t.data.log_lines.slice(-1500))
      } else {
        // minimal frame: fall back to lightweight fetch once
        void (async () => {
          try {
            const r = await software.task(orgID, serverID, taskID)
            if (!alive) return
            setTask(r)
          } catch {}
        })()
      }
      const st = t.data?.status ?? t.status
      if (st === 'success' || st === 'failed') {
        doneRef.current = true
        onSettled()
      }
    })
    return () => { alive = false; unsub?.() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgID, serverID, taskID])


  useEffect(() => {
    const el = logBoxRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  const pct = task?.percent ?? 0
  const status = task?.status ?? 'queued'

  return (
    <div className="fixed inset-0 z-[90]">
      <div className="absolute inset-0 bg-[#0b1220]/40 backdrop-blur-[2px]" onClick={onClose} aria-hidden="true" />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="Task progress"
        className="absolute inset-y-0 right-0 flex w-full max-w-[480px] flex-col border-l border-line bg-white shadow-modal"
        style={{ animation: 'fadeUp .22s ease both' }}
      >
        <div className="flex items-center gap-3 border-b border-line px-4 py-3.5">
          <div className="grid h-[32px] w-[32px] place-items-center rounded-[9px] bg-brand-soft text-brand">
            {finished ? (
              task?.status === 'success' ? <CircleCheck size={16} /> : <CircleX size={16} className="text-danger" />
            ) : (
              <Loader2 size={16} className="animate-spin" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] font-bold text-ink">
              {task?.name ? `${task.name}${task.version ? ` ${task.version}` : ''}` : 'Task'}
            </div>
            <div className="truncate text-[10.5px] text-muted">
              {task?.step || (status === 'queued' ? 'Queued — waiting for the agent…' : status)}
            </div>
          </div>
          <StatusBadge status={status === 'success' ? 'successful' : status} />
          <button className="icon-btn" onClick={onClose} aria-label="Close task panel">
            <X size={14} />
          </button>
        </div>

        <div className="border-b border-line px-4 py-3">
          <div className="mb-1 flex items-center justify-between text-[10.5px] font-bold text-sub">
            <span>{task?.type?.replace(/_/g, ' ') ?? 'software task'}</span>
            <span>{Math.round(pct)}%</span>
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-app">
            <div
              className={`h-full rounded-full transition-all duration-200 ${task?.status === 'failed' ? 'bg-danger' : 'bg-brand'}`}
              style={{ width: `${task?.status === 'success' ? 100 : Math.max(status === 'queued' ? 2 : 4, pct)}%` }}
            />
          </div>
          {(task?.steps ?? []).length > 0 && (
            <ol className="mt-3 space-y-1.5">
              {(task?.steps ?? []).map((s, i) => (
                <li key={`${s.name}-${i}`} className="flex items-center gap-2 text-[11.5px]">
                  <StepIcon status={s.status} />
                  <span className={`flex-1 truncate ${s.status === 'running' ? 'font-bold text-ink' : s.status === 'failed' ? 'text-danger' : 'text-sub'}`}>
                    {s.name}
                  </span>
                  {typeof s.percent === 'number' && s.status === 'running' && (
                    <span className="text-[10px] font-bold text-brand">{Math.round(s.percent)}%</span>
                  )}
                </li>
              ))}
            </ol>
          )}
          {task?.error && (
            <div className="mt-2 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11.5px] font-semibold text-danger">
              {task.error}
            </div>
          )}
        </div>

        <div className="flex min-h-0 flex-1 flex-col px-4 py-3">
          <div className="mb-1.5 flex items-center gap-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-muted">
            <Terminal size={11} /> Live log
            <span className="ml-auto flex items-center gap-1 font-mono text-[9px] normal-case tracking-normal">
              {logDone || finished ? 'end of log' : 'tailing…'}
            </span>
          </div>
          <div
            ref={logBoxRef}
            className="min-h-0 flex-1 overflow-y-auto rounded-[10px] border border-line bg-[#0d1420] p-3 font-mono text-[10.5px] leading-[1.6] text-[#c8e3ff]"
          >
            {lines.length === 0 ? (
              <span className="text-[#5b6c85]">
                {logErr ? `log stream: ${logErr}` : 'waiting for the agent to report…'}
              </span>
            ) : (
              lines.map((l, i) => (
                <div key={i} className="whitespace-pre-wrap break-all">{l}</div>
              ))
            )}
          </div>
          <p className="mt-2 text-[10px] text-muted">
            Polling status + log deltas every 2s from cursor {cursorRef.current.toLocaleString()}.
          </p>
        </div>
      </aside>
    </div>
  )
}

// ---------------------------------------------------------------- PHP extensions

function ExtensionsModal({ orgID, serverID, item, onClose, onQueued }: {
  orgID: string
  serverID: string
  item: SoftwareItem
  onClose: () => void
  onQueued: (taskID: string) => void
}) {
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [installedSet, setInstalledSet] = useState<Set<string>>(new Set())
  const versions = (item.installed ?? []).map((i) => i.version)
  const [version, setVersion] = useState(versions[versions.length - 1] ?? item.default_version ?? '')

  const toggle = async (name: string, action: 'install' | 'remove') => {
    setErr('')
    setBusy(name)
    try {
      const r = await software.extension(orgID, serverID, item.name, { version, extension: name, action })
      setInstalledSet((prev) => {
        const next = new Set(prev)
        if (action === 'install') next.add(name)
        else next.delete(name)
        return next
      })
      pushToast('success', `${name} ${action} queued`)
      if (r.task_id) onQueued(r.task_id)
    } catch (ex: any) {
      setErr(ex.message ?? `Failed to queue ${name} ${action}`)
    } finally {
      setBusy('')
    }
  }

  return (
    <Modal open onClose={onClose} title={`PHP ${version || ''} extensions`} subtitle={`${item.display_name} — install or remove modules for one version at a time.`} width="max-w-lg">
      <ErrorNote message={err} />
      {versions.length > 1 && (
        <Field label="PHP version">
          <Select value={version} onChange={setVersion} options={versions.map((v) => ({ value: v, label: `PHP ${v}` }))} />
        </Field>
      )}
      {versions.length === 0 && (
        <ErrorNote message="PHP is not installed on this server — install it first." />
      )}
      <div className="max-h-[55vh] space-y-1.5 overflow-y-auto pr-1">
        {PHP_EXTENSIONS.map((ext) => {
          const on = installedSet.has(ext.name)
          return (
            <div key={ext.name} className="flex items-center gap-3 rounded-[9px] border border-line px-3.5 py-2.5">
              <div className="min-w-0 flex-1">
                <span className="text-[13px] font-bold text-ink">{ext.label}</span>
                {ext.note && <span className="mt-0.5 block truncate text-[10.5px] text-muted">{ext.note}</span>}
                {on && <span className="mt-1 inline-block rounded-full bg-ok-soft px-2 py-[2px] text-[9px] font-bold text-ok">QUEUED INSTALL</span>}
              </div>
              <button
                className={on ? 'btn-ghost !py-1.5 !text-[11px]' : 'btn-primary !py-1.5 !text-[11px]'}
                onClick={() => void toggle(ext.name, on ? 'remove' : 'install')}
                disabled={!!busy || !version || versions.length === 0}
              >
                {busy === ext.name ? <Loader2 size={12} className="animate-spin" /> : on ? 'Remove' : 'Install'}
              </button>
            </div>
          )
        })}
      </div>
      <p className="mt-3 text-[11px] text-muted">
        Extensions are installed package-first (distro/PPA <code>php&#123;ver&#125;-&#123;ext&#125;</code>) with a PECL/source
        fallback, then PHP-FPM reloads automatically.
      </p>
    </Modal>
  )
}

// ---------------------------------------------------------------- install modal

function InstallModal({ item, onClose, onSubmit }: {
  item: SoftwareItem
  onClose: () => void
  onSubmit: (version: string, method: SoftwareMethod) => Promise<void>
}) {
  const versions = pickableVersions(item)
  const [version, setVersion] = useState(bestAvailable(item) ?? '')
  const av = (item.available_versions ?? []).find((v) => v.version === version)
  const methods: SoftwareMethod[] = ['auto', ...(av?.methods ?? [])]
  const [method, setMethod] = useState<SoftwareMethod>('auto')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  return (
    <Modal open onClose={onClose} title={`Install ${item.display_name}`} subtitle={item.description || 'Picked from the software catalog.'} width="max-w-md">
      <ErrorNote message={err} />
      <Field label="Version" hint={item.singleton ? 'This software is single-version per server.' : 'Multiple versions can coexist.'}>
        {versions.length > 0 ? (
          <Select value={version} onChange={(v) => setVersion(v)} options={versions.map((v) => ({ value: v, label: v }))} />
        ) : (
          <input className="input" value={version} onChange={(e) => setVersion(e.target.value)} placeholder={item.default_version ?? 'e.g. 8.3'} />
        )}
      </Field>
      <Field label="Install method" hint="Auto walks the catalog chain (package → repo → tarball → source).">
        <Select value={method} onChange={(v) => setMethod(v)} options={methods.map((m) => ({ value: m, label: m }))} />
      </Field>
      {(av?.eol || (item.conflicts ?? []).length > 0) && (
        <div className="mb-2 space-y-1 text-[11px]">
          {av?.eol && <div className="text-danger">⚠ This version is end-of-life.</div>}
          {(item.conflicts ?? []).map((c) => (
            <div key={c} className="text-warn">⚠ Conflicts with {c} (ports / service ownership).</div>
          ))}
        </div>
      )}
      <button
        className="btn-brand w-full justify-center"
        disabled={busy || !version}
        onClick={async () => {
          setBusy(true)
          try {
            await onSubmit(version, method)
            onClose()
          } catch (ex: any) {
            setErr(ex.message ?? 'Failed to queue install')
            setBusy(false)
          }
        }}
      >
        {busy ? 'Queuing…' : `Install ${item.display_name}${version ? ` ${version}` : ''}`}
      </button>
    </Modal>
  )
}

// ---------------------------------------------------------------- stack banner

function StackBanner({ busy, onRun }: { busy: boolean; onRun: () => void }) {
  return (
    <div
      className="mb-5 flex flex-wrap items-center gap-4 rounded-card border border-[#cddaff] bg-white p-4 shadow-card"
      style={{ background: 'linear-gradient(90deg, #eef4ff 0%, #ffffff 55%)' }}
    >
      <div className="grid h-[40px] w-[40px] flex-none place-items-center rounded-[11px] text-white"
        style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)' }}>
        <Zap size={19} strokeWidth={2} />
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-bold text-ink">One-click LNMP stack</div>
        <div className="text-[11.5px] text-muted">
          Linux · Nginx · MySQL · PHP in one ordered task — preflight, install and verify each step; aborts on the first failure.
        </div>
      </div>
      <button className="btn-brand" onClick={onRun} disabled={busy}>
        {busy ? <Loader2 size={14} className="animate-spin" /> : <Rocket size={14} />} {busy ? 'Queued…' : 'Install stack'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------- page

export function SoftwareInstallerPage() {
  const { org, user } = useAuth()
  const isAdmin = !!user?.is_platform_admin
  const [servers, setServers] = useState<Server[] | null>(null)
  const [serverID, setServerID] = useState('')
  const [items, setItems] = useState<SoftwareItem[] | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [category, setCategory] = useState('all')
  const [q, setQ] = useState('')
  const [installFor, setInstallFor] = useState<SoftwareItem | null>(null)
  const [extFor, setExtFor] = useState<SoftwareItem | null>(null)
  const [taskId, setTaskId] = useState('')
  const [busy, setBusy] = useState(false)

  const server = (servers ?? []).find((s) => s.id === serverID) ?? null

  const load = useCallback(async () => {
    if (!org) return
    setLoadErr('')
    try {
      const r = await software.list(org.id, serverID)
      setItems(r.data ?? [])
    } catch (ex: any) {
      setItems([])
      setLoadErr(ex.message ?? 'Software catalog is not available yet on this panel.')
    }
  }, [org?.id, serverID]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!org || !isAdmin) return
    api
      .get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`)
      .then((r) => {
        const online = (r.servers ?? []).filter((s) => s.status === 'online')
        setServers(online)
        if (online.length > 0) setServerID((cur) => cur || online[0].id)
      })
      .catch((ex) => {
        setServers([])
        setLoadErr(ex.message ?? 'Failed to load servers')
      })
  }, [org?.id, isAdmin])

  useEffect(() => {
    if (org && serverID) void load()
    else if (serverID === '') setItems(null)
  }, [org?.id, serverID]) // eslint-disable-line react-hooks/exhaustive-deps

  // While a task drawer is closed but the task ran, refresh the catalog once
  // it settles (load() is also called from onSettled).
  const runQueued = useCallback(async (label: string, fn: () => Promise<{ task_id: string }>) => {
    setBusy(true)
    try {
      const r = await fn()
      pushToast('success', `${label} — task queued`)
      if (r.task_id) setTaskId(r.task_id)
      return r
    } catch (ex: any) {
      pushToast('error', ex.message ?? `${label} failed`)
      throw ex
    } finally {
      setBusy(false)
    }
  }, [])

  const doRemove = async (item: SoftwareItem, version: string) => {
    if (!org || !serverID) return
    const ok = await confirmAction({
      title: `Remove ${item.display_name} ${version}`,
      message: item.singleton
        ? 'Sites may depend on this software. Removal stops + disables its service and never deletes data directories.'
        : 'Only this version is removed; symlinks are re-pointed to the highest remaining version.',
      confirmLabel: 'Remove',
    })
    if (!ok) return
    void runQueued(`${item.display_name} ${version} removal`, () =>
      software.remove(org.id, serverID, { name: item.name, version }),
    ).catch(() => undefined)
  }

  const shown = useMemo(() => {
    const tab = CATEGORY_TABS.find((t) => t.key === category) ?? CATEGORY_TABS[0]
    const needle = q.trim().toLowerCase()
    return (items ?? []).filter(
      (it) =>
        tab.match(it.category, it.name) &&
        (!needle || it.display_name.toLowerCase().includes(needle) || it.name.includes(needle)),
    )
  }, [items, category, q])

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <h1 className="section-title">Software Installer</h1>
        <Card className="mt-6">
          <EmptyState
            icon={<Boxes size={22} />}
            title="The installer is administrator-only"
            subtitle="Server software (web servers, databases, runtimes, PHP extensions) is managed by platform administrators."
          />
        </Card>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="section-title">Software Installer</h1>
          <p className="mt-[5px] max-w-[600px] text-[12px] text-muted">
            Catalog-driven installs on your servers — typed, stepwise and verified by the agent. Every action returns a
            task with per-step progress and a live log.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {servers !== null && servers.length > 0 && (
            <Select
              value={serverID}
              onChange={setServerID}
              options={servers.map((s) => ({ value: s.id, label: s.name }))}
            />
          )}
          <button className="icon-btn" onClick={() => void load()} title="Reload catalog" disabled={!serverID}>
            <RefreshCw size={13} />
          </button>
        </div>
      </div>

      {(servers ?? []).length === 0 && servers !== null ? (
        <Card>
          <EmptyState
            icon={<ServerIcon size={22} />}
            title="No online servers"
            subtitle="Enroll and connect a server first — the installer dispatches typed jobs through its agent."
          />
        </Card>
      ) : (
        <>
          <StackBanner
            busy={busy}
            onRun={() => {
              if (!org || !serverID) return
              void runQueued('LNMP stack', () =>
                software.stack(org.id, serverID, { stack: 'lnmp', components: { web: 'nginx', db: 'mysql', cache: 'redis' } }),
              ).catch(() => undefined)
            }}
          />

          {/* category tabs + search */}
          <div className="mb-4 flex flex-wrap items-center gap-2">
            {CATEGORY_TABS.map((t) => (
              <button
                key={t.key}
                onClick={() => setCategory(t.key)}
                className={`rounded-full px-3 py-1.5 text-[11px] font-bold transition ${
                  category === t.key ? 'bg-brand text-white' : 'border border-line bg-white text-sub hover:bg-surface-2'
                }`}
              >
                {t.label}
              </button>
            ))}
            <input
              className="input ml-auto !w-[220px] !py-1.5 !text-[11.5px]"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search catalog…"
            />
          </div>

          {loadErr && (
            <div className="mb-4 rounded-card border border-[#ffe8b1] bg-warn-soft px-4 py-3 text-[12px] font-semibold text-warn">
              {loadErr}
              <span className="ml-1 font-normal text-sub">
                The catalog surface (<code>GET …/software</code>) is a control-plane phase — it appears here once the
                backend ships. No placeholder data is shown.
              </span>
              <button className="btn-ghost !ml-3 !py-1 !text-[11px]" onClick={() => void load()}>
                <RefreshCw size={12} /> Retry
              </button>
            </div>
          )}

          {items === null ? (
            <Card><SkeletonRows rows={4} /></Card>
          ) : shown.length === 0 ? (
            <Card>
              <EmptyState
                icon={<Boxes size={22} />}
                title={loadErr ? 'Catalog unavailable' : 'Nothing in this category yet'}
                subtitle={
                  loadErr
                    ? 'Once the software catalog endpoints land, its entries render here as installable cards.'
                    : 'Try another category or clear the search.'
                }
              />
            </Card>
          ) : (
            <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
              {shown.map((it) => {
                const installed = it.installed ?? []
                const up = hasUpgrade(it)
                const pv = bestAvailable(it)
                return (
                  <Card key={it.name} className="flex flex-col !p-0">
                    <div className="flex items-start gap-3 px-4 pb-3 pt-4">
                      <div className="grid h-[34px] w-[34px] flex-none place-items-center rounded-[10px] bg-brand-soft text-brand">
                        {it.name === 'php' ? <Layers size={16} strokeWidth={1.8} /> : <Boxes size={16} strokeWidth={1.8} />}
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <strong className="truncate text-[13.5px] text-ink">{it.display_name}</strong>
                          <span className="badge-neutral">{CATEGORY_LABEL[it.category] ?? it.category}</span>
                        </div>
                        {it.description && (
                          <p className="mt-0.5 line-clamp-2 text-[11px] leading-[1.45] text-muted">{it.description}</p>
                        )}
                      </div>
                    </div>

                    <div className="flex-1 space-y-1.5 px-4 pb-3">
                      {installed.length === 0 ? (
                        <div className="text-[11px] font-semibold text-muted">Not installed</div>
                      ) : (
                        installed.map((i) => (
                          <div key={i.version} className="flex items-center gap-2 rounded-[9px] border border-line bg-surface-2 px-2.5 py-1.5">
                            <span className="text-[11.5px] font-bold text-ink">{i.version}</span>
                            {i.method && <span className="rounded bg-white px-1.5 text-[9px] font-bold uppercase tracking-wide text-muted">{i.method}</span>}
                            <StatusBadge status={i.healthy === false ? 'failed' : i.status ?? 'available'} />
                            <div className="ml-auto flex items-center gap-1">
                              {it.name === 'php' && (
                                <button className="icon-btn" title="Extensions" onClick={() => setExtFor(it)}>
                                  <Puzzle size={13} />
                                </button>
                              )}
                              <button className="icon-btn hover:border-[#ffd0d7] hover:bg-danger-soft hover:text-danger" title="Remove" onClick={() => void doRemove(it, i.version)}>
                                <Trash2 size={13} />
                              </button>
                            </div>
                          </div>
                        ))
                      )}
                      {installed.length > 0 && (it.available_versions ?? []).length > 0 && (
                        <div className="text-[10.5px] text-muted">
                          Available: {(it.available_versions ?? []).map((v) => v.version).join(', ')}
                        </div>
                      )}
                    </div>

                    <div className="flex items-center gap-2 border-t border-line px-4 py-3">
                      {installed.length > 0 && up && (
                        <button
                          className="btn-ghost !min-h-[32px] !text-[11px]"
                          title={`Upgrade ${up.from} → ${up.to}`}
                          onClick={() => {
                            if (!org) return
                            void runQueued(`${it.display_name} upgrade`, () =>
                              software.upgrade(org.id, serverID, { name: it.name, from: up.from, to: up.to }),
                            ).catch(() => undefined)
                          }}
                          disabled={busy}
                        >
                          <CircleArrowUp size={13} /> Upgrade {up.to}
                        </button>
                      )}
                      <button
                        className="btn-primary ml-auto !min-h-[32px] !text-[11px]"
                        disabled={busy || !pv}
                        onClick={() => setInstallFor(it)}
                      >
                        Install{pv ? ` ${pv}` : ''} <ChevronRight size={13} />
                      </button>
                    </div>
                  </Card>
                )
              })}
            </div>
          )}

          {server && (
            <p className="mt-4 text-[10.5px] text-muted">
              Catalog for <strong className="text-sub">{server.name}</strong> ({server.hostname || server.os_info}) —
              installs queue jobs the agent runs stepwise with pinned checksums and a final verify pass.
            </p>
          )}
        </>
      )}

      {installFor && org && serverID && (
        <InstallModal
          item={installFor}
          onClose={() => setInstallFor(null)}
          onSubmit={async (version, method) => {
            await runQueued(`${installFor.display_name} ${version} install`, () =>
              software.install(org.id, serverID, { name: installFor.name, version, method }),
            )
          }}
        />
      )}

      {extFor && org && serverID && (
        <ExtensionsModal
          orgID={org.id}
          serverID={serverID}
          item={extFor}
          onClose={() => setExtFor(null)}
          onQueued={(id) => setTaskId(id)}
        />
      )}

      {taskId && org && serverID && (
        <TaskDrawer
          orgID={org.id}
          serverID={serverID}
          taskID={taskId}
          onClose={() => setTaskId('')}
          onSettled={() => void load()}
        />
      )}
    </div>
  )
}
