import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Package, Plus, Trash2, RefreshCw, Loader2, Puzzle, ChevronDown, CheckCircle2, XCircle, Clock,
} from 'lucide-react'
import { api } from '@epicpanel/core'
import { Card, EmptyState, SkeletonRows, StatusBadge, PageTitle } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select } from '@epicpanel/forms'
import { pushToast } from '@epicpanel/ui'
import { adminview } from '../adminview'
import type { AdminServer } from '../adminview'

// ────────────────────────────────────────────────────── catalog definitions ──

const CATALOG: { type: string; label: string; versions: string[] }[] = [
  { type: 'php',           label: 'PHP (FPM + extensions)',   versions: ['8.1', '8.2', '8.3', '8.4', '8.5'] },
  { type: 'node',          label: 'Node.js (NodeSource)',     versions: ['20', '22', '24'] },
  { type: 'python',        label: 'Python (standalone)',      versions: ['3.12', '3.13'] },
  { type: 'go',            label: 'Go (official toolchain)',  versions: ['1.26', '1.27'] },
  { type: 'java',          label: 'Java (OpenJDK / Temurin)', versions: ['21', '25'] },
  { type: 'apache',        label: 'Apache HTTP Server',       versions: ['2.4'] },
  { type: 'openlitespeed', label: 'OpenLiteSpeed',            versions: ['1.8'] },
  { type: 'redis',         label: 'Redis (cache)',            versions: ['latest'] },
  { type: 'dbtools',       label: 'DB Tools (phpMyAdmin + Adminer + SSO)', versions: ['latest'] },
]

const DEFAULTS: Record<string, string> = {
  php: '8.3', node: '22', python: '3.13', go: '1.26',
  java: '21', apache: '2.4', openlitespeed: '1.8', redis: 'latest',
  dbtools: 'latest',
}

const ENGINE_LABELS: Record<string, string> = {
  php: 'PHP', node: 'Node.js', python: 'Python', go: 'Go',
  apache: 'Apache', openlitespeed: 'OpenLiteSpeed', java: 'Java',
  phpmyadmin: 'phpMyAdmin', adminer: 'Adminer', redis: 'Redis', dbtools: 'DB Tools',
}

interface Runtime {
  id: string
  server_id: string
  type: string
  version: string
  status: string
  error_message?: string
}

interface ActiveJob {
  id: string
  server_id: string
  type: string
  status: 'pending' | 'running' | 'success' | 'failed'
  progress: number
  progress_step?: string
  error?: string
  payload?: { type?: string; version?: string }
}

interface PhpExtension {
  id: string
  name: string
  status: string
  error_message?: string
}

interface ExtCatalogItem { name: string; label: string }

// ─────────────────────────────────────────────── job progress pill component ──

function JobPill({ job }: { job: ActiveJob }) {
  const running = job.status === 'running' || job.status === 'pending'
  const failed = job.status === 'failed'
  const done = job.status === 'success'
  const pct = done ? 100 : job.progress ?? 0

  return (
    <div className="mt-2.5 rounded-[10px] border border-line bg-surface-2 px-3 py-2.5">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          {running && <Loader2 size={11} className="animate-spin text-brand" />}
          {done   && <CheckCircle2 size={11} className="text-ok" />}
          {failed && <XCircle size={11} className="text-danger" />}
          {!running && !done && !failed && <Clock size={11} className="text-muted" />}
          <span className={`text-[11px] font-semibold ${failed ? 'text-danger' : done ? 'text-ok' : running ? 'text-brand' : 'text-muted'}`}>
            {failed
              ? (job.error ?? 'Failed')
              : done
              ? 'Installed successfully'
              : job.progress_step || (running ? 'Installing…' : 'Queued')}
          </span>
        </div>
        <span className={`text-[11px] font-bold tabular-nums ${failed ? 'text-danger' : done ? 'text-ok' : 'text-brand'}`}>
          {done ? '100%' : running ? `${pct}%` : ''}
        </span>
      </div>
      {/* Progress bar */}
      <div className="h-1.5 overflow-hidden rounded-full bg-app">
        <div
          className={`h-full rounded-full transition-all duration-500 ${failed ? 'bg-danger' : done ? 'bg-ok' : 'bg-brand'}`}
          style={{ width: `${failed ? 100 : Math.max(4, pct)}%` }}
        />
      </div>
    </div>
  )
}

