import { useCallback, useEffect, useState } from 'react'
import {
  Play, Square, RotateCw, Terminal, ScrollText, Globe, Braces,
  Rocket, RefreshCw, Plus, X,
} from 'lucide-react'
import { applicationApi, appsApi, api, useAuth } from '@epicpanel/core'
import type { Application, Job, Website } from '@epicpanel/core'
import { Card, CardHeader, pushToast } from '@epicpanel/ui'
import { Field, ErrorNote, Modal } from '@epicpanel/forms'

// ============================================================================
// Apps & Tools section (site detail): application process manager
// (node/python/go), one-click WordPress + Laravel, and the allowlisted
// command runner (composer / npm / artisan / node...).
// ============================================================================

const POLL_MS = 2500
const POLL_MAX_MS = 5 * 60_000

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Poll the site's job history until the matching job finishes (or timeout). */
async function pollJob(orgId: string, websiteId: string, jobId: string | null, typeFilter: string): Promise<Job | null> {
  const deadline = Date.now() + POLL_MAX_MS
  while (Date.now() < deadline) {
    await sleep(POLL_MS)
    try {
      const r = await appsApi.jobs(orgId, websiteId)
      const hit = (r.jobs ?? []).find(
        (j) => (jobId ? j.id === jobId : j.type === typeFilter) && (j.status === 'success' || j.status === 'failed'),
      )
      if (hit) return hit
    } catch {
      // transient — keep polling
    }
  }
  return null
}

function pairAppRuntime(rt: string) {
  return rt === 'node' || rt === 'python' || rt === 'go'
}

export function SiteAppsSection({ site, canManage }: { site: Website; canManage: boolean }) {
  const { org } = useAuth()
  const orgId = org?.id ?? ''

  if (!site || site.status === 'deleted' || site.status === 'deleting') return null
  const showInstaller = site.runtime === 'php' && site.status === 'ready'
  const showApp = pairAppRuntime(site.runtime)
  const showCommands = site.status === 'ready' && site.runtime !== 'static'
  if (!showInstaller && !showApp && !showCommands) return null

  return (
    <>
      {showApp && <ApplicationCard orgId={orgId} site={site} canManage={canManage} />}
      {showInstaller && (
        <div className="mb-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
          <WordPressCard orgId={orgId} site={site} canManage={canManage} />
          <LaravelCard orgId={orgId} site={site} canManage={canManage} />
        </div>
      )}
      {showCommands && <CommandsCard orgId={orgId} site={site} canManage={canManage} />}
    </>
  )
}

// ---------------------------------------------------------------- application

