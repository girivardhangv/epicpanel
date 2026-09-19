import { useCallback, useEffect, useState } from 'react'
import { Terminal, Play, Square, RotateCw, Hammer, Save } from 'lucide-react'
import { api, appApi } from '@epicpanel/core'
import type { AppConfig } from '@epicpanel/core'
import { Card, CardHeader, pushToast } from '@epicpanel/ui'
import { Field, ErrorNote } from '@epicpanel/forms'

// App management panel for node/python/go websites: process controls, config
// (start/build commands + env) and journald log viewer. Config is desired
// state — saving converges through the job queue (provision → build → start).
export function AppPanel({ orgId, websiteId }: { orgId: string; websiteId: string }) {
  const [app, setApp] = useState<AppConfig | null | undefined>(undefined) // undefined = loading
  const [startup, setStartup] = useState('')
  const [build, setBuild] = useState('')
  const [envText, setEnvText] = useState('')
  const [logs, setLogs] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const r = await appApi.get(orgId, websiteId)
      setApp(r.app)
      if (r.app) {
        setStartup(r.app.startup_command ?? '')
        setBuild(r.app.build_command ?? '')
        setEnvText(Object.entries(r.app.env ?? {}).map(([k, v]) => `${k}=${v}`).join('\n'))
      }
    } catch {
      setApp(null)
    }
  }, [orgId, websiteId])

  useEffect(() => {
    void load()
  }, [load])

  const run = async (fn: () => Promise<unknown>, msg: string) => {
    setErr('')
    setBusy(true)
    try {
      await fn()
      pushToast('success', msg)
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  const save = () =>
    run(async () => {
      const env: Record<string, string> = {}
      for (const line of envText.split('\n')) {
        const i = line.indexOf('=')
        if (i > 0) env[line.slice(0, i).trim()] = line.slice(i + 1).trim()
      }
      await appApi.set(orgId, websiteId, { startup_command: startup, build_command: build, env })
      setLogs(null)
    }, 'App config saved — the process is converging.')

  const fetchLogs = () =>
    run(async () => {
      const { job_id: jobId } = await appApi.logs(orgId, websiteId, 300)
      for (let i = 0; i < 20; i++) {
        await new Promise((r) => setTimeout(r, 1500))
        const { jobs } = await api.get<{ jobs: { id: string; status: string; result?: string }[] }>(
          `/v1/organizations/${orgId}/websites/${websiteId}/jobs`,
        )
        const j = jobs.find((x) => x.id === jobId)
        if (j?.status === 'success') {
          try {
            setLogs(JSON.parse(j.result ?? '{}').logs ?? '(no output)')
          } catch {
            setLogs(j.result ?? '(no output)')
          }
          return
        }
        if (j?.status === 'failed') {
          setLogs('failed to read logs: ' + (j as any).error)
          return
        }
      }
      setLogs('(log fetch is taking longer than expected — try again)')
    }, '')

  if (app === undefined) return null
  if (app === null) return null

  return (
    <Card className="mb-4 overflow-hidden !p-0">
      <CardHeader
        className="mx-4 mt-4 !mb-0"
        title="Application"
        subtitle={`${app.runtime} ${app.runtime_version} · process ${app.desired_state} · port ${app.port || '—'}`}
        right={
          <div className="flex gap-1.5">
            {app.desired_state === 'running' ? (
              <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" disabled={busy} onClick={() => void run(() => appApi.stop(orgId, websiteId), 'Stopping the app…')}><Square size={12} /> Stop</button>
            ) : (
              <button className="btn-primary !min-h-[30px] !px-2.5 !text-[10.5px]" disabled={busy} onClick={() => void run(() => appApi.start(orgId, websiteId), 'Build + start queued.')}><Play size={12} /> Start</button>
            )}
            <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" disabled={busy} onClick={() => void run(() => appApi.restart(orgId, websiteId), 'Restart queued.')}><RotateCw size={12} /> Restart</button>
            <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" disabled={busy} onClick={() => void run(() => appApi.build(orgId, websiteId), 'Build queued.')}><Hammer size={12} /> Build</button>
          </div>
        }
      />
      <div className="p-4 pt-3">
        <ErrorNote message={err} />
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <Field label="Start command" hint="Empty = smart default (node server.js / gunicorn/uvicorn / ./bin/app)">
            <input className="input font-mono !text-[11px]" value={startup} onChange={(e) => setStartup(e.target.value)} />
          </Field>
          <Field label="Build command" hint="Ran on every deploy, before the release goes live">
            <input className="input font-mono !text-[11px]" value={build} onChange={(e) => setBuild(e.target.value)} />
          </Field>
        </div>
        <Field label="Environment variables" hint="One per line, KEY=value. Stored encrypted at rest.">
          <textarea className="input min-h-[70px] font-mono !text-[11px]" value={envText} onChange={(e) => setEnvText(e.target.value)} placeholder={'DATABASE_URL=postgres://…\nNODE_ENV=production'} />
        </Field>
        <div className="flex gap-2">
          <button className="btn-brand !min-h-[30px] !px-3 !text-[10.5px]" disabled={busy} onClick={() => void save()}><Save size={12} /> Save & apply</button>
          <button className="btn-ghost !min-h-[30px] !px-3 !text-[10.5px]" disabled={busy} onClick={() => void fetchLogs()}><Terminal size={12} /> View logs</button>
        </div>
        {logs !== null && (
          <pre className="mt-3 max-h-[240px] overflow-auto rounded-[8px] bg-[#0f172a] p-3 text-[10px] leading-[1.5] text-[#c9d4e5]">{logs || '(no output)'}</pre>
        )}
      </div>
    </Card>
  )
}
