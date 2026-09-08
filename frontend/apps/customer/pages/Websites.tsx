import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Globe, Plus, ArrowRight, Folder, Clock, Network } from 'lucide-react'
import { api, useAuth, timeAgo } from '@epicpanel/core'
import type { Website } from '@epicpanel/core'
import { Card, StatusBadge, EmptyState, SkeletonRows, PageTitle, ToolbarSearch, Initials, RowActions, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select } from '@epicpanel/forms'

export function WebsitesPage() {
  const { org, user } = useAuth()
  const [sites, setSites] = useState<Website[] | null>(null)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState('all')
  const [show, setShow] = useState(false)
  const [form, setForm] = useState({ name: '', primary_domain: '', runtime: 'php' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = () => {
    if (!org) return
    api
      .get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) => setSites((r.websites ?? []).filter((w) => w.status !== 'deleted' && w.status !== 'deleting')))
      .catch(() => setSites([]))
  }

  useEffect(() => {
    load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: form.name.toLowerCase(), runtime: form.runtime }
      if (form.primary_domain) body.primary_domain = form.primary_domain.toLowerCase()
      await api.post(`/v1/organizations/${org.id}/websites`, body)
      setShow(false)
      setForm({ name: '', primary_domain: '', runtime: 'php' })
      pushToast('success', 'Website created — provisioning is running.')
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create website')
    } finally {
      setBusy(false)
    }
  }

  const filtered = (sites ?? []).filter(
    (w) =>
      (statusFilter === 'all' || (statusFilter === 'staging' ? w.is_staging : w.status === statusFilter)) &&
      (!query || `${w.name} ${w.primary_domain}`.toLowerCase().includes(query.toLowerCase())),
  )

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Websites"
        subtitle="Create and manage websites without touching a terminal."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add website</button>}
      />

      <Card className="overflow-hidden !p-0">
        <div className="toolbar">
          <ToolbarSearch value={query} onChange={setQuery} placeholder="Search websites..." />
          <select className="input w-[150px] cursor-pointer" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="all">All</option>
            <option value="ready">Live</option>
            <option value="provisioning">Provisioning</option>
            <option value="suspended">Suspended</option>
            <option value="staging">Staging</option>
          </select>
        </div>
        {sites === null ? (
          <div className="p-5"><SkeletonRows rows={3} /></div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<Globe size={22} />}
            title="No websites yet"
            subtitle="Create your first website — files, SSL and databases are wired up for you."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Add Website</button>}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[670px] border-collapse">
              <thead>
                <tr>
                  {['Website', 'Runtime', 'Created', 'Status', ''].map((h) => (
                    <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {filtered.map((w) => (
                  <tr key={w.id}>
                    <td className="border-b border-line px-4 py-[11px]">
                      <div className="flex items-center gap-2.5">
                        <Initials text={w.primary_domain || w.name} />
                        <div className="min-w-0">
                          <Link to={`/websites/${w.id}`} className="block truncate text-[10.5px] font-bold text-[#243047] hover:text-brand">
                            {w.primary_domain || w.name}
                          </Link>
                          <span className="block truncate text-[9px] text-muted">
                            {w.runtime}{w.runtime_version ? ` ${w.runtime_version}` : ''}{w.is_staging ? ' · staging' : ''}
                          </span>
                        </div>
                      </div>
                    </td>
                    <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{w.web_server}</td>
                    <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{timeAgo(w.created_at)}</td>
                    <td className="border-b border-line px-4 py-[11px]"><StatusBadge status={w.status} /></td>
                    <td className="border-b border-line px-4 py-[11px]">
                      <RowActions>
                        <Link to={`/files/${w.id}`} className="icon-btn" title="File manager"><Folder size={13} /></Link>
                        <Link to={`/crons/${w.id}`} className="icon-btn" title="Cron jobs"><Clock size={13} /></Link>
                        <Link to={`/dns/${w.id}`} className="icon-btn" title="DNS zone"><Network size={13} /></Link>
                        <Link to={`/websites/${w.id}`} className="icon-btn" title="Manage"><ArrowRight size={13} /></Link>
                      </RowActions>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add Website" subtitle="Provision a site in under a minute.">
        <ErrorNote message={err} />
        <Field label="Site name" hint="Lowercase letters, digits and dashes.">
          <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="my-project" />
        </Field>
        <Field label="Primary domain (optional)">
          <input className="input" value={form.primary_domain} onChange={(e) => setForm({ ...form, primary_domain: e.target.value })} placeholder="example.com" />
        </Field>
        <Field label="Runtime">
          <Select
            value={form.runtime}
            onChange={(v) => setForm({ ...form, runtime: v })}
            options={[
              { value: 'php', label: 'PHP' },
              { value: 'static', label: 'Static site (HTML)' },
              { value: 'node', label: 'Node.js' },
            ]}
          />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.name}>
          {busy ? 'Creating...' : 'Create Website'}
        </button>
      </Modal>
    </div>
  )
}
