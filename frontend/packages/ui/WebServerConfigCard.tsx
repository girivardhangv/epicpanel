import { useEffect, useState } from 'react'
import { RotateCcw, ShieldCheck, Trash2, Plus } from 'lucide-react'
import { api } from '@epicpanel/core'
import { Card, CardHeader } from './cards'

// ADR-068 — context-aware web server configuration. Structured sections are
// nginx-only (Apache/OLS keep the legacy snippet textarea). Every field is
// validated server-side; the UI never asks users to type location {}.

export interface SiteConfigDoc {
  schema?: number
  server_directives?: string
  root_location?: { try_files?: string; extra?: string }
  locations?: { id?: string; kind: string; match: string; body: string }[]
  headers?: { name: string; value: string; always?: boolean }[]
  ip_access?: { rules: { cidr: string; action: string }[]; default: string }
  error_pages?: { status: number; uri: string }[]
  client_max_body_size?: string
  asset_caching?: { classes: string[]; expires: string }
  redirects?: { from: string; to: string; code: number }[]
}

const TABS = ['Overview', 'Redirects', 'Locations', 'Headers', 'Access', 'Error Pages', 'Advanced'] as const
type Tab = (typeof TABS)[number]

const emptyConfig = (): SiteConfigDoc => ({ schema: 2 })

const PRESETS: { name: string; desc: string; apply: (c: SiteConfigDoc) => SiteConfigDoc }[] = [
  {
    name: 'WordPress',
    desc: 'Pretty permalinks via index.php',
    apply: (c) => ({ ...c, root_location: { ...c.root_location, try_files: '$uri $uri/ /index.php?$args' } }),
  },
  {
    name: 'Laravel',
    desc: 'Front-controller pattern',
    apply: (c) => ({ ...c, root_location: { ...c.root_location, try_files: '$uri $uri/ /index.php?$query_string' } }),
  },
  {
    name: 'SPA',
    desc: 'React/Vue/Angular client routing',
    apply: (c) => ({ ...c, root_location: { ...c.root_location, try_files: '$uri $uri/ /index.html' } }),
  },
  {
    name: 'Security headers',
    desc: 'XFO, nosniff, referrer, policy',
    apply: (c) => ({
      ...c,
      headers: [
        ...(c.headers ?? []).filter((h) => !['X-Frame-Options', 'X-Content-Type-Options', 'Referrer-Policy', 'Permissions-Policy'].includes(h.name)),
        { name: 'X-Frame-Options', value: 'SAMEORIGIN', always: true },
        { name: 'X-Content-Type-Options', value: 'nosniff', always: true },
        { name: 'Referrer-Policy', value: 'strict-origin-when-cross-origin' },
      ],
    }),
  },
  {
    name: 'Asset caching',
    desc: '30d expires for css/js/img/fonts',
    apply: (c) => ({ ...c, asset_caching: { classes: ['css', 'js', 'img', 'fonts'], expires: '30d' } }),
  },
  {
    name: 'Static files',
    desc: 'Plain 404 for missing files',
    apply: (c) => ({ ...c, root_location: { ...c.root_location, try_files: '$uri $uri/ =404' } }),
  },
]

const btn = 'btn-ghost !min-h-[28px] !px-2 !text-[10.5px]'
const input = 'input !min-h-[30px] !text-[11.5px]'

