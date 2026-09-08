import { useState } from 'react'
import { api } from '@/lib/api'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'

interface Props {
  open: boolean
  onClose: () => void
  orgId: string
  websiteId: string
  websiteName: string
  runtime: 'node' | 'python' | 'go'
  runtimeVersion: string
  onQueued?: () => void
}

const PLACEHOLDERS: Record<string, { file: string; cmd: string; build: string; port: string }> = {
  node:   { file: 'server.js',            cmd: '',                          build: 'npm run build',              port: '3000' },
  python: { file: 'app:app',              cmd: '',                          build: '',                            port: '8000' },
  go:     { file: '',                     cmd: '',                          build: 'go build -o bin/app ./...',  port: '8080' },
}

export function AppWizardModal({ open, onClose, orgId, websiteId, websiteName, runtime, runtimeVersion, onQueued }: Props) {
  const defaults = PLACEHOLDERS[runtime] ?? PLACEHOLDERS.node
  const [form, setForm] = useState({
    startup_file: defaults.file,
    startup_command: defaults.cmd,
    build_command: defaults.build,
    internal_port: defaults.port,
    env_vars: 'NODE_ENV=production',
  })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const update = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async () => {
    setErr('')
    setBusy(true)
    try {
      const env: Record<string, string> = {}
      form.env_vars.split('\n').filter(Boolean).forEach((line) => {
        const [k, ...rest] = line.split('=')
        if (k && rest.length > 0) env[k.trim()] = rest.join('=')
      })
      await api.post(`/v1/organizations/${orgId}/websites/${websiteId}/application`, {
        startup_file: form.startup_file,
        startup_command: form.startup_command,
        build_command: form.build_command,
        internal_port: Number(form.internal_port) || 0,
        env_vars: env,
      })
      onClose()
      onQueued?.()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const runtimeLabel = runtime === 'node' ? 'Node.js' : runtime === 'python' ? 'Python' : 'Go'
  const fileLabel = runtime === 'node' ? 'Startup file' : runtime === 'python' ? 'WSGI/ASGI entry point (module:app)' : ''

  return (
    <Modal open={open} onClose={onClose} title={`Configure ${runtimeLabel} Application`} width="max-w-lg">
      <ErrorNote message={err} />
      <p className="mb-4 rounded-xl border border-brand/20 bg-brand/[0.04] px-3.5 py-2.5 text-[13px] leading-relaxed text-sub">
        Configures <b>{websiteName}</b> as a {runtimeLabel} {runtimeVersion} application.
        The panel will build, start a supervised process, and reverse-proxy your domain to it.
      </p>
      <Field label="Startup file" hint={runtime === 'node' ? 'Entry point, e.g. server.js or dist/index.js' : runtime === 'python' ? 'Gunicorn/uvicorn module:app (e.g. app:app for Flask/FastAPI)' : undefined}>
        <input className="input font-mono text-[13px]" value={form.startup_file} onChange={(e) => update('startup_file', e.target.value)} placeholder={defaults.file} />
      </Field>
      {runtime === 'node' && (
        <Field label="Startup command (optional)" hint="Overrides startup file. E.g. node dist/server.js">
          <input className="input font-mono text-[13px]" value={form.startup_command} onChange={(e) => update('startup_command', e.target.value)} placeholder="node server.js" />
        </Field>
      )}
      <Field label="Build command (optional)" hint={runtime === 'node' ? 'npm script name, e.g. "npm run build"' : runtime === 'go' ? 'go build command' : 'Leave empty to skip build'}>
        <input className="input font-mono text-[13px]" value={form.build_command} onChange={(e) => update('build_command', e.target.value)} placeholder={defaults.build} />
      </Field>
      <Field label="Internal port" hint="The port your app listens on locally. The panel reverse-proxies your domain to it.">
        <input className="input" type="number" value={form.internal_port} onChange={(e) => update('internal_port', e.target.value)} />
      </Field>
      <Field label="Environment variables" hint="One per line: KEY=value">
        <textarea className="input h-24 font-mono text-[12px]" value={form.env_vars} onChange={(e) => update('env_vars', e.target.value)} placeholder={'NODE_ENV=production\nDATABASE_URL=...'} />
      </Field>
      <button className="btn-brand w-full justify-center" onClick={submit} disabled={busy}>
        {busy ? 'Configuring...' : 'Create Application'}
      </button>
    </Modal>
  )
}
