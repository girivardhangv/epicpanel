import { useEffect, useState } from 'react'
import { Globe, Plus, Trash2, Folder, Clock, KeyRound, Terminal as TerminalIcon, Settings2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { BulkBar, BulkCheckbox, useBulkSelection } from '@epicpanel/ui'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, StatusBadge, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, Toolbar, ToolbarSearch, Initials } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { WordPressModal } from '@/components/WordPressModal'
import { confirmAction } from '@/lib/confirm'
import { timeAgo } from '@/lib/types'
import type { Website, Server } from '@/lib/types'

export function SitesPage() {
  const { org, user } = useAuth()
  const [sites, setSites] = useState<Website[] | null>(null)
  const [servers, setServers] = useState<Server[]>([])
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState('all')
  const [form, setForm] = useState({ name: '', primary_domain: '', runtime: 'static', runtime_version: '', server_id: '' })
  const [wpTarget, setWPTarget] = useState<Website | null>(null)

  const load = async () => {
    if (!org) return
    const [ws, sv] = await Promise.all([
      api.get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`),
      api.get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`).catch(() => ({ servers: [] as Server[] })),
    ])
    setSites(ws.websites ?? [])
    setServers(sv.servers ?? [])
  }

  useEffect(() => {
    void load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: form.name.toLowerCase(), runtime: form.runtime }
      if (form.primary_domain) body.primary_domain = form.primary_domain.toLowerCase()
      if (form.server_id) body.server_id = form.server_id
      if (form.runtime !== 'static') body.runtime_version = form.runtime_version
      await api.post(`/v1/organizations/${org.id}/websites`, body)
      setShow(false)
      setForm({ name: '', primary_domain: '', runtime: 'static', runtime_version: '', server_id: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create website')
    } finally {
      setBusy(false)
    }
  }

  const remove = async (w: Website) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Delete Website', message: `Delete website "${w.name}"? Its files will be removed. This cannot be undone.`, confirmLabel: 'Delete Website' }))) return
    await api.del(`/v1/organizations/${org.id}/websites/${w.id}`)
    await load()
  }

  const filtered = (sites ?? [])
    .filter((w) => statusFilter === 'all' || (statusFilter === 'staging' ? w.is_staging : w.status === statusFilter))
    .filter((w) => w.name.includes(query.toLowerCase()) || (w.primary_domain ?? '').includes(query.toLowerCase()))

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Sites"
        subtitle={`All websites in ${org?.name ?? 'your organization'}.`}
        actions={
          user?.is_platform_admin ? (
            <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Create site</button>
          ) : undefined
        }
      />

      <Card className="overflow-hidden !p-0">
        <Toolbar>
          <ToolbarSearch value={query} onChange={setQuery} placeholder="Search name or domain..." />
          <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="all">All statuses</option>
            <option value="ready">Ready</option>
            <option value="provisioning">Provisioning</option>
            <option value="failed">Failed</option>
            <option value="staging">Staging</option>
          </select>
        </Toolbar>

        {sites === null ? (
          <div className="p-5"><SkeletonRows rows={3} /></div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<Globe size={22} />}
            title="No websites yet"
            subtitle="Create your first website to get started."
            action={user?.is_platform_admin ? <button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Create Website</button> : undefined}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[670px] border-collapse">
              <thead>
                <tr>
                  {['Site', 'Runtime', 'Web server', 'Created', 'Status', ''].map((h) => (
                    <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {filtered.map((w) => (
                  <tr key={w.id} className="group">
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex items-center gap-2.5">
                        <Initials text={w.primary_domain || w.name} />
                        <div className="min-w-0">
                          <Link to={`/sites/${w.id}`} className="block truncate text-[10.5px] font-bold text-[#243047] hover:text-brand">
                            {w.primary_domain || w.name}
                          </Link>
                          <span className="block truncate text-[9px] text-muted">
                            {w.primary_domain ? w.name : `${w.name}.epichost.local`}
                            {w.is_staging && ' · staging'}
                          </span>
                        </div>
                      </div>
                    </td>
                    <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">
                      {w.runtime}{w.runtime_version ? ` ${w.runtime_version}` : ''}
                    </td>
                    <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{w.web_server}</td>
                    <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{timeAgo(w.created_at)}</td>
                    <td className="border-b border-line px-4 py-[11px]"><StatusBadge status={w.status} /></td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex justify-end gap-[5px]">
                        <Link to={`/sites/${w.id}/files`} className="icon-btn" title="File manager"><Folder size={13} /></Link>
                        <Link to={`/sites/${w.id}/crons`} className="icon-btn" title="Cron jobs"><Clock size={13} /></Link>
                        {user?.is_platform_admin && (
                          <Link to={`/sites/${w.id}/ssh-keys`} className="icon-btn" title="SSH keys"><KeyRound size={13} /></Link>
                        )}
                        {user?.is_platform_admin && w.status === 'ready' && (
                          <Link to={`/sites/${w.id}/terminal`} className="icon-btn" title="Web terminal"><TerminalIcon size={13} /></Link>
                        )}
                        {w.runtime === 'php' && user?.is_platform_admin && w.status === 'ready' && (
                          <button className="icon-btn" title="One-click WordPress" onClick={() => setWPTarget(w)}><Globe size={13} /></button>
                        )}
                        <Link to={`/sites/${w.id}`} className="icon-btn" title="Manage"><Settings2 size={13} /></Link>
                        {user?.is_platform_admin && (
                          <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="More actions" onClick={() => remove(w)}>
                            <Trash2 size={13} />
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {wpTarget && org && (
        <WordPressModal
          open={!!wpTarget}
          onClose={() => setWPTarget(null)}
          orgId={org.id}
          websiteId={wpTarget.id}
          siteName={wpTarget.name}
          domain={wpTarget.primary_domain}
          onQueued={() => void load()}
        />
      )}

      <Modal open={show} onClose={() => setShow(false)} title="Create Website" subtitle="Provision a site with a runtime and web server.">
        <ErrorNote message={err} />
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <Field label="Site name" hint="Lowercase letters, digits and dashes.">
              <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="my-project" />
            </Field>
          </div>
          <div className="sm:col-span-2">
            <Field label="Primary domain (optional)">
              <input className="input" value={form.primary_domain} onChange={(e) => setForm({ ...form, primary_domain: e.target.value })} placeholder="example.com" />
            </Field>
          </div>
          <Field label="Server" hint="Auto places on the least-loaded server.">
            <Select
              value={form.server_id}
              onChange={(v) => setForm({ ...form, server_id: v })}
              placeholder="Auto (recommended)"
              options={servers.filter((s) => s.status === 'online').map((s) => ({ value: s.id, label: `${s.name} (${s.status})` }))}
            />
          </Field>
          <Field label="Runtime">
            <Select
              value={form.runtime}
              onChange={(v) => setForm({ ...form, runtime: v, runtime_version: '' })}
              options={[
                { value: 'static', label: 'Static site (HTML)' },
                { value: 'php', label: 'PHP (choose version below)' },
              ]}
            />
          </Field>
          {form.runtime === 'php' && (
            <div className="sm:col-span-2">
              <Field label="PHP version" hint="Only versions installed on the selected server are offered.">
                <input className="input" value={form.runtime_version} onChange={(e) => setForm({ ...form, runtime_version: e.target.value })} placeholder="8.3" />
              </Field>
            </div>
          )}
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name}>
            {busy ? 'Creating...' : 'Create Website'}
          </button>
        </div>
      </Modal>
    </div>
  )
}
