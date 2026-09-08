import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  Globe, ShieldCheck, ShieldAlert, ArrowRight, ArrowLeft, Check, Package,
  Loader2, PartyPopper, Server, Cpu, Database as DbIcon, Layers,
} from 'lucide-react'
import { api } from '@/lib/api'
import { Field, ErrorNote } from '@/components/ui'

// --- types -----------------------------------------------------------------

interface SetupStatus {
  setup_completed: boolean
  hostname?: string
  has_users?: boolean
  token_valid?: boolean
}

interface CatalogItem {
  type: string
  label: string
  description: string
  versions: string[]
  recommended: boolean
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

const ICONS: Record<string, React.ReactNode> = {
  php: <Layers size={18} />,
  node: <Cpu size={18} />,
  python: <Cpu size={18} />,
  go: <Cpu size={18} />,
  apache: <Server size={18} />,
  openlitespeed: <Server size={18} />,
  dbtools: <DbIcon size={18} />,
}

const DEFAULT_VERSIONS: Record<string, string> = {
  php: '8.3', node: '22', python: '3.12', go: '1.22', apache: '2.4', openlitespeed: '1.8', dbtools: 'latest',
}

const TYPE_LABEL: Record<string, string> = {
  install_runtime: 'Software',
  install_database_tools: 'Database tools',
  install_extension: 'PHP extension',
}

// --- page ------------------------------------------------------------------

export function SetupPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''

  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [step, setStep] = useState(0) // 0 welcome, 1 software, 2 hostname, 3 admin, 4 done
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // software step
  const [catalog, setCatalog] = useState<CatalogItem[]>([])
  const [selected, setSelected] = useState<Record<string, string>>({})
  const [jobs, setJobs] = useState<SetupJob[]>([])

  // hostname step
  const [hostname, setHostname] = useState('')
  const [dns, setDns] = useState<'idle' | 'checking' | 'ok' | 'mismatch' | 'error' | 'ip'>('idle')
  const [dnsDetail, setDnsDetail] = useState<{ resolved?: string[]; note?: string; error?: string }>({})

  // admin step
  const [admin, setAdmin] = useState({ name: 'Admin', email: '', password: '' })

  useEffect(() => {
    api.get<SetupStatus>(`/v1/setup/status${token ? `?token=${encodeURIComponent(token)}` : ''}`)
      .then((r) => {
        setStatus(r)
        if (r.setup_completed) navigate('/login', { replace: true })
      })
      .catch(() => setStatus({ setup_completed: false }))
  }, [token, navigate]) // eslint-disable-line react-hooks/exhaustive-deps

  // poll install jobs while the software step is active
  const pollJobs = useCallback(async () => {
    if (!token) return
    try {
      const r = await api.get<{ jobs: SetupJob[] }>(`/v1/setup/jobs?token=${encodeURIComponent(token)}`)
      setJobs(r.jobs ?? [])
    } catch { /* transient */ }
  }, [token])

  useEffect(() => {
    if (step !== 1 || !token) return
    void pollJobs()
    const t = setInterval(() => void pollJobs(), 2500)
    return () => clearInterval(t)
  }, [step, token, pollJobs])

  const installJobs = useMemo(() => jobs.filter((j) => j.type === 'install_runtime' || j.type === 'install_database_tools'), [jobs])
  const anyActive = installJobs.some((j) => j.status === 'pending' || j.status === 'running')
  const anyFailed = installJobs.some((j) => j.status === 'failed')

  const startSoftware = async () => {
    setErr('')
    if (!token) {
      setErr('Setup link is missing its token — open the exact link printed by the installer.')
      return
    }
    const items = Object.entries(selected).map(([type, version]) => ({ type, version }))
    if (items.length === 0) {
      setStep(2)
      return
    }
    setBusy(true)
    try {
      await api.post('/v1/setup/software', { token, items })
      setTimeout(() => void pollJobs(), 800)
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to start installation')
    } finally {
      setBusy(false)
    }
  }

  const checkDns = async () => {
    const h = hostname.trim().toLowerCase()
    if (!h) return
    setErr('')
    setDns('checking')
    try {
      const r = await api.post<{ matched: boolean; resolved?: string[]; note?: string; error?: string }>('/v1/setup/verify-hostname', { hostname: h })
      setDnsDetail(r)
      if (r.note) {
        setDns('ip')
      } else if (r.matched) {
        setDns('ok')
      } else if (r.error) {
        setDns('error')
      } else {
        setDns('mismatch')
      }
    } catch (ex: any) {
      setDns('error')
      setDnsDetail({ error: ex.message })
    }
  }

