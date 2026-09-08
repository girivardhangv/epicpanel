import { useCallback, useEffect, useMemo, useState } from 'react'
import { Package, Plus, Trash2, Server as ServerIcon, RefreshCw, Loader2, Puzzle, ChevronDown } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import type { Server } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface Runtime {
  id: string
  server_id: string
  type: string
  version: string
  status: string
  error_message?: string
}

interface SetupJob {
  id: string
  type: string
  status: string
  progress: number
  progress_step?: string
  error?: string
  payload?: any
}

interface PhpExtension {
  id: string
  name: string
  status: string
  error_message?: string
}

interface ExtCatalogItem {
  name: string
  label: string
}

const ENGINE_LABELS: Record<string, string> = {
  php: 'PHP',
  node: 'Node.js',
  python: 'Python',
  go: 'Go',
  apache: 'Apache',
  openlitespeed: 'OpenLiteSpeed',
}

const CATALOG: { type: string; label: string; versions: string[] }[] = [
  { type: 'php', label: 'PHP (FPM + common extensions)', versions: ['8.3', '8.4', '8.5', '8.2'] },
  { type: 'node', label: 'Node.js (NodeSource)', versions: ['22', '20', '24'] },
  { type: 'python', label: 'Python', versions: ['3.12', '3.11', '3.13'] },
  { type: 'go', label: 'Go (official toolchain)', versions: ['1.22', '1.23'] },
  { type: 'apache', label: 'Apache HTTP Server 2.4', versions: ['2.4'] },
  { type: 'openlitespeed', label: 'OpenLiteSpeed 1.8', versions: ['1.8'] },
]

const DEFAULTS: Record<string, string> = { php: '8.3', node: '22', python: '3.12', go: '1.22', apache: '2.4', openlitespeed: '1.8' }

// Live job progress pill for one install job.
function JobProgress({ job }: { job: SetupJob }) {
  const active = job.status === 'pending' || job.status === 'running'
  const failed = job.status === 'failed'
  return (
    <div className="mt-2">
      <div className="flex items-center justify-between text-[11.5px]">
        <span className={`font-semibold ${failed ? 'text-danger' : active ? 'text-brand' : 'text-ok'}`}>
          {active && <Loader2 size={11} className="mr-1 inline animate-spin" />}
          {failed ? job.error : job.progress_step || (job.status === 'success' ? 'Done' : 'Queued…')}
        </span>
        <span className={`font-bold ${failed ? 'text-danger' : active ? 'text-brand' : 'text-ok'}`}>
          {job.status === 'success' ? '100%' : active ? `${job.progress}%` : ''}
        </span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-app">
        <div
          className={`h-full rounded-full transition-all duration-500 ${failed ? 'bg-danger' : 'bg-brand'}`}
          style={{ width: `${job.status === 'success' ? 100 : failed ? 100 : Math.max(4, job.progress)}%` }}
        />
      </div>
    </div>
  )
}