function ApplicationCard({ orgId, site, canManage }: { orgId: string; site: Website; canManage: boolean }) {
  const [app, setApp] = useState<Application | null | 'missing'>(null)
  const [form, setForm] = useState({ startup_file: '', startup_command: '', build_command: '', internal_port: '' })
  const [env, setEnv] = useState<Array<{ k: string; v: string }>>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [logs, setLogs] = useState('')
  const [edit, setEdit] = useState(false)

  const load = useCallback(() => {
    if (!orgId) return
    applicationApi
      .get(orgId, site.id)
      .then(setApp)
      .catch(() => setApp('missing'))
  }, [orgId, site.id])

  useEffect(() => {
    load()
  }, [load])

  const openEdit = () => {
    if (typeof app === 'string' || !app) return
    setForm({
      startup_file: app.startup_file ?? '',
      startup_command: app.startup_command ?? '',
      build_command: app.build_command ?? '',
      internal_port: app.internal_port ? String(app.internal_port) : '',
    })
    setEdit(true)
  }

  const save = async () => {
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        startup_file: form.startup_file.trim(),
        startup_command: form.startup_command.trim(),
        build_command: form.build_command.trim(),
      }
      if (form.internal_port.trim()) body.internal_port = Number(form.internal_port.trim())
      const envObj: Record<string, string> = {}
      for (const e of env) if (e.k.trim()) envObj[e.k.trim()] = e.v
      if (Object.keys(envObj).length > 0) body.env_vars = envObj
      if (typeof app === 'string' && app === 'missing') {
        await applicationApi.create(orgId, site.id, body as any)
        pushToast('success', 'Application configured — dependencies install and the proxy updates now.')
      } else {
        await applicationApi.update(orgId, site.id, body as any)
        pushToast('success', 'Application updated — restart queued.')
      }
      setEdit(false)
      setEnv([])
      await sleep(600)
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to save application')
    } finally {
      setBusy(false)
    }
  }

  const lifecycle = async (action: 'start' | 'stop' | 'restart') => {
    setBusy(true)
    try {
      await (applicationApi as any)[action](orgId, site.id)
      pushToast('success', `Application ${action} queued.`)
      await sleep(1200)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? `Failed to ${action}`)
    } finally {
      setBusy(false)
    }
  }

  const fetchLogs = async () => {
    setBusy(true)
    try {
      await api.get(`/v1/organizations/${orgId}/websites/${site.id}/application/logs?lines=200`)
      const job = await pollJob(orgId, site.id, null, 'app_logs')
      const out = (job?.result as any)?.logs
      setLogs(typeof out === 'string' && out ? out : 'No log output (is the app running?)')
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to fetch logs')
    } finally {
      setBusy(false)
    }
  }

  const missing = app === 'missing'
  const healthTone = typeof app === 'object' && app ? (app.health === 'running' ? 'status-live' : app.health === 'crashed' ? 'status-danger' : '') : ''

  return (
    <Card className="mb-4">
      <CardHeader
        title="Application"
        subtitle={`${site.runtime} app — supervised process with reverse proxy on your domain`}
        right={
          <div className="flex items-center gap-2">
            {typeof app === 'object' && app && (
              <span className={`status-chip ${healthTone}`}>{app.health}</span>
            )}
            {canManage && typeof app === 'object' && app && (
              <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={openEdit}><Braces size={12} /> Edit</button>
            )}
          </div>
        }
      />
      <ErrorNote message={err} />
      {app === null ? (
        <p className="px-4 pb-4 text-[11px] text-muted">Loading…</p>
      ) : missing ? (
        canManage ? (
          <div className="px-4 pb-4">
            <p className="mb-3 text-[11px] text-muted">
              No application configured yet. Point EpicPanel at your start file and build command — it installs
              dependencies, supervises the process and proxies your domain to it.
            </p>
            <AppForm form={form} setForm={setForm} env={env} setEnv={setEnv} />
            <button className="btn-brand" onClick={save} disabled={busy || (!form.startup_file.trim() && !form.startup_command.trim())}>
              {busy ? 'Saving…' : 'Configure application'}
            </button>
          </div>
        ) : (
          <p className="px-4 pb-4 text-[11px] text-muted">No application configured for this website.</p>
        )
      ) : (
        typeof app === 'object' && app && (
          <div className="space-y-3 px-4 pb-4">
            <div className="grid grid-cols-2 gap-3 text-[11px] lg:grid-cols-4">
              <Info label="Startup" value={app.startup_command || app.startup_file || '—'} />
              <Info label="Build" value={app.build_command || '—'} />
              <Info label="Internal port" value={String(app.internal_port)} />
              <Info label="Process" value={app.process_name || '—'} />
            </div>
            {canManage && (
              <div className="flex flex-wrap gap-2">
                <button className="btn-ghost" onClick={() => lifecycle('start')} disabled={busy}><Play size={13} /> Start</button>
                <button className="btn-ghost" onClick={() => lifecycle('stop')} disabled={busy}><Square size={13} /> Stop</button>
                <button className="btn-ghost" onClick={() => lifecycle('restart')} disabled={busy}><RotateCw size={13} /> Restart</button>
                <button className="btn-ghost" onClick={fetchLogs} disabled={busy}><ScrollText size={13} /> Logs</button>
              </div>
            )}
            {logs && (
              <pre className="max-h-64 overflow-auto rounded-[10px] bg-[#0d1322] p-3 font-mono text-[10.5px] leading-relaxed text-[#c9d4e8]">{logs}</pre>
            )}
          </div>
        )
      )}

      <Modal open={edit} onClose={() => setEdit(false)} title="Edit application" subtitle="Changes restart the app. Environment values are kept unless you add new ones.">
        <AppForm form={form} setForm={setForm} env={env} setEnv={setEnv} />
        <button className="btn-brand w-full justify-center" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save & restart'}
        </button>
      </Modal>
    </Card>
  )
}