  const finishSetup = async () => {
    setErr('')
    if (!token) {
      setErr('Setup link is missing its token — open the exact link printed by the installer.')
      return
    }
    setBusy(true)
    try {
      await api.post('/v1/setup', {
        token,
        hostname: hostname.trim().toLowerCase(),
        email: admin.email,
        password: admin.password,
        name: admin.name,
      })
      setStep(4)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  if (status === null) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-app p-4">
        <Loader2 className="animate-spin text-brand" size={28} />
      </div>
    )
  }

  const tokenKnown = status.has_users === false && token !== '' && status.token_valid === true
  // Show a warning (but let the user proceed) when the link has no token —
  // the installer always prints one; direct visits get instructions.
  const noToken = token === ''

  return (
    <div className="flex min-h-screen items-center justify-center bg-app p-4">
      <div className="w-full max-w-[560px] fade-up">
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <div
            className="flex h-14 w-14 items-center justify-center rounded-[16px] text-white"
            style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)', boxShadow: '0 8px 18px rgba(37,99,235,.27)' }}
          >
            <Globe size={28} strokeWidth={1.8} />
          </div>
          <div>
            <div className="text-[26px] font-bold tracking-[-.025em] text-ink">Welcome to EpicPanel</div>
            <p className="mx-auto mt-1 max-w-[380px] text-[12px] text-muted">
              {STEPS[step].subtitle}
            </p>
          </div>
        </div>

        <StepBar step={step} />

        <div className="card p-7">
          <ErrorNote message={err} />

          {noToken && step < 4 && (
            <div className="mb-5 rounded border border-[#ffe8b1] bg-warn-soft px-4 py-3 text-[13px] text-warn">
              <b>No setup token in this link.</b> For security, first-time setup requires the one-time link printed by the
              installer on your server. Run <code className="rounded bg-danger-soft px-1.5 py-0.5 font-mono text-[12px]">sudo epicpanel-api setup-token</code> and open the URL it prints.
            </div>
          )}

          {step === 0 && (
            <div className="space-y-5">
              <p className="text-[13.5px] leading-relaxed text-sub">
                Your server is prepared. The next steps will:
              </p>
              <ol className="space-y-2.5 text-[13.5px] text-ink">
                <li className="flex items-start gap-2.5"><Num n={1} /> Install the software you need — PHP, Node.js, Python, Go, web servers and database tools — as managed packages.</li>
                <li className="flex items-start gap-2.5"><Num n={2} /> Verify that your panel hostname points to this server.</li>
                <li className="flex items-start gap-2.5"><Num n={3} /> Create your administrator account with your secure one-time link.</li>
              </ol>
              <button className="btn-brand w-full justify-center py-2.5" onClick={() => setStep(1)}>
                Get Started <ArrowRight size={16} />
              </button>
            </div>
          )}

