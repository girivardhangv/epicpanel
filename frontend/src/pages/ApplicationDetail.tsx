import { useCallback, useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import {
  ArrowLeft, ArrowRight, Play, Square, RotateCw, RefreshCw, Terminal as TerminalIcon,
  Folder, Clock, KeyRound, History, Lock, Globe, GitBranch,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { AppWizardModal } from '@/components/AppWizardModal'
import type { Website } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface App {
  id: string
  website_id: string
  startup_command: string
  build_command: string
  startup_file: string
  internal_port: number
  env_vars: Record<string, string>
  health: string
  created_at: string
}

export function ApplicationDetailPage() {
  const { org } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const [site, setSite] = useState<Website | null>(null)
  const [app, setApp] = useState<App | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [showWizard, setShowWizard] = useState(false)
  const [err, setErr] = useState('')
  const [logs, setLogs] = useState<string | null>(null)

  const base = `/v1/organizations/${org?.id}/websites/${websiteId}`

  const load = useCallback(async () => {
    if (!org) return
    try {
      const ws = await api.get<Website>(base)
      setSite(ws)
      const appRes = await api.get<App | null>(`${base}/application`).catch(() => null)
      setApp(appRes)
    } catch {
      setSite(null)
    } finally {
      setLoading(false)
    }
  }, [org?.id, base]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { void load() }, [load])

  const action = async (verb: 'start' | 'stop' | 'restart') => {
    setBusy(true)
    setErr('')
    try {
      await api.post(`${base}/application/${verb}`)
      setTimeout(() => void load(), 2000)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const fetchLogs = async () => {
    setBusy(true)
    try {
      await api.get(`${base}/application/logs?lines=200`)
      setTimeout(() => void load(), 2000)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const removeApp = async () => {
    if (!(await confirmAction({ title: 'Application', message: 'Remove the application process? Files are kept.', confirmLabel: 'Remove' }))) return
    setBusy(true)
    try {
      await api.post(`${base}/application/stop`)
      await api.del(`${base}/application`)
      setApp(null)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  if (loading) {
    return <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6"><SkeletonRows rows={4} /></div>
  }

  const runtimeLabel = site?.runtime === 'node' ? 'Node.js' : site?.runtime === 'python' ? 'Python' : site?.runtime === 'go' ? 'Go' : (site?.runtime ?? '')

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      {/* Header */}
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div className="flex items-center gap-3">
          <Link to={`/sites/${websiteId}`} className="icon-btn !h-[34px] !w-[34px]" title="Back to site">
            <ArrowLeft size={15} />
          </Link>
          <div>
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="text-[23px] font-bold tracking-[-.025em] text-ink">{site?.primary_domain || site?.name || '...'}</h1>
              {site && <StatusBadge status={site.status} />}
              {app && <StatusBadge status={app.health === 'running' ? 'ready' : app.health === 'crashed' ? 'failed' : 'pending'} />}
            </div>
            <div className="mt-[5px] text-[12px] text-muted">
              {runtimeLabel} {site?.runtime_version} · port {app?.internal_port ?? '—'} · app root /app
            </div>
          </div>
        </div>
        {app ? (
          <div className="flex flex-wrap items-center gap-2">
            <button className="btn-ghost" onClick={() => action('start')} disabled={busy}><Play size={14} /> Start</button>
            <button className="btn-ghost" onClick={() => action('stop')} disabled={busy}><Square size={14} /> Stop</button>
            <button className="btn-primary" onClick={() => action('restart')} disabled={busy}><RotateCw size={14} /> Restart</button>
            <button className="icon-btn !h-[36px] !w-[36px] hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Remove app" onClick={removeApp}>
              <Square size={15} />
            </button>
          </div>
        ) : (
          <button className="btn-brand" onClick={() => setShowWizard(true)}>
            <Globe size={15} /> Configure Application
          </button>
        )}
      </div>

      {err && <div className="mb-4 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{err}</div>}

      {/* App config */}
      {app ? (
        <Card className="mb-3.5">
          <CardHeader title="Application Configuration" subtitle="Build, start and proxy settings" right={
            <button className="text-[11px] font-bold text-brand hover:underline" onClick={() => setShowWizard(true)}>Edit <ArrowRight size={11} className="inline" /></button>
          } />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {[
              ['Startup file', app.startup_file || '—'],
              ['Startup command', app.startup_command || '(auto)'],
              ['Build command', app.build_command || '—'],
              ['Internal port', String(app.internal_port)],
              ['Environment vars', String(Object.keys(app.env_vars ?? {}).length)],
              ['Process', `epicpanel-app-${websiteId.slice(0, 8)}...`],
            ].map(([label, value]) => (
              <div key={label} className="rounded-[12px] border border-line px-3.5 py-3">
                <div className="text-[9.5px] font-extrabold uppercase tracking-[.06em] text-muted">{label}</div>
                <div className="mt-1 truncate font-mono text-[11.5px] font-semibold text-ink">{value}</div>
              </div>
            ))}
          </div>
          {app.env_vars && Object.keys(app.env_vars).length > 0 && (
            <div className="mt-4">
              <div className="mb-2 text-[9.5px] font-extrabold uppercase tracking-[.06em] text-muted">Environment variables</div>
              <div className="rounded-[12px] bg-navy p-4 font-mono text-[11.5px] leading-relaxed text-slate-300">
                {Object.entries(app.env_vars).map(([k, v]) => (
                  <div key={k}>{k}=<span className="text-slate-400">{v}</span></div>
                ))}
              </div>
            </div>
          )}
        </Card>
      ) : (
        <Card className="mb-3.5">
          <EmptyState
            icon={<Globe size={22} />}
            title="No application configured"
            subtitle={`Configure this ${runtimeLabel} site as an application to build, start and proxy it.`}
            action={<button className="btn-brand" onClick={() => setShowWizard(true)}>Configure Application</button>}
          />
        </Card>
      )}

      {/* Logs */}
      {app && (
        <Card className="mb-6">
          <CardHeader title="Application Logs" right={
            <button className="btn-ghost" onClick={fetchLogs} disabled={busy}><RefreshCw size={14} /> Fetch Logs</button>
          } />
          {logs !== null ? (
            <pre className="max-h-[50vh] overflow-auto rounded-xl bg-navy p-4 font-mono text-[12px] leading-relaxed text-slate-300">{logs || '(no output)'}</pre>
          ) : (
            <div className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-[13px] text-sub">
              Click "Fetch Logs" to pull the latest 200 lines from journald.
            </div>
          )}
        </Card>
      )}

      {/* Site tools */}
      <Card>
        <CardHeader title="Site Tools" />
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {[
            { to: `/sites/${websiteId}/files`, icon: Folder, label: 'File Manager', desc: 'Browse and edit app files' },
            { to: `/sites/${websiteId}/crons`, icon: Clock, label: 'Cron Jobs', desc: 'Scheduled tasks' },
            { to: `/sites/${websiteId}/ssh-keys`, icon: KeyRound, label: 'SSH Keys', desc: 'SSH access' },
            ...(site?.status === 'ready' ? [{ to: `/sites/${websiteId}/terminal`, icon: TerminalIcon, label: 'Terminal', desc: 'Sandboxed shell' }] : []),
            { to: '/backups', icon: History, label: 'Backups', desc: 'Backup & restore' },
            { to: '/security', icon: Lock, label: 'SSL / Security', desc: 'Certificates' },
          ].map((t) => {
            const Icon = t.icon
            return (
              <Link key={t.to} to={t.to} className="flex items-start gap-3.5 rounded-xl border border-line p-4 transition hover:border-brand/40 hover:bg-brand/[0.03]">
                <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-brand/10 text-brand">
                  <Icon size={18} />
                </div>
                <div>
                  <div className="text-[14px] font-bold text-ink">{t.label}</div>
                  <div className="mt-0.5 text-xs text-sub">{t.desc}</div>
                </div>
              </Link>
            )
          })}
        </div>
      </Card>

      {site && (
        <AppWizardModal
          open={showWizard}
          onClose={() => setShowWizard(false)}
          orgId={org?.id ?? ''}
          websiteId={websiteId}
          websiteName={site.name}
          runtime={site.runtime as 'node' | 'python' | 'go'}
          runtimeVersion={site.runtime_version}
          onQueued={() => void load()}
        />
      )}
    </div>
  )
}
