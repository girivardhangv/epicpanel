import { useEffect, useState } from 'react'
import { Database, Plus, Trash2, KeyRound, X, ExternalLink, Loader2 } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, Toolbar, ToolbarSearch, pushToast } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import type { Database as DB, Server, Website } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

export function DatabasesPage() {
  const { org, user } = useAuth()
  const [dbs, setDbs] = useState<DB[] | null>(null)
  const [servers, setServers] = useState<Server[]>([])
  const [sites, setSites] = useState<Website[]>([])
  const [show, setShow] = useState(false)
  const [creds, setCreds] = useState<{ database: string; user: string; password: string; engine: string } | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [dropping, setDropping] = useState('')
  const [query, setQuery] = useState('')
  const [form, setForm] = useState({ name: '', engine: 'mariadb', server_id: '', website_id: '' })

  const load = async () => {
    if (!org) return
    const [d, sv, ws] = await Promise.all([
      api.get<{ databases: DB[] }>(`/v1/organizations/${org.id}/databases`),
      api.get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`).catch(() => ({ servers: [] as Server[] })),
      api.get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`).catch(() => ({ websites: [] as Website[] })),
    ])
    setDbs(d.databases ?? [])
    setServers(sv.servers ?? [])
    setSites(ws.websites ?? [])
  }

  useEffect(() => {
    void load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: form.name.toLowerCase(), engine: form.engine, server_id: form.server_id }
      if (form.website_id) body.website_id = form.website_id
      await api.post(`/v1/organizations/${org.id}/databases`, body)
      setShow(false)
      setForm({ name: '', engine: 'mariadb', server_id: '', website_id: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create database')
    } finally {
      setBusy(false)
    }
  }

  const reveal = async (db: DB) => {
    if (!org) return
    try {
      const c = await api.get<{ database: string; user: string; password: string; engine: string }>(
        `/v1/organizations/${org.id}/databases/${db.id}/credentials`,
      )
      setCreds(c)
    } catch (ex: any) {
      pushToast('error', ex.message)
    }
  }

  // One-click SSO into phpMyAdmin / Adminer — see gate page served by the panel.
  const openPma = async (db: DB) => {
    if (!org) return
    try {
      const r = await api.get<{ url: string }>(`/v1/organizations/${org.id}/databases/${db.id}/pma-sso`)
      const target = `${window.location.hostname}:8081`
      window.open(`${window.location.origin}${r.url}&h=${encodeURIComponent(target)}`, '_blank', 'noopener')
    } catch (ex: any) {
      pushToast('error', ex.message)
    }
  }

  const remove = async (db: DB) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Drop Database', message: `Drop database ${db.name}? This cannot be undone.`, confirmLabel: 'Drop Database' }))) return
    setDropping(db.id)
    try {
      await api.del(`/v1/organizations/${org.id}/databases/${db.id}`)
      await load()
    } finally {
      setDropping('')
    }
  }

  const filtered = (dbs ?? []).filter((d) => d.name.includes(query.toLowerCase()))
  const ready = (dbs ?? []).filter((d) => d.status === 'ready').length
  const engines = new Set((dbs ?? []).map((d) => d.engine))

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Databases"
        subtitle="Create and manage databases and database users."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Create database</button>}
      />

      {/* Stats */}
      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <Stat label="Databases" value={dbs === null ? '—' : `${dbs.length}`} icon={<Database size={15} strokeWidth={1.8} />} tone="purple" />
        <Stat label="Healthy" value={dbs === null ? '—' : `${ready}`} icon={<ExternalLink size={15} strokeWidth={1.8} />} tone="green" />
        <Stat label="Engines" value={dbs === null ? '—' : `${engines.size}`} icon={<X size={15} strokeWidth={1.8} />} tone="blue" />
        <Stat label="Sites attached" value={dbs === null ? '—' : `${new Set((dbs ?? []).filter((d) => d.website_id).map((d) => d.website_id)).size}`} icon={<KeyRound size={15} strokeWidth={1.8} />} tone="amber" />
      </div>

      <Card className="overflow-hidden !p-0">
        <Toolbar>
          <ToolbarSearch value={query} onChange={setQuery} placeholder="Search databases..." />
        </Toolbar>
        {dbs === null ? (
          <div className="p-5"><SkeletonRows rows={3} /></div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<Database size={22} />}
            title="No databases yet"
            subtitle="Create your first database and attach it to a site."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Create Database</button>}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[670px] border-collapse">
              <thead>
                <tr>
                  {['Database', 'Engine', 'Attached site', 'Status', ''].map((h) => (
                    <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {filtered.map((db) => {
                  const site = sites.find((w) => w.id === db.website_id)
                  return (
                    <tr key={db.id}>
                      <td className="border-b border-line px-4 py-[11px]">
                        <div className="text-[10.5px] font-bold text-[#243047]">{db.name}</div>
                        <div className="mt-0.5 font-mono text-[9px] text-muted">user: {db.db_user}</div>
                      </td>
                      <td className="border-b border-line px-4 py-[11px]">
                        <span className="status-chip">{db.engine}</span>
                      </td>
                      <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">
                        {site ? (site.primary_domain || site.name) : <span className="text-muted">—</span>}
                      </td>
                      <td className="border-b border-line px-4 py-[11px]"><StatusBadge status={db.status} /></td>
                      <td className="border-b border-line px-4 py-[11px]">
                        <div className="flex justify-end gap-[5px]">
                          {db.status === 'ready' && (
                            <>
                              {(db.engine === 'mariadb' || db.engine === 'mysql' || db.engine === 'postgres' || db.engine === 'postgresql') && (
                                <button
                                  className="icon-btn"
                                  onClick={() => void openPma(db)}
                                  title={`Open ${db.engine === 'postgres' ? 'Adminer' : 'phpMyAdmin'} (SSO)`}
                                  aria-label={`Open ${db.name} in ${db.engine === 'postgres' ? 'Adminer' : 'phpMyAdmin'} (SSO)`}
                                >
                                  <ExternalLink size={13} />
                                </button>
                              )}
                              <button className="icon-btn" onClick={() => reveal(db)} title="Reveal credentials" aria-label={`Reveal credentials for ${db.name}`}><KeyRound size={13} /></button>
                            </>
                          )}
                          {user?.is_platform_admin && (
                            <button
                              className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                              onClick={() => remove(db)}
                              disabled={dropping === db.id}
                              title="Drop database"
                              aria-label={`Drop database ${db.name}`}
                            >
                              {dropping === db.id ? <Loader2 size={13} className="animate-spin" /> : <Trash2 size={13} />}
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Create Database" subtitle="The database and user are created together.">
        <ErrorNote message={err} />
        <Field label="Label" hint="The panel derives a safe database name automatically.">
          <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="shop_db" />
        </Field>
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <Field label="Engine">
            <Select
              value={form.engine}
              onChange={(v) => setForm({ ...form, engine: v })}
              options={[
                { value: 'mariadb', label: 'MariaDB' },
                { value: 'mysql', label: 'MySQL' },
                { value: 'postgresql', label: 'PostgreSQL' },
              ]}
            />
          </Field>
          <Field label="Server">
            <Select
              value={form.server_id}
              onChange={(v) => setForm({ ...form, server_id: v })}
              placeholder="Select a server..."
              options={servers.map((s) => ({ value: s.id, label: `${s.name} (${s.status})` }))}
            />
          </Field>
          <div className="sm:col-span-2">
            <Field label="Attach to website (optional)">
              <Select
                value={form.website_id}
                onChange={(v) => setForm({ ...form, website_id: v })}
                placeholder="None"
                options={sites.map((w) => ({ value: w.id, label: w.primary_domain || w.name }))}
              />
            </Field>
          </div>
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name || !form.server_id}>
            {busy ? 'Creating...' : 'Create Database'}
          </button>
        </div>
      </Modal>

      <Modal open={!!creds} onClose={() => setCreds(null)} title="Database Credentials" subtitle="Store these now — each reveal is audited.">
        {creds && (
          <div className="space-y-2.5">
            <p className="rounded-[9px] border border-[#ffe8b1] bg-warn-soft px-3 py-2 text-[11px] font-semibold text-warn">
              The password is encrypted at rest. Anyone with panel access can reveal it — keep this window private.
            </p>
            {[
              ['Engine', creds.engine],
              ['Database', creds.database],
              ['User', creds.user],
              ['Password', creds.password],
            ].map(([k, v]) => (
              <div key={k} className="flex items-center justify-between rounded-[9px] border border-line px-3 py-2">
                <span className="text-[11px] font-bold text-[#566278]">{k}</span>
                <span className="max-w-[240px] truncate font-mono text-[11px] font-semibold text-ink">{v}</span>
              </div>
            ))}
            <div className="flex justify-end pt-1">
              <button className="btn-ghost" onClick={() => setCreds(null)}><X size={13} /> Close</button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}

function Stat({ label, value, icon, tone }: { label: string; value: React.ReactNode; icon: React.ReactNode; tone: 'blue' | 'green' | 'amber' | 'purple' }) {
  const toneCls: Record<string, string> = {
    blue: 'bg-brand-soft text-brand',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    purple: 'bg-purple-soft text-purple',
  }
  return (
    <div className="card p-[15px]">
      <div className="flex items-start justify-between gap-2.5">
        <div>
          <div className="text-[10px] font-bold uppercase tracking-[.06em] text-muted">{label}</div>
          <div className="mt-[5px] text-[24px] font-extrabold leading-[1.1] tracking-[-.04em] text-ink">{value}</div>
        </div>
        <div className={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px] ${toneCls[tone]}`}>{icon}</div>
      </div>
    </div>
  )
}