          {step === 1 && (
            <div className="space-y-5">
              <div className="grid grid-cols-1 gap-2.5">
                {catalog.map((item) => {
                  const active = selected[item.type] !== undefined
                  return (
                    <label
                      key={item.type}
                      className={`flex cursor-pointer items-start gap-3 rounded border p-3.5 transition ${active ? 'border-brand bg-brand/[0.04]' : 'border-line hover:border-brand/40'}`}
                    >
                      <input
                        type="checkbox"
                        className="mt-1 accent-[color:var(--brand)]"
                        checked={active}
                        onChange={(e) => {
                          setSelected((prev) => {
                            const next = { ...prev }
                            if (e.target.checked) next[item.type] = DEFAULT_VERSIONS[item.type] ?? 'latest'
                            else delete next[item.type]
                            return next
                          })
                        }}
                      />
                      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-brand/10 text-brand">
                        {ICONS[item.type] ?? <Package size={18} />}
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-[14px] font-bold text-ink">{item.label}</span>
                          {item.recommended && <span className="rounded-full bg-brand/10 px-2 py-0.5 text-[10.5px] font-bold text-brand">RECOMMENDED</span>}
                        </div>
                        <div className="mt-0.5 text-xs text-sub">{item.description}</div>
                        {active && item.versions.length > 1 && (
                          <div className="mt-2 flex flex-wrap gap-1.5">
                            {item.versions.map((v) => (
                              <button
                                key={v}
                                type="button"
                                onClick={(e) => { e.preventDefault(); setSelected((prev) => ({ ...prev, [item.type]: v })) }}
                                className={`rounded px-2.5 py-1 text-[11.5px] font-bold transition ${selected[item.type] === v ? 'bg-brand text-white' : 'bg-surface-2 text-sub hover:text-ink'}`}
                              >
                                {v}
                              </button>
                            ))}
                          </div>
                        )}
                      </div>
                    </label>
                  )
                })}
              </div>

              {installJobs.length > 0 && (
                <div className="rounded-[12px] border border-line p-4">
                  <div className="mb-2.5 flex items-center gap-2 text-[13px] font-bold text-ink">
                    {anyActive ? <Loader2 size={14} className="animate-spin text-brand" /> : anyFailed ? <ShieldAlert size={14} className="text-danger" /> : <Check size={14} className="text-ok" />}
                    Installation {anyActive ? 'in progress…' : anyFailed ? 'finished with errors' : 'complete'}
                  </div>
                  <div className="space-y-2.5">
                    {installJobs.map((j) => {
                      const label = j.payload?.type ? `${j.payload.type}${j.payload.version && j.payload.version !== 'latest' ? ' ' + j.payload.version : ''}` : TYPE_LABEL[j.type] ?? j.type
                      const failed = j.status === 'failed'
                      return (
                        <div key={j.id}>
                          <div className="flex items-center justify-between text-[12.5px]">
                            <span className="font-semibold capitalize text-ink">{label}</span>
                            <span className={failed ? 'text-danger' : j.status === 'success' ? 'text-ok' : 'text-muted'}>
                              {j.status === 'running' || j.status === 'pending' ? `${j.progress}%` : j.status}
                            </span>
                          </div>
                          <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-app">
                            <div
                              className={`h-full rounded-full transition-all duration-500 ${failed ? 'bg-danger' : 'bg-brand'}`}
                              style={{ width: `${j.status === 'success' ? 100 : failed ? 100 : Math.max(4, j.progress)}%` }}
                            />
                          </div>
                          <div className="mt-1 truncate text-[11.5px] text-muted">
                            {failed ? j.error : j.progress_step || (j.status === 'success' ? 'Done' : 'Queued…')}
                          </div>
                        </div>
                      )
                    })}
                  </div>
                </div>
              )}

              <div className="flex gap-2">
                <button className="btn-ghost flex-1 justify-center" onClick={() => setStep(0)}><ArrowLeft size={15} /> Back</button>
                <button className="btn-brand flex-1 justify-center" onClick={startSoftware} disabled={busy}>
                  {installJobs.length === 0 ? 'Install Selected' : anyActive ? 'Installing…' : 'Continue'} <ArrowRight size={16} />
                </button>
              </div>
            </div>
          )}

          {step === 2 && (
            <div className="space-y-5">
              <Field label="Panel hostname" hint="Where this panel will be reachable, e.g. panel.example.com. You can also leave it empty.">
                <div className="flex gap-2">
                  <input
                    className="input"
                    value={hostname}
                    onChange={(e) => { setHostname(e.target.value); setDns('idle') }}
                    placeholder="panel.example.com"
                  />
                  <button className="btn-primary shrink-0" onClick={checkDns} disabled={!hostname.trim() || dns === 'checking'}>
                    {dns === 'checking' ? <Loader2 size={15} className="animate-spin" /> : <ShieldCheck size={15} />} Check
                  </button>
                </div>
              </Field>

              {dns === 'ip' && (
                <Note tone="ok"><ShieldCheck size={14} /> {dnsDetail.note}</Note>
              )}
              {dns === 'ok' && (
                <Note tone="ok"><ShieldCheck size={14} /> {hostname} points to this server ({dnsDetail.resolved?.join(', ')}) — verified.</Note>
              )}
              {dns === 'mismatch' && (
                <Note tone="warn">
                  <ShieldAlert size={14} /> {hostname} resolves to {dnsDetail.resolved?.join(', ')} which is not this server.
                  Add an A record pointing to this machine's IP, or continue anyway if you know it will be updated.
                </Note>
              )}
              {dns === 'error' && (
                <Note tone="warn"><ShieldAlert size={14} /> {dnsDetail.error ?? 'Could not resolve this hostname yet — DNS may need a few minutes to propagate.'}</Note>
              )}

              <div className="flex gap-2">
                <button className="btn-ghost flex-1 justify-center" onClick={() => setStep(1)}><ArrowLeft size={15} /> Back</button>
                <button className="btn-brand flex-1 justify-center" onClick={() => setStep(3)}>
                  {dns === 'mismatch' || dns === 'error' ? 'Continue Anyway' : 'Continue'} <ArrowRight size={16} />
                </button>
              </div>
            </div>
          )}