export function SoftwarePage() {
  const { org, user } = useAuth()
  const [servers, setServers] = useState<Server[] | null>(null)
  const [runtimes, setRuntimes] = useState<Runtime[]>([])
  const [jobs, setJobs] = useState<SetupJob[]>([])
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ server_id: '', type: 'php', version: '8.3' })
  const [extFor, setExtFor] = useState<Runtime | null>(null)
  const [exts, setExts] = useState<PhpExtension[] | null>(null)
  const [extCatalog, setExtCatalog] = useState<ExtCatalogItem[]>([])
  const [extBusy, setExtBusy] = useState('')
  const [lsphpVersion, setLsphpVersion] = useState('8.3')
  const isAdmin = !!user?.is_platform_admin

  const load = useCallback(async () => {
    if (!org) return
    const sv = await api.get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`)
    const list = sv.servers ?? []
    setServers(list)
    const all: Runtime[] = []
    await Promise.all(
      list.map(async (s) => {
        const r = await api
          .get<{ runtimes: Runtime[] }>(`/v1/organizations/${org.id}/servers/${s.id}/runtimes`)
          .catch(() => ({ runtimes: [] as Runtime[] }))
        all.push(...(r.runtimes ?? []))
      }),
    )
    setRuntimes(all)
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const loadJobs = useCallback(async () => {
    if (!org || !(servers ?? []).length) return
    const all: SetupJob[] = []
    await Promise.all(
      (servers ?? []).map(async (s) => {
        const r = await api
          .get<{ jobs: SetupJob[] }>(`/v1/organizations/${org.id}/servers/${s.id}/jobs?types=install_runtime,remove_runtime,install_extension&limit=20`)
          .catch(() => ({ jobs: [] as SetupJob[] }))
        all.push(...(r.jobs ?? []))
      }),
    )
    setJobs(all)
  }, [org?.id, servers]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (isAdmin) void load()
  }, [isAdmin, load])

  // Poll while anything is installing/removing so statuses + progress update live.
  const busyNow = useMemo(
    () => runtimes.some((r) => r.status === 'installing' || r.status === 'removing') ||
      jobs.some((j) => j.status === 'running' || j.status === 'pending'),
    [runtimes, jobs],
  )
  useEffect(() => {
    if (!busyNow) return
    const t = setInterval(() => { void load(); void loadJobs() }, 2500)
    return () => clearInterval(t)
  }, [busyNow, load, loadJobs])

  const matchingJobs = (type: string, version: string) =>
    jobs
      .filter((j) => (j.payload?.type === type || (type === 'dbtools' && j.type === 'install_database_tools')) && (j.payload?.version === version || version === 'latest'))
      .filter((j) => j.status === 'running' || j.status === 'pending')
      .slice(0, 1)

  const install = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/servers/${form.server_id}/runtimes`, {
        type: form.type,
        version: form.version,
      })
      setShow(false)
      await load()
      await loadJobs()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to request installation')
    } finally {
      setBusy(false)
    }
  }

  const retry = async (r: Runtime) => {
    if (!org) return
    setErr('')
    try {
      await api.post(`/v1/organizations/${org.id}/servers/${r.server_id}/runtimes`, { type: r.type, version: r.version })
      await load()
      await loadJobs()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to retry installation')
    }
  }

  const remove = async (r: Runtime) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Remove Runtime', message: `Remove ${ENGINE_LABELS[r.type]} ${r.version} from this server?`, confirmLabel: 'Remove' }))) return
    try {
      await api.del(`/v1/organizations/${org.id}/servers/${r.server_id}/runtimes/${r.id}`)
      await load()
    } catch (ex: any) {
      alert(ex.message)
    }
  }

  const openExtensions = async (r: Runtime) => {
    setExtFor(r)
    setExts(null)
    if (!org) return
    try {
      const res = await api.get<{ extensions: PhpExtension[]; catalog: ExtCatalogItem[] }>(
        `/v1/organizations/${org.id}/servers/${r.server_id}/runtimes/${r.id}/extensions`)
      setExts(res.extensions ?? [])
      setExtCatalog(res.catalog ?? [])
    } catch (ex: any) {
      setExts([])
      setErr(ex.message)
    }
  }

  const toggleExtension = async (name: string, current: PhpExtension | undefined) => {
    if (!org || !extFor) return
    const action = current?.status === 'available' ? 'remove' : 'install'
    setExtBusy(name)
    try {
      await api.post(`/v1/organizations/${org.id}/servers/${extFor.server_id}/runtimes/${extFor.id}/extensions`, {
        name,
        action,
        php_version: extFor.type === 'openlitespeed' ? lsphpVersion : undefined,
      })
      setTimeout(() => { if (extFor) void openExtensions(extFor) }, 600)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setExtBusy('')
    }
  }

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <h1 className="text-[23px] font-bold tracking-[-.025em] text-ink">Software</h1>
        <Card className="mt-6">
          <EmptyState
            icon={<Package size={22} />}
            title="Software management is administrator-only"
            subtitle="Runtime versions (PHP, Node.js, Python, Go) and web servers (Apache, OpenLiteSpeed) are installed by platform administrators and shared across all sites."
          />
        </Card>
      </div>
    )
  }

  const eligibleServers = (servers ?? []).filter((s) => s.status === 'online')

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Software</h1>
          <p className="mt-[5px] max-w-[560px] text-[12px] text-muted">
            Managed runtimes, web servers and PHP extensions — installed once per server, shared safely by all sites.
          </p>
        </div>
        <button className="btn-primary" onClick={() => setShow(true)} disabled={eligibleServers.length === 0}>
          <Plus size={14} /> Install software
        </button>
      </div>

      <ErrorNote message={err} />

      {servers === null ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : eligibleServers.length === 0 ? (
        <Card>
          <EmptyState
            icon={<ServerIcon size={22} />}
            title="No online servers"
            subtitle="Connect and enroll a server first — software installs on your servers via the agent."
          />
        </Card>
      ) : (
        <div className="space-y-5">
          {eligibleServers.map((s) => {
            const list = runtimes.filter((r) => r.server_id === s.id)
            return (
              <Card key={s.id} className="overflow-hidden !p-0">
                <div className="mb-0 flex items-center gap-3 border-b border-line px-4 py-3.5">
                  <div className="grid h-[34px] w-[34px] place-items-center rounded-[10px] bg-brand-soft text-brand">
                    <ServerIcon size={16} strokeWidth={1.8} />
                  </div>
                  <div>
                    <div className="text-[12px] font-bold text-[#243047]">{s.name}</div>
                    <div className="text-[9.5px] text-muted">{s.hostname || s.os_info}</div>
                  </div>
                  <button className="icon-btn ml-auto" onClick={() => { void load(); void loadJobs() }} title="Refresh">
                    <RefreshCw size={13} />
                  </button>
                </div>
                {list.length === 0 ? (
                  <div className="px-4 py-6 text-center text-[11px] text-muted">
                    No runtimes installed yet. Install PHP, Node.js, Apache or OpenLiteSpeed to start hosting.
                  </div>
                ) : (
                  <div className="space-y-2">
                    {list.map((r) => {
                      const live = matchingJobs(r.type, r.version)[0] ?? matchingJobs(r.type, r.type === 'php' ? r.version : r.version)[0]
                      return (
                        <div key={r.id} className="border-b border-line px-4 py-3 last:border-b-0">
                          <div className="flex items-center gap-4">
                            <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-[10px] font-extrabold uppercase text-brand">
                              {ENGINE_LABELS[r.type]?.slice(0, 2) ?? r.type}
                            </div>
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-2.5">
                                <span className="text-[14.5px] font-bold text-ink">
                                  {ENGINE_LABELS[r.type] ?? r.type} {r.version}
                                </span>
                                <StatusBadge status={r.status} />
                              </div>
                              {r.status === 'failed' && r.error_message && (
                                <div className="mt-0.5 truncate text-xs text-danger" title={r.error_message}>{r.error_message}</div>
                              )}
                            </div>
                            {(r.type === 'php' || r.type === 'openlitespeed') && r.status === 'available' && (
                              <button className="btn-ghost !py-1.5 text-[12.5px]" onClick={() => void openExtensions(r)}>
                                <Puzzle size={13} /> Extensions
                              </button>
                            )}
                            {r.status === 'failed' && (
                              <button className="btn-ghost !py-1.5" onClick={() => retry(r)}>
                                <RefreshCw size={13} /> Retry
                              </button>
                            )}
                            {r.status === 'available' && (
                              <button className="rounded-lg p-2 text-muted transition hover:border-[#ffd0d7] hover:bg-danger-soft hover:text-danger" onClick={() => remove(r)} title="Remove">
                                <Trash2 size={15} />
                              </button>
                            )}
                          </div>
                          {live && <JobProgress job={live} />}
                        </div>
                      )
                    })}
                    {jobs.filter((j) => j.status === 'running' || j.status === 'pending').length === 0 && runtimes.some((r) => r.status === 'installing') && (
                      <div className="rounded border border-line px-4 py-2 text-[12px] text-muted">
                        <Loader2 size={12} className="mr-1.5 inline animate-spin" /> Working — the agent reports each stage here.
                      </div>
                    )}
                  </div>
                )}
              </Card>
            )
          })}
        </div>
      )}

      {/* Install modal */}
      <Modal open={show} onClose={() => setShow(false)} title="Install Software">
        <ErrorNote message={err} />
        <Field label="Server">
          <Select
            value={form.server_id}
            onChange={(v) => setForm({ ...form, server_id: v })}
            placeholder="Select a server..."
            options={eligibleServers.map((s) => ({ value: s.id, label: s.name }))}
          />
        </Field>
        <Field label="Software">
          <Select
            key={form.type}
            value={form.type}
            onChange={(v) => setForm({ ...form, type: v, version: DEFAULTS[v] ?? '' })}
            options={CATALOG.map((c) => ({ value: c.type, label: c.label }))}
          />
        </Field>
        <Field label="Version" hint="PHP/Python: major.minor (e.g. 8.3). Node/Go: major (e.g. 22). Missing PHP extensions are skipped gracefully.">
          <input className="input" value={form.version} onChange={(e) => setForm({ ...form, version: e.target.value })} placeholder="8.3" />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={install} disabled={busy || !form.server_id || !form.version}>
          {busy ? 'Requesting...' : 'Install'}
        </button>
      </Modal>

      {/* Extensions modal */}
      <Modal open={!!extFor} onClose={() => setExtFor(null)} title={(extFor?.type === 'openlitespeed' ? 'OpenLiteSpeed ' : `PHP ${extFor?.version ?? ''} `) + 'Extensions'} width="max-w-lg">
        <ErrorNote message={err} />
        {extFor?.type === 'openlitespeed' && (
          <Field label="LSPHP version" hint="OpenLiteSpeed extensions are per LSPHP runtime (installed from the LiteSpeed repo).">
            <Select
              value={lsphpVersion}
              onChange={setLsphpVersion}
              options={['8.2', '8.3', '8.4', '8.5'].map((v) => ({ value: v, label: 'LSPHP ' + v }))}
            />
          </Field>
        )}
        {exts === null ? (
          <SkeletonRows rows={5} height="h-9" />
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
                      <span className="text-[13.5px] font-bold text-ink">{c.label}</span>
                      {st === 'available' && <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-bold text-emerald-700">INSTALLED</span>}
                      {st === 'installing' && <Loader2 size={12} className="animate-spin text-brand" />}
                      {st === 'failed' && <span className="rounded-full bg-danger-soft px-2 py-0.5 text-[10px] font-bold text-danger" title={cur?.error_message}>FAILED</span>}
                    </div>
                    {st === 'failed' && cur?.error_message && (
                      <div className="mt-0.5 truncate text-[11.5px] text-danger">{cur.error_message}</div>
                    )}
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
          <ChevronDown size={12} /> Extensions install from your distro/PPA packages and reload PHP-FPM automatically.
        </p>
      </Modal>
    </div>
  )
}