export function WebServerConfigCard({ orgId, websiteId, webServer }: {
  orgId: string
  websiteId: string
  webServer: string
}) {
  const [cfg, setCfg] = useState<SiteConfigDoc>(emptyConfig)
  const [version, setVersion] = useState(0)
  const [legacy, setLegacy] = useState('')
  const [hasStructured, setHasStructured] = useState(false)
  const [tab, setTab] = useState<Tab>('Overview')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [saved, setSaved] = useState('')
  const [warnings, setWarnings] = useState<string[]>([])
  const [versions, setVersions] = useState<{ version: number; created_at: string }[]>([])

  const structured = webServer === 'nginx'

  const load = async () => {
    try {
      const r = await api.get<{ rewrite_rules?: string; config?: SiteConfigDoc | null; version: number; updated_at?: string }>(
        `/v1/organizations/${orgId}/websites/${websiteId}/config`,
      )
      setVersion(r.version ?? 0)
      if (r.config && r.config.schema) {
        setCfg(r.config)
        setHasStructured(true)
      } else if (structured) {
        // Structured editor prefilled with the legacy snippet so nothing is
        // lost when the user switches over (visible migration, not silent).
        setCfg({ ...emptyConfig(), server_directives: r.rewrite_rules ?? '' })
        setLegacy(r.rewrite_rules ?? '')
        setHasStructured(false)
      } else {
        setLegacy(r.rewrite_rules ?? '')
      }
    } catch { /* first load before card mounts */ }
    try {
      const v = await api.get<{ versions: { version: number; created_at: string }[] }>(
        `/v1/organizations/${orgId}/websites/${websiteId}/config/versions`,
      )
      setVersions(v.versions ?? [])
    } catch { /* optional surface */ }
  }
  useEffect(() => { void load() }, [orgId, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setErr(''); setSaved(''); setWarnings([])
    setBusy(true)
    try {
      const r = await api.put<{ version: number }>(
        `/v1/organizations/${orgId}/websites/${websiteId}/config`,
        { config: { ...cfg, schema: 2 } },
      )
      setVersion(r.version)
      setHasStructured(true)
      setSaved(`Saved (version ${r.version}) — the web server config is being updated.`)
      setTimeout(() => setSaved(''), 4000)
      void load()
    } catch (ex: unknown) {
      setErr((ex as Error).message)
    } finally { setBusy(false) }
  }

  const validate = async () => {
    setErr(''); setSaved(''); setWarnings([])
    setBusy(true)
    try {
      const r = await api.post<{ valid: boolean; warnings: string[] }>(
        `/v1/organizations/${orgId}/websites/${websiteId}/config/validate`,
        { config: { ...cfg, schema: 2 } },
      )
      if (r.valid) setSaved('Valid — this configuration would be accepted.')
      setWarnings(r.warnings ?? [])
      setTimeout(() => setSaved(''), 4000)
    } catch (ex: unknown) {
      setErr((ex as Error).message)
    } finally { setBusy(false) }
  }

  const rollback = async (to: number) => {
    setErr(''); setSaved(''); setWarnings([])
    setBusy(true)
    try {
      const r = await api.post<{ version: number }>(
        `/v1/organizations/${orgId}/websites/${websiteId}/config/rollback`,
        { to_version: to },
      )
      setVersion(r.version)
      setSaved(`Rolled back to version ${to} (saved as version ${r.version}).`)
      setTimeout(() => setSaved(''), 4000)
      void load()
    } catch (ex: unknown) {
      setErr((ex as Error).message)
    } finally { setBusy(false) }
  }

  const saveLegacy = async () => {
    setErr(''); setSaved('')
    setBusy(true)
    try {
      await api.put(`/v1/organizations/${orgId}/websites/${websiteId}/config/rewrite`, { rewrite_rules: legacy })
      setSaved('Saved — the web server config is being updated.')
      setTimeout(() => setSaved(''), 4000)
    } catch (ex: unknown) {
      setErr((ex as Error).message)
    } finally { setBusy(false) }
  }

  const up = <K extends keyof SiteConfigDoc>(key: K, value: SiteConfigDoc[K]) => setCfg((c) => ({ ...c, [key]: value }))

  const renderLegacyEditor = () => (
    <>
      <p className="mb-3 text-[11px] leading-relaxed text-muted">
        Raw server-level directives applied to this site (rewrite rules, headers, access control…).
      </p>
      <textarea className="input min-h-[120px] font-mono text-[12px]" value={legacy} onChange={(e) => setLegacy(e.target.value)}
        placeholder={'# e.g.\nrewrite ^/old$ /new permanent;'} />
      <div className="mt-3 flex justify-end">
        <button className="btn-primary" onClick={saveLegacy} disabled={busy}>{busy ? 'Applying…' : 'Save & Apply'}</button>
      </div>
    </>
  )

  if (!structured) {
    return (
      <Card className="mb-4">
        <CardHeader className="mx-4 mt-4 !mb-0" title="Rewrite Rules & Custom Config" subtitle={`${webServer} directives applied to this site`} />
        <div className="p-4 pt-3">{renderLegacyEditor()}{notes(err, saved)}</div>
      </Card>
    )
  }

  return (
    <Card className="mb-4">
      <CardHeader
        className="mx-4 mt-4 !mb-0"
        title="Web Server Configuration"
        subtitle={`Context-aware nginx configuration — version ${version || '—'}`}
        right={
          <div className="flex gap-2">
            <button className={btn} onClick={validate} disabled={busy}><ShieldCheck size={12} className="inline" /> Validate</button>
            <button className="btn-primary !min-h-[28px] !px-3 !text-[10.5px]" onClick={save} disabled={busy}>{busy ? 'Applying…' : 'Save & Apply'}</button>
          </div>
        }
      />
      <div className="flex flex-wrap gap-1 px-4 pt-3">
        {TABS.map((t) => (
          <button key={t} onClick={() => setTab(t)}
            className={`rounded-[8px] px-2.5 py-1 text-[10.5px] font-bold transition ${tab === t ? 'bg-brand text-white' : 'bg-surface-2 text-muted hover:text-ink'}`}>
            {t}
          </button>
        ))}
      </div>
      <div className="p-4 pt-3">
        {tab === 'Overview' && (
          <>
            <p className="mb-2 text-[11px] font-bold text-ink">Presets</p>
            <p className="mb-2 text-[10.5px] text-muted">Presets fill the matching sections — they never wipe other configuration.</p>
            <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-3">
              {PRESETS.map((p) => (
                <button key={p.name} className="flex items-start gap-2 rounded-[10px] border border-line bg-surface-2 p-2.5 text-left transition hover:border-[#ced9f7] hover:bg-white"
                  onClick={() => setCfg((c) => p.apply(c))}>
                  <span className="min-w-0">
                    <strong className="block truncate text-[10.5px] text-ink">{p.name}</strong>
                    <span className="block truncate text-[8.5px] text-muted">{p.desc}</span>
                  </span>
                </button>
              ))}
            </div>
            <dl className="grid grid-cols-2 gap-2 text-[10.5px] text-muted">
              <div><dt className="font-bold text-ink">Upload cap</dt><dd>{cfg.client_max_body_size || 'server default'}</dd></div>
              <div><dt className="font-bold text-ink">Asset caching</dt><dd>{cfg.asset_caching ? `${cfg.asset_caching.expires} for ${cfg.asset_caching.classes.join(', ')}` : 'off'}</dd></div>
              <div><dt className="font-bold text-ink">Locations</dt><dd>{cfg.locations?.length ?? 0} custom</dd></div>
              <div><dt className="font-bold text-ink">Headers</dt><dd>{cfg.headers?.length ?? 0} configured</dd></div>
            </dl>
            {versions.length > 0 && (
              <>
                <p className="mb-2 mt-4 text-[11px] font-bold text-ink">History & rollback</p>
                <div className="space-y-1.5">
                  {versions.slice(0, 5).map((v) => (
                    <div key={v.version} className="flex items-center justify-between rounded-[8px] border border-line bg-surface-2 px-2.5 py-1.5 text-[10.5px]">
                      <span className="text-muted">Version {v.version}{v.version === version ? ' (current)' : ''} — {new Date(v.created_at).toLocaleString()}</span>
                      {v.version !== version && (
                        <button className={btn} onClick={() => rollback(v.version)} disabled={busy}><RotateCcw size={11} className="inline" /> Restore</button>
                      )}
                    </div>
                  ))}
                </div>
              </>
            )}
          </>
        )}

        {tab === 'Redirects' && (
          <ListEditor
            items={cfg.redirects ?? []}
            make={() => ({ from: '/', to: '/', code: 301 })}
            render={(rd, set, del) => (
              <div className="flex flex-wrap items-center gap-2">
                <input className={`${input} w-36`} value={rd.from} placeholder="/old-path or ^/blog/(.*)$" onChange={(e) => set({ ...rd, from: e.target.value })} />
                <span className="text-[10px] text-muted">→</span>
                <input className={`${input} w-44`} value={rd.to} placeholder="/new-path or https://…" onChange={(e) => set({ ...rd, to: e.target.value })} />
                <select className={`${input} w-20`} value={rd.code} onChange={(e) => set({ ...rd, code: Number(e.target.value) })}>
                  <option value={301}>301</option><option value={302}>302</option><option value={307}>307</option><option value={308}>308</option>
                </select>
                <DelBtn onClick={del} />
              </div>
            )}
            onChange={(items) => up('redirects', items)}
            hint="Path redirects. Whole-domain redirects (www ↔ non-www) live in the Domains section."
          />
        )}

        {tab === 'Locations' && (
          <ListEditor
            items={cfg.locations ?? []}
            make={() => ({ id: `loc-${Date.now()}`, kind: 'prefix', match: '/app/', body: '' })}
            render={(loc, set, del) => (
              <div className="space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <select className={`${input} w-32`} value={loc.kind} onChange={(e) => set({ ...loc, kind: e.target.value })}>
                    <option value="prefix">prefix</option><option value="exact">exact =</option>
                    <option value="regex">regex ~</option><option value="regex_nocase">regex ~*</option>
                  </select>
                  <input className={`${input} w-56 font-mono`} value={loc.match} placeholder="/api/ or \\.php$" onChange={(e) => set({ ...loc, match: e.target.value })} />
                  <DelBtn onClick={del} />
                </div>
                <textarea className="input min-h-[60px] font-mono text-[11.5px]" value={loc.body}
                  placeholder="Directives inside this location, one per line (proxy_pass http://127.0.0.1:3000, expires 30d, deny 1.2.3.4…)"
                  onChange={(e) => set({ ...loc, body: e.target.value })} />
              </div>
            )}
            onChange={(items) => up('locations', items)}
            hint="EpicPanel generates the location wrapper — enter just the matcher and the directives. location / is managed in Advanced (root location)."
          />
        )}

        {tab === 'Headers' && (
          <ListEditor
            items={cfg.headers ?? []}
            make={() => ({ name: '', value: '', always: false })}
            render={(h, set, del) => (
              <div className="flex flex-wrap items-center gap-2">
                <input className={`${input} w-52 font-mono`} value={h.name} placeholder="X-Custom-Header" onChange={(e) => set({ ...h, name: e.target.value })} />
                <input className={`${input} w-56`} value={h.value} placeholder="value" onChange={(e) => set({ ...h, value: e.target.value })} />
                <label className="flex items-center gap-1 text-[10.5px] text-muted">
                  <input type="checkbox" checked={!!h.always} onChange={(e) => set({ ...h, always: e.target.checked })} /> always
                </label>
                <DelBtn onClick={del} />
              </div>
            )}
            onChange={(items) => up('headers', items)}
            hint="Applied to every response of this site (unless an app sends the same header)."
          />
        )}

        {tab === 'Access' && (
          <>
            <ListEditor
              items={cfg.ip_access?.rules ?? []}
              make={() => ({ cidr: '', action: 'deny' })}
              render={(r, set, del) => (
                <div className="flex flex-wrap items-center gap-2">
                  <select className={`${input} w-20`} value={r.action} onChange={(e) => set({ ...r, action: e.target.value })}>
                    <option value="allow">allow</option><option value="deny">deny</option>
                  </select>
                  <input className={`${input} w-52 font-mono`} value={r.cidr} placeholder="1.2.3.4 or 10.0.0.0/8 or all" onChange={(e) => set({ ...r, cidr: e.target.value })} />
                  <DelBtn onClick={del} />
                </div>
              )}
              onChange={(items) => up('ip_access', { rules: items, default: cfg.ip_access?.default ?? 'allow' })}
              hint="Rules apply top to bottom; everything unlisted gets the default action below."
            />
            <div className="mt-2 flex items-center gap-2 text-[10.5px] text-muted">
              Default action for unlisted sources:
              <select className={`${input} w-24`} value={cfg.ip_access?.default ?? 'allow'}
                onChange={(e) => up('ip_access', { rules: cfg.ip_access?.rules ?? [], default: e.target.value })}>
                <option value="allow">allow all</option><option value="deny">deny all</option>
              </select>
            </div>
          </>
        )}

        {tab === 'Error Pages' && (
          <ListEditor
            items={cfg.error_pages ?? []}
            make={() => ({ status: 404, uri: '/404.html' })}
            render={(ep, set, del) => (
              <div className="flex flex-wrap items-center gap-2">
                <input type="number" min={300} max={599} className={`${input} w-20`} value={ep.status} onChange={(e) => set({ ...ep, status: Number(e.target.value) })} />
                <input className={`${input} w-56 font-mono`} value={ep.uri} placeholder="/404.html" onChange={(e) => set({ ...ep, uri: e.target.value })} />
                <DelBtn onClick={del} />
              </div>
            )}
            onChange={(items) => up('error_pages', items)}
            hint="Overrides the panel defaults per code. The URI must be a file inside your site (e.g. /404.html in your web root)."
          />
        )}

        {tab === 'Advanced' && (
          <>
            <div className="mb-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label className="block text-[10.5px] text-muted">
                <span className="mb-1 block font-bold text-ink">Upload cap (client_max_body_size)</span>
                <input className={`${input} w-32 font-mono`} value={cfg.client_max_body_size ?? ''} placeholder="64m"
                  onChange={(e) => up('client_max_body_size', e.target.value)} />
              </label>
              <label className="block text-[10.5px] text-muted">
                <span className="mb-1 block font-bold text-ink">Root location try_files override</span>
                <input className={`${input} w-full font-mono`} value={cfg.root_location?.try_files ?? ''} placeholder="$uri $uri/ =404"
                  onChange={(e) => up('root_location', { ...cfg.root_location, try_files: e.target.value })} />
              </label>
            </div>
            <label className="block text-[10.5px] text-muted">
              <span className="mb-1 block font-bold text-ink">Extra location / directives (raw)</span>
              <textarea className="input min-h-[100px] font-mono text-[12px]" value={cfg.server_directives ?? ''}
                placeholder={'# server-level directives, one per line\nexpires 1h\nif ($request_method = POST) {\nreturn 405\n}'}
                onChange={(e) => up('server_directives', e.target.value)} />
              <span className="mt-1 block text-[9.5px]">
                Context-aware: server-level directives only. location blocks belong in the Locations tab; the PHP handler, logs and document root are platform-managed.
              </span>
            </label>
          </>
        )}

        {warnings.length > 0 && (
          <div className="mt-3 rounded-[9px] border border-[#f5d9a0] bg-[#fff7e6] px-3 py-2 text-[11px] font-semibold text-[#92610a]">
            {warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
          </div>
        )}
        {notes(err, saved)}
      </div>
    </Card>
  )
}

function notes(err: string, saved: string) {
  return (
    <>
      {err && <div className="mt-2 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{err}</div>}
      {saved && <div className="mt-2 rounded-[9px] border border-[#cef0e1] bg-ok-soft px-3 py-2 text-[11px] font-semibold text-ok">{saved}</div>}
    </>
  )
}

function DelBtn({ onClick }: { onClick: () => void }) {
  return (
    <button className="icon-btn !h-[28px] !w-[28px] text-danger" onClick={onClick} title="Remove" aria-label="Remove">
      <Trash2 size={12} />
    </button>
  )
}

function ListEditor<T>({ items, make, render, onChange, hint }: {
  items: T[]
  make: () => T
  render: (item: T, set: (t: T) => void, del: () => void) => React.ReactNode
  onChange: (items: T[]) => void
  hint: string
}) {
  return (
    <>
      <div className="space-y-2">
        {items.map((it, i) => render(
          it,
          (next) => onChange(items.map((x, j) => (j === i ? next : x))),
          () => onChange(items.filter((_, j) => j !== i)),
        ))}
      </div>
      <button className={`${btn} mt-2`} onClick={() => onChange([...items, make()])}>
        <Plus size={12} className="inline" /> Add
      </button>
      <p className="mt-2 text-[9.5px] text-muted">{hint}</p>
    </>
  )
}