function Info({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[10px] border border-line bg-surface-2 px-3 py-2">
      <div className="text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{label}</div>
      <div className="mt-0.5 truncate font-mono text-[10.5px] text-ink" title={value}>{value}</div>
    </div>
  )
}

function AppForm({ form, setForm, env, setEnv }: {
  form: { startup_file: string; startup_command: string; build_command: string; internal_port: string }
  setForm: (f: { startup_file: string; startup_command: string; build_command: string; internal_port: string }) => void
  env: Array<{ k: string; v: string }>
  setEnv: (e: Array<{ k: string; v: string }>) => void
}) {
  return (
    <div className="mb-3 space-y-2">
      <Field label="Startup file" hint="e.g. server.js (Node) — used when no custom start command is set">
        <input className="input" value={form.startup_file} onChange={(e) => setForm({ ...form, startup_file: e.target.value })} placeholder="server.js" />
      </Field>
      <Field label="Startup command (optional)" hint="Overrides the startup file, e.g. node dist/main.js or npm run start">
        <input className="input" value={form.startup_command} onChange={(e) => setForm({ ...form, startup_command: e.target.value })} placeholder="npm run start" />
      </Field>
      <Field label="Build command (optional)" hint="Runs on deploy, e.g. npm run build">
        <input className="input" value={form.build_command} onChange={(e) => setForm({ ...form, build_command: e.target.value })} placeholder="npm run build" />
      </Field>
      <Field label="Internal port (optional)" hint="Port your app listens on — leave empty to auto-allocate">
        <input className="input" value={form.internal_port} onChange={(e) => setForm({ ...form, internal_port: e.target.value })} placeholder="3000" />
      </Field>
      <div>
        <div className="mb-1 flex items-center justify-between">
          <span className="text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">Environment variables</span>
          <button className="icon-btn" title="Add variable" aria-label="Add variable" onClick={() => setEnv([...env, { k: '', v: '' }])}><Plus size={12} /></button>
        </div>
        {env.map((e, i) => (
          <div key={i} className="mb-1.5 flex gap-1.5">
            <input className="input w-1/3" value={e.k} placeholder="KEY" onChange={(ev) => setEnv(env.map((x, j) => (j === i ? { ...x, k: ev.target.value.toUpperCase() } : x)))} />
            <input className="input flex-1" value={e.v} placeholder="value" onChange={(ev) => setEnv(env.map((x, j) => (j === i ? { ...x, v: ev.target.value } : x)))} />
            <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Remove" aria-label="Remove variable" onClick={() => setEnv(env.filter((_, j) => j !== i))}><X size={12} /></button>
          </div>
        ))}
      </div>
    </div>
  )
}

// ------------------------------------------------------------------ wordpress