          {step === 3 && (
            <form onSubmit={(e) => { e.preventDefault(); void finishSetup() }} className="space-y-4">
              <Field label="Administrator name">
                <input className="input" value={admin.name} onChange={(e) => setAdmin({ ...admin, name: e.target.value })} placeholder="Admin" />
              </Field>
              <Field label="Admin email">
                <input className="input" type="email" required value={admin.email} onChange={(e) => setAdmin({ ...admin, email: e.target.value })} placeholder="admin@example.com" />
              </Field>
              <Field label="Admin password" hint="At least 10 characters — this account owns the whole panel.">
                <input className="input" type="password" required minLength={10} value={admin.password} onChange={(e) => setAdmin({ ...admin, password: e.target.value })} />
              </Field>
              <div className="flex gap-2">
                <button type="button" className="btn-ghost flex-1 justify-center" onClick={() => setStep(2)}><ArrowLeft size={15} /> Back</button>
                <button className="btn-brand flex-1 justify-center" disabled={busy || noToken}>
                  {busy ? 'Creating…' : 'Create Admin Account'}
                </button>
              </div>
            </form>
          )}

          {step === 4 && (
            <div className="space-y-5 text-center">
              <div className="flex justify-center"><PartyPopper size={36} className="text-brand" /></div>
              <div>
                <div className="text-[16px] font-extrabold text-ink">EpicPanel is ready.</div>
                <p className="mt-1 text-[13px] text-sub">Log in with the admin account you just created to start hosting.</p>
              </div>
              <button className="btn-brand w-full justify-center py-2.5" onClick={() => navigate('/login', { replace: true })}>
                Go to Login <ArrowRight size={16} />
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

const STEPS = [
  { label: 'Welcome', subtitle: 'First-time setup — a few steps and your hosting control panel is live.' },
  { label: 'Software', subtitle: 'Pick the managed software to install on this server.' },
  { label: 'Hostname', subtitle: 'Point a hostname at this server and we will verify it.' },
  { label: 'Admin', subtitle: 'Create your administrator account.' },
  { label: 'Done', subtitle: 'Setup complete.' },
]

function StepBar({ step }: { step: number }) {
  return (
    <div className="mb-6 flex items-center justify-center gap-1.5">
      {STEPS.slice(0, 4).map((s, i) => (
        <div key={s.label} className="flex items-center gap-1.5">
          <div
            className={`flex h-7 items-center gap-1.5 rounded-full px-3 text-[11.5px] font-bold transition ${
              i === step ? 'bg-brand text-white' : i < step ? 'bg-brand/10 text-brand' : 'bg-surface-2 text-muted'
            }`}
          >
            {i < step ? <Check size={12} /> : <span>{i + 1}</span>}
            {s.label}
          </div>
          {i < 3 && <div className={`h-0.5 w-4 rounded ${i < step ? 'bg-brand' : 'bg-line'}`} />}
        </div>
      ))}
    </div>
  )
}

function Num({ n }: { n: number }) {
  return <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-brand/10 text-[11px] font-black text-brand">{n}</span>
}

function Note({ tone, children }: { tone: 'ok' | 'warn'; children: React.ReactNode }) {
  const cls = tone === 'ok'
    ? 'border border-emerald-200 bg-emerald-50 text-emerald-800'
    : 'border border-[#ffe8b1] bg-warn-soft text-warn'
  return <div className={`flex items-start gap-2 rounded px-4 py-3 text-[13px] leading-relaxed ${cls}`}>{children}</div>
}
