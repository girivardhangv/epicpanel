import { useEffect, useState } from 'react'
import { Spinner } from '../loading'
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
  const [form, setForm] = useState({ name: '', primary_domain: '', runtime: 'php', runtime_version: '8.3', startup_command: '', build_command: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const appRuntime = form.runtime === 'node' || form.runtime === 'python' || form.runtime === 'go'
  const versionOptions: Record<string, { value: string; label: string }[]> = {
    php: [
      { value: '8.5', label: 'PHP 8.5' },
      { value: '8.4', label: 'PHP 8.4' },
      { value: '8.3', label: 'PHP 8.3' },
      { value: '8.2', label: 'PHP 8.2' },
    ],
    node: [
      { value: '24', label: 'Node.js 24 (LTS)' },
      { value: '22', label: 'Node.js 22 (LTS)' },
    ],
    python: [
      { value: '3.13', label: 'Python 3.13' },
      { value: '3.12', label: 'Python 3.12' },
    ],
    go: [
      { value: '1.27', label: 'Go 1.27' },
      { value: '1.26', label: 'Go 1.26' },
    ],
  }

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
      const body: Record<string, unknown> = {
        name: form.name.toLowerCase(),
        runtime: form.runtime,
        // Missing runtimes are installed on the picked server first; the site
        // provisions automatically when the install lands (job chaining).
        install_if_missing: form.runtime !== 'static',
      }
      if (form.primary_domain) body.primary_domain = form.primary_domain.toLowerCase()
      if (form.runtime !== 'static') body.runtime_version = form.runtime_version
      if (appRuntime && form.startup_command) body.startup_command = form.startup_command
      if (appRuntime && form.build_command) body.build_command = form.build_command
      await api.post(`/v1/organizations/${org.id}/websites`, body)
      setShow(false)
      setForm({ name: '', primary_domain: '', runtime: 'php', runtime_version: '8.3', startup_command: '', build_command: '' })
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
                        <Link to={`/files/${w.id}`} className="icon-btn" title="File manager" aria-label="File manager"><Folder size={13} /></Link>
                        <Link to={`/crons/${w.id}`} className="icon-btn" title="Cron jobs" aria-label="Cron jobs"><Clock size={13} /></Link>
                        <Link to={`/dns/${w.id}`} className="icon-btn" title="DNS zone" aria-label="DNS zone"><Network size={13} /></Link>
                        <Link to={`/websites/${w.id}`} className="icon-btn" title="Manage" aria-label="Manage"><ArrowRight size={13} /></Link>
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
            onChange={(v) => {
              const opts = versionOptions[v]
              setForm({ ...form, runtime: v, runtime_version: opts ? opts[0].value : '' })
            }}
            options={[
              { value: 'php', label: 'PHP' },
              { value: 'static', label: 'Static site (HTML)' },
              { value: 'node', label: 'Node.js app' },
              { value: 'python', label: 'Python app' },
              { value: 'go', label: 'Go app' },
            ]}
          />
        </Field>
        {form.runtime !== 'static' && versionOptions[form.runtime] && (
          <Field label="Version" hint="Installed automatically on the chosen server if missing.">
            <Select
              value={form.runtime_version}
              onChange={(v) => setForm({ ...form, runtime_version: v })}
              options={versionOptions[form.runtime]}
            />
          </Field>
        )}
        {appRuntime && (
          <>
            <Field label="Start command (optional)" hint="Defaults: node server.js · gunicorn/uvicorn autodetect · ./bin/app">
              <input className="input" value={form.startup_command} onChange={(e) => setForm({ ...form, startup_command: e.target.value })} placeholder="e.g. node dist/main.js" />
            </Field>
            <Field label="Build command (optional)" hint="Ran on every deploy, before the site goes live.">
              <input className="input" value={form.build_command} onChange={(e) => setForm({ ...form, build_command: e.target.value })} placeholder="e.g. npm run build" />
            </Field>
          </>
        )}
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.name}>
          {busy ? (<><Spinner size={13} /> Creating…</>) : 'Create Website'}
        </button>
      </Modal>
    </div>
  )
}