function WordPressCard({ orgId, site, canManage }: { orgId: string; site: Website; canManage: boolean }) {
  const [show, setShow] = useState(false)
  const [form, setForm] = useState({ title: '', admin_user: '', admin_email: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [creds, setCreds] = useState<{ user: string; pass: string; url: string } | null>(null)
  const [installed, setInstalled] = useState(false)

  useEffect(() => {
    // Already installed? A previous install_wordpress success says so.
    appsApi.jobs(orgId, site.id).then((r) => {
      const done = (r.jobs ?? []).find((j) => j.type === 'install_wordpress' && j.status === 'success')
      if (done) setInstalled(true)
    }).catch(() => {})
  }, [orgId, site.id])

  const install = async () => {
    setErr('')
    setBusy(true)
    try {
      await appsApi.installWordPress(orgId, site.id, {
        admin_user: form.admin_user.trim(),
        admin_email: form.admin_email.trim(),
        title: form.title.trim(),
      })
      setShow(false)
      pushToast('success', 'WordPress is installing — this takes about a minute.')
      const job = await pollJob(orgId, site.id, null, 'install_wordpress')
      if (job?.status === 'failed') {
        pushToast('error', job.error ?? 'WordPress install failed')
        return
      }
      const r = (job?.result ?? {}) as any
      setInstalled(true)
      if (r.admin_password) {
        setCreds({ user: r.admin_user ?? form.admin_user, pass: r.admin_password, url: r.admin_url ?? '' })
      } else {
        pushToast('success', 'WordPress installed.')
      }
    } catch (ex: any) {
      setErr(ex.message ?? 'WordPress install failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="mb-0">
      <CardHeader title="WordPress" subtitle={installed ? 'Installed on this site' : 'One-click install — database included'} />
      <ErrorNote message={err} />
      {creds && (
        <div className="mx-4 mb-3 rounded-[10px] border border-[#ffd88a] bg-[#fff8e8] px-3 py-2.5 text-[11px]">
          <p className="font-bold text-[#7a5200]">Save these credentials — shown only once</p>
          <p className="mt-1 font-mono text-[10.5px] text-[#5c4a00]">
            {creds.user} / {creds.pass}<br />{creds.url}
          </p>
        </div>
      )}
      <div className="px-4 pb-4">
        {installed ? (
          <p className="text-[11px] text-muted">
            WordPress is installed. Log in at <span className="font-mono">/wp-admin</span> — wp-cli and PHP run as this site's user.
          </p>
        ) : canManage ? (
          <>
            <button className="btn-primary" onClick={() => setShow(true)}><Globe size={14} /> Install WordPress</button>
            <Modal open={show} onClose={() => setShow(false)} title="Install WordPress" subtitle="A fresh database is created and wired up automatically.">
              <Field label="Site title">
                <input className="input" value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} placeholder="My blog" />
              </Field>
              <Field label="Admin username" hint="3+ characters, letters and digits">
                <input className="input" value={form.admin_user} onChange={(e) => setForm({ ...form, admin_user: e.target.value })} placeholder="admin" />
              </Field>
              <Field label="Admin email">
                <input className="input" type="email" value={form.admin_email} onChange={(e) => setForm({ ...form, admin_email: e.target.value })} placeholder="you@example.com" />
              </Field>
              <button className="btn-brand w-full justify-center" onClick={install} disabled={busy || !form.admin_user || !form.admin_email}>
                {busy ? (<><RefreshCw size={13} className="animate-spin" /> Installing…</>) : 'Install WordPress'}
              </button>
            </Modal>
          </>
        ) : (
          <p className="text-[11px] text-muted">WordPress is not installed on this site yet.</p>
        )}
      </div>
    </Card>
  )
}

// -------------------------------------------------------------------- laravel

function LaravelCard({ orgId, site, canManage }: { orgId: string; site: Website; canManage: boolean }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [state, setState] = useState<'idle' | 'running' | 'done'>('idle')
  const [version, setVersion] = useState('')

  useEffect(() => {
    appsApi.jobs(orgId, site.id).then((r) => {
      const done = (r.jobs ?? []).find((j) => j.type === 'install_laravel' && j.status === 'success')
      if (done) {
        setState('done')
        setVersion(((done.result as any)?.version as string) ?? '')
      }
    }).catch(() => {})
  }, [orgId, site.id])

  const install = async () => {
    setErr('')
    setBusy(true)
    try {
      const res = await appsApi.installLaravel(orgId, site.id)
      setState('running')
      pushToast('success', `composer create-project started on PHP ${res.php}.`)
      const job = await pollJob(orgId, site.id, res.job_id, 'install_laravel')
      if (job?.status === 'failed') {
        setState('idle')
        pushToast('error', job.error?.slice(0, 300) ?? 'Laravel install failed')
        return
      }
      setState('done')
      setVersion(((job?.result as any)?.version as string) ?? '')
      pushToast('success', 'Laravel installed — your domain now serves app/public.')
    } catch (ex: any) {
      setErr(ex.message ?? 'Laravel install failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader title="Laravel" subtitle={`composer create-project on the site's PHP ${site.runtime_version}`} />
      <ErrorNote message={err} />
      <div className="px-4 pb-4">
        {state === 'running' ? (
          <p className="flex items-center gap-2 text-[11px] text-muted"><RefreshCw size={13} className="animate-spin" /> Downloading Laravel and installing dependencies — this can take a few minutes…</p>
        ) : state === 'done' ? (
          <p className="text-[11px] text-muted">
            Laravel {version || ''} installed. Serving from <span className="font-mono">app/public</span> — run composer and artisan from the commands tool below.
          </p>
        ) : canManage ? (
          <button className="btn-primary" onClick={install} disabled={busy}><Rocket size={14} /> {busy ? 'Starting…' : 'Install Laravel'}</button>
        ) : (
          <p className="text-[11px] text-muted">Laravel is not installed on this site yet.</p>
        )}
      </div>
    </Card>
  )
}

// ------------------------------------------------------------------- commands

const commandPresets: Record<string, string[]> = {
  php: ['composer install', 'composer update', 'composer require', 'php artisan list', 'php artisan migrate --force', 'wp core version'],
  node: ['npm install', 'npm ci', 'npm run build', 'npm run dev', 'node -v', 'npx -v'],
  python: ['pip3 install -r requirements.txt', 'python3 -V'],
  go: ['go build ./...', 'go version'],
}

function CommandsCard({ orgId, site, canManage }: { orgId: string; site: Website; canManage: boolean }) {
  const [cmd, setCmd] = useState('')
  const [busy, setBusy] = useState(false)
  const [output, setOutput] = useState('')
  const [err, setErr] = useState('')

  const run = async (raw?: string) => {
    const command = (raw ?? cmd).trim()
    if (!command) return
    setErr('')
    setBusy(true)
    if (!raw) setCmd(command)
    try {
      const res = await appsApi.runCommand(orgId, site.id, command)
      setOutput(`$ ${res.argv.join(' ')}\n`)
      const job = await pollJob(orgId, site.id, res.job_id, 'site_command')
      if (!job) {
        setOutput((o) => o + '\n[timed out waiting for output — check the Activity feed]')
        return
      }
      const out = ((job.result as any)?.output as string) ?? ''
      setOutput(`$ ${res.argv.join(' ')}\n${out}${job.status === 'failed' ? `\n${job.error ?? ''}` : ''}`)
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to run command')
    } finally {
      setBusy(false)
    }
  }

  const presets = commandPresets[site.runtime] ?? []

  return (
    <Card className="mb-4">
      <CardHeader
        title="Commands"
        subtitle="Run composer, npm, artisan and more inside your site — as the site user"
        right={<Terminal size={14} className="text-[#7a8597]" />}
      />
      <ErrorNote message={err} />
      <div className="space-y-3 px-4 pb-4">
        {presets.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {presets.map((p) => (
              <button
                key={p}
                className="status-chip cursor-pointer font-mono hover:!border-brand hover:!text-brand"
                onClick={() => canManage && run(p)}
                disabled={busy}
                title={canManage ? `Run: ${p}` : 'Read-only'}
              >
                {p}
              </button>
            ))}
          </div>
        )}
        {canManage && (
          <div className="flex gap-2">
            <input
              className="input flex-1 font-mono"
              value={cmd}
              onChange={(e) => setCmd(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !busy && run()}
              placeholder={site.runtime === 'node' ? 'npm install' : 'composer install'}
              disabled={busy}
            />
            <button className="btn-brand" onClick={() => run()} disabled={busy || !cmd.trim()}>
              {busy ? 'Running…' : 'Run'}
            </button>
          </div>
        )}
        {output && (
          <pre className="max-h-72 overflow-auto rounded-[10px] bg-[#0d1322] p-3 font-mono text-[10.5px] leading-relaxed text-[#c9d4e8]">{output}</pre>
        )}
      </div>
    </Card>
  )
}