// ──────────────────────────────────────────────────────────────── main page ──

export function SoftwarePage() {
  const [servers, setServers] = useState<AdminServer[] | null>(null)
  const [runtimes, setRuntimes] = useState<Runtime[]>([])
  const [jobs, setJobs] = useState<ActiveJob[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [mutating, setMutating] = useState('')
  const [show, setShow] = useState(false)
  const [form, setForm] = useState({ server_id: '', type: 'php', version: '8.3' })

  // PHP / OLS extensions modal
  const [extFor, setExtFor] = useState<Runtime | null>(null)
  const [exts, setExts] = useState<PhpExtension[] | null>(null)
  const [extCatalog, setExtCatalog] = useState<ExtCatalogItem[]>([])
  const [extBusy, setExtBusy] = useState('')
  const [lsphpVersion, setLsphpVersion] = useState('8.3')
  const [globalPHP, setGlobalPHP] = useState<string>('')
  const [globalPHPSaving, setGlobalPHPSaving] = useState(false)

  // ── data loading ──
  const loadRuntimes = useCallback(async (list: AdminServer[]) => {
    const all: Runtime[] = []
    await Promise.all(
      list.map(async (s) => {
        const r = await api
          .get<{ runtimes: Runtime[] }>(`/v1/organizations/${s.organization_id}/servers/${s.id}/runtimes`)
          .catch(() => ({ runtimes: [] as Runtime[] }))
        all.push(...(r.runtimes ?? []))
      }),
    )
    setRuntimes(all)
  }, [])

  const loadJobs = useCallback(async (list: AdminServer[]) => {
    const all: ActiveJob[] = []
    await Promise.all(
      list.map(async (s) => {
        const r = await api
          .get<{ jobs: ActiveJob[] }>(
            `/v1/organizations/${s.organization_id}/servers/${s.id}/jobs?types=install_runtime,remove_runtime,install_database_tools&limit=30`,
          )
          .catch(() => ({ jobs: [] as ActiveJob[] }))
        all.push(...(r.jobs ?? []).map((j) => ({ ...j, server_id: s.id })))
      }),
    )
    setJobs(all)
  }, [])

  const load = useCallback(async () => {
    try {
      const sv = await adminview.servers()
      const list = sv.servers ?? []
      setServers(list)
      await Promise.all([loadRuntimes(list), loadJobs(list)])
      const st = await api
        .get<{ global_php_version?: string }>('/v1/settings')
        .catch(() => ({ global_php_version: '' }) as any)
      setGlobalPHP(st.global_php_version ?? '')
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to load')
    }
  }, [loadRuntimes, loadJobs])

  useEffect(() => { void load() }, [load])

  // Auto-poll while any job is active
  const hasActiveJobs = useMemo(
    () => jobs.some((j) => j.status === 'running' || j.status === 'pending'),
    [jobs],
  )
  useEffect(() => {
    if (!hasActiveJobs) return
    const t = setInterval(async () => {
      if (servers && servers.length > 0) {
        await Promise.all([loadRuntimes(servers), loadJobs(servers)])
      }
    }, 2500)
    return () => clearInterval(t)
  }, [hasActiveJobs, servers, loadRuntimes, loadJobs])

  const eligibleServers = (servers ?? []).filter((s) => s.status === 'online')

  // Match a job to a runtime row (by type + version)
  const matchJob = (r: Runtime): ActiveJob | undefined =>
    jobs.find(
      (j) =>
        j.server_id === r.server_id &&
        (j.status === 'running' || j.status === 'pending' || j.status === 'failed') &&
        j.payload?.type === r.type &&
        (j.payload?.version === r.version || r.version === 'latest'),
    )

  // ── install ──
  const install = async () => {
    const srv = eligibleServers.find((s) => s.id === form.server_id)
    if (!srv) return
    setErr(''); setBusy(true)
    try {
      if (form.type === 'dbtools') {
        // DB Tools is a combined software package (phpMyAdmin + Adminer +
        // serving + SSO), not a runtime — it installs via /software/install.
        await api.post(`/v1/organizations/${srv.organization_id}/servers/${srv.id}/software/install`, {
          name: 'dbtools',
          version: 'latest',
        })
      } else {
        await api.post(`/v1/organizations/${srv.organization_id}/servers/${srv.id}/runtimes`, {
          type: form.type,
          version: form.version,
        })
      }
      pushToast('success', `${ENGINE_LABELS[form.type] ?? form.type} ${form.version} — install queued`)
      setShow(false)
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to queue install')
    } finally {
      setBusy(false)
    }
  }

  const retry = async (r: Runtime) => {
    const srv = eligibleServers.find((s) => s.id === r.server_id)
    if (!srv) return
    setMutating(r.id)
    try {
      await api.post(`/v1/organizations/${srv.organization_id}/servers/${r.server_id}/runtimes`, {
        type: r.type, version: r.version,
      })
      pushToast('success', `${ENGINE_LABELS[r.type] ?? r.type} ${r.version} — retry queued`)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Retry failed')
    } finally {
      setMutating('')
    }
  }

  const remove = async (r: Runtime) => {
    const srv = eligibleServers.find((s) => s.id === r.server_id)
    if (!srv) return
    setMutating(r.id)
    try {
      await api.del(`/v1/organizations/${srv.organization_id}/servers/${r.server_id}/runtimes/${r.id}`)
      pushToast('success', `${ENGINE_LABELS[r.type] ?? r.type} ${r.version} removed`)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Remove failed')
    } finally {
      setMutating('')
    }
  }

  // ── PHP extensions ──
  const openExtensions = async (r: Runtime) => {
    const srv = eligibleServers.find((s) => s.id === r.server_id)
    if (!srv) return
    setExtFor(r); setExts(null)
    try {
      const res = await api.get<{ extensions: PhpExtension[]; catalog: ExtCatalogItem[] }>(
        `/v1/organizations/${srv.organization_id}/servers/${r.server_id}/runtimes/${r.id}/extensions`,
      )
      setExts(res.extensions ?? [])
      setExtCatalog(res.catalog ?? [])
    } catch { setExts([]) }
  }

  const toggleExtension = async (name: string, cur: PhpExtension | undefined) => {
    const srv = extFor && eligibleServers.find((s) => s.id === extFor.server_id)
    if (!srv || !extFor) return
    const action = cur?.status === 'available' ? 'remove' : 'install'
    setExtBusy(name)
    try {
      await api.post(
        `/v1/organizations/${srv.organization_id}/servers/${extFor.server_id}/runtimes/${extFor.id}/extensions`,
        { name, action, php_version: extFor.type === 'openlitespeed' ? lsphpVersion : undefined },
      )
      setTimeout(() => { if (extFor) void openExtensions(extFor) }, 600)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Extension toggle failed')
    } finally {
      setExtBusy('')
    }
  }

  const saveGlobalPHP = async (version: string) => {
    setGlobalPHPSaving(true)
    try {
      const res = await api.patch<{ dbtools_migrations_queued?: number }>('/v1/settings/global-php', { version })
      setGlobalPHP(version)
      const n = res.dbtools_migrations_queued ?? 0
      pushToast('success', version
        ? `Global PHP set to ${version}${n > 0 ? ` — migrating DB Tools on ${n} server${n > 1 ? 's' : ''}…` : ''}`
        : 'Global PHP set to auto (highest installed)')
      await load() // pick up the auto-queued migration jobs
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to save global PHP')
    } finally {
      setGlobalPHPSaving(false)
    }
  }

  const installedPhpVersions = useMemo(
    () => Array.from(new Set(runtimes.filter((r) => r.type === 'php' && r.status === 'available').map((r) => r.version))).sort(),
    [runtimes],
  )

  // ── render ──
  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <PageTitle
          title="Software"
          subtitle={
            hasActiveJobs
              ? '⚡ Installation in progress — updating live every 2.5 s'
              : 'Runtimes, web servers and PHP extensions installed per node.'
          }
        />
        <button className="btn-primary" onClick={() => setShow(true)} disabled={eligibleServers.length === 0}>
          <Plus size={14} /> Install software
        </button>
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-2 rounded-xl border border-line bg-surface-2 px-4 py-3 text-[12px]">
        <span className="font-medium">Global PHP (panel tools — DB Tools):</span>
        <select
          className="rounded-lg border border-line bg-surface px-2 py-1"
          value={globalPHP}
          disabled={globalPHPSaving || installedPhpVersions.length === 0}
          onChange={(e) => void saveGlobalPHP(e.target.value)}
        >
          <option value="">Auto (highest installed)</option>
          {installedPhpVersions.map((v) => (
            <option key={v} value={v}>PHP {v}</option>
          ))}
        </select>
        <span className="opacity-60">Applies when DB Tools (re)installs. Extensions are per-version below.</span>
      </div>

      {err && (
        <div className="mb-4 rounded-xl border border-[#ffd0d7] bg-danger-soft px-4 py-3 text-[12px] text-danger">{err}</div>
      )}

      {servers === null ? (
        <Card><SkeletonRows rows={4} /></Card>
      ) : eligibleServers.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Package size={22} />}
            title="No online servers"
            subtitle="Enroll a server first — software is installed on each server by the agent."
          />
        </Card>
      ) : (
        <div className="space-y-5">
          {eligibleServers.map((s) => {
            const list = runtimes.filter((r) => r.server_id === s.id)
            // DB Tools is a combined package tracked by its jobs (not a
            // runtime row) — surface it as an installed entry so the list
            // reflects reality.
            const dbtoolsJobs = jobs.filter((j) => j.server_id === s.id)
            const dbtoolsDone = dbtoolsJobs.some((j) => j.status === 'success')
            const dbtoolsActive = dbtoolsJobs.some((j) => j.status === 'running' || j.status === 'pending')
            const dbtoolsRow: Runtime | null = dbtoolsDone || dbtoolsActive
              ? {
                  id: `dbtools-${s.id}`,
                  server_id: s.id,
                  type: 'dbtools',
                  version: globalPHP ? `php ${globalPHP}` : 'auto',
                  status: dbtoolsActive ? 'installing' : 'available',
                }
              : null
            const shownList = dbtoolsRow ? [...list, dbtoolsRow] : list
            // Pending installs that don't have a runtime row yet
            const pendingNew = jobs.filter(
              (j) =>
                j.server_id === s.id &&
                (j.status === 'running' || j.status === 'pending') &&
                !j.type.includes('database_tools') &&
                !list.some((r) => r.type === j.payload?.type && r.version === j.payload?.version),
            )

            return (
              <Card key={s.id} className="overflow-hidden !p-0">
                {/* Server header */}
                <div className="flex items-center gap-3 border-b border-line px-4 py-3.5">
                  <div className="grid h-[34px] w-[34px] place-items-center rounded-[10px] bg-brand-soft text-brand">
                    <Package size={16} strokeWidth={1.8} />
                  </div>
                  <div>
                    <div className="text-[13px] font-bold text-ink">{s.name}</div>
                    <div className="text-[10px] text-muted">{s.hostname || s.os_info}</div>
                  </div>
                  {hasActiveJobs && (
                    <span className="ml-2 flex items-center gap-1 rounded-full bg-brand/10 px-2.5 py-1 text-[10px] font-bold text-brand">
                      <Loader2 size={10} className="animate-spin" /> Installing…
                    </span>
                  )}
                  <button className="icon-btn ml-auto" onClick={() => void load()} title="Refresh">
                    <RefreshCw size={13} />
                  </button>
                </div>

                {list.length === 0 && pendingNew.length === 0 && !dbtoolsRow ? (
                  <div className="px-4 py-8 text-center text-[11.5px] text-muted">
                    No software installed yet. Click <strong>Install software</strong> to begin.
                  </div>
                ) : (
                  <div className="divide-y divide-line">
                    {/* Installed runtimes */}
                    {shownList.map((r) => {
                      const job = matchJob(r)
                      return (
                        <div key={r.id} className="px-4 py-3">
                          <div className="flex items-center gap-4">
                            {/* Abbrev badge */}
                            <div className="grid h-[32px] w-[32px] flex-none place-items-center rounded-[8px] bg-brand-soft text-[10px] font-extrabold uppercase text-brand">
                              {(ENGINE_LABELS[r.type] ?? r.type).slice(0, 2)}
                            </div>
                            <div className="min-w-0 flex-1">
                              <div className="flex flex-wrap items-center gap-2">
                                <span className="text-[14px] font-bold text-ink">
                                  {ENGINE_LABELS[r.type] ?? r.type} {r.version}
                                </span>
                                <StatusBadge status={r.status} />
                              </div>
                              {r.status === 'failed' && !job && r.error_message && (
                                <div className="mt-0.5 truncate text-[11px] text-danger" title={r.error_message}>
                                  {r.error_message}
                                </div>
                              )}
                            </div>
                            {/* Action buttons */}
                            <div className="flex items-center gap-1.5">
                              {(r.type === 'php' || r.type === 'openlitespeed') && r.status === 'available' && (
                                <button className="btn-ghost !py-1.5 !text-[12px]" onClick={() => void openExtensions(r)}>
                                  <Puzzle size={13} /> Extensions
                                </button>
                              )}
                              {r.status === 'failed' && !job && (
                                <button className="btn-ghost !py-1.5" onClick={() => void retry(r)} disabled={mutating === r.id}>
                                  {mutating === r.id ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
                                  {' '}Retry
                                </button>
                              )}
                              {r.status === 'available' && (
                                <button
                                  className="rounded-lg p-2 text-muted transition hover:bg-danger-soft hover:text-danger"
                                  onClick={() => void remove(r)}
                                  disabled={mutating === r.id}
                                  title="Remove"
                                >
                                  {mutating === r.id ? <Loader2 size={15} className="animate-spin" /> : <Trash2 size={15} />}
                                </button>
                              )}
                            </div>
                          </div>
                          {/* Live job progress bar for this runtime */}
                          {job && <JobPill job={job} />}
                        </div>
                      )
                    })}

                    {/* Jobs for not-yet-created runtimes (first queued install) */}
                    {pendingNew.map((j) => (
                      <div key={j.id} className="px-4 py-3">
                        <div className="flex items-center gap-4">
                          <div className="grid h-[32px] w-[32px] flex-none place-items-center rounded-[8px] bg-surface-2 text-[10px] font-extrabold uppercase text-muted">
                            {(ENGINE_LABELS[j.payload?.type ?? ''] ?? j.payload?.type ?? '??').slice(0, 2)}
                          </div>
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <span className="text-[14px] font-bold text-ink">
                                {ENGINE_LABELS[j.payload?.type ?? ''] ?? j.payload?.type} {j.payload?.version}
                              </span>
                              <span className="badge-neutral">queued</span>
                            </div>
                          </div>
                        </div>
                        <JobPill job={j} />
                      </div>
                    ))}
                  </div>
                )}
              </Card>
            )
          })}
        </div>
      )}

      {/* ── Install Modal ── */}
      <Modal open={show} onClose={() => setShow(false)} title="Install Software">
        <ErrorNote message={err} />
        <Field label="Server">
          <Select
            value={form.server_id}
            onChange={(v) => setForm({ ...form, server_id: v })}
            placeholder="Select a server…"
            options={eligibleServers.map((s) => ({ value: s.id, label: s.name }))}
          />
        </Field>
        <Field label="Software">
          <Select
            key="type"
            value={form.type}
            onChange={(v) => setForm({ ...form, type: v, version: DEFAULTS[v] ?? '' })}
            options={CATALOG.map((c) => ({ value: c.type, label: c.label }))}
          />
        </Field>
        <Field label="Version">
          <Select
            key={form.type}
            value={form.version}
            onChange={(v) => setForm({ ...form, version: v })}
            options={(CATALOG.find((c) => c.type === form.type)?.versions ?? []).map((v) => ({ value: v, label: v }))}
          />
        </Field>
        <button
          className="btn-brand w-full justify-center"
          onClick={install}
          disabled={busy || !form.server_id || !form.version}
        >
          {busy ? <><Loader2 size={14} className="animate-spin" /> Queuing…</> : 'Install'}
        </button>
      </Modal>

      {/* ── PHP / OLS Extensions Modal ── */}
      <Modal
        open={!!extFor}
        onClose={() => setExtFor(null)}
        title={(extFor?.type === 'openlitespeed' ? 'OpenLiteSpeed ' : `PHP ${extFor?.version ?? ''} `) + 'Extensions'}
        width="max-w-lg"
      >
        {extFor?.type === 'openlitespeed' && (
          <Field label="LSPHP version" hint="Extensions are per LSPHP runtime.">
            <Select
              value={lsphpVersion}
              onChange={setLsphpVersion}
              options={['8.2', '8.3', '8.4', '8.5'].map((v) => ({ value: v, label: 'LSPHP ' + v }))}
            />
          </Field>
        )}
        {exts === null ? (
          <SkeletonRows rows={5} height="h-9" />
        ) : exts.length === 0 && extCatalog.length === 0 ? (
          <EmptyState icon={<Puzzle size={20} />} title="No extensions found" subtitle="Catalog is empty or server offline." />
        ) : (
          <div className="max-h-[55vh] space-y-1.5 overflow-y-auto pr-1">
            {extCatalog.map((c) => {
              const cur = exts.find((e) => e.name === c.name)
              const st = cur?.status ?? 'not_installed'
              const busyMe = extBusy === c.name
              return (
                <div key={c.name} className="flex items-center gap-3 rounded-[9px] border border-line px-3.5 py-2.5">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13px] font-bold text-ink">{c.label}</span>
                      {st === 'available' && (
                        <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-bold text-emerald-700">INSTALLED</span>
                      )}
                      {st === 'installing' && <Loader2 size={12} className="animate-spin text-brand" />}
                      {st === 'failed' && (
                        <span className="rounded-full bg-danger-soft px-2 py-0.5 text-[10px] font-bold text-danger">{cur?.error_message ? 'FAILED' : 'ERROR'}</span>
                      )}
                    </div>
                  </div>
                  <button
                    className={st === 'available' ? 'btn-ghost !py-1.5 text-[12px]' : 'btn-primary !py-1.5 text-[12px]'}
                    onClick={() => void toggleExtension(c.name, cur)}
                    disabled={busyMe || st === 'installing' || st === 'removing'}
                  >
                    {busyMe || st === 'installing' || st === 'removing' ? '…' : st === 'available' ? 'Remove' : 'Install'}
                  </button>
                </div>
              )
            })}
          </div>
        )}
        <p className="mt-3 flex items-center gap-1.5 text-[11.5px] text-muted">
          <ChevronDown size={12} /> Extensions install from distro packages and reload PHP-FPM automatically.
        </p>
      </Modal>
    </div>
  )
}
