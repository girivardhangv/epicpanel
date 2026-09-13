import { useCallback, useEffect, useMemo, useState } from 'react'
import { Spinner } from '../loading'
import { Link } from 'react-router-dom'
import { Globe, Plus, Trash2, ShieldQuestion, RefreshCw, ArrowRight } from 'lucide-react'
import { api, useAuth, domainsApi } from '@epicpanel/core'
import type { Website, Domain } from '@epicpanel/core'
import { Card, EmptyState, PageTitle, RowActions, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { Modal, Field, ErrorNote, Select } from '@epicpanel/forms'

export function DomainsPage() {
  const { org } = useAuth()
  const [domains, setDomains] = useState<Domain[] | null>(null)
  const [sites, setSites] = useState<Website[]>([])
  const [kindFilter, setKindFilter] = useState('all')
  const [show, setShow] = useState(false)
  const [form, setForm] = useState({ website_id: '', domain: '', docroot_suffix: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    const [d, w] = await Promise.all([
      api.get<{ domains: Domain[] }>(`/v1/organizations/${org.id}/domains`).catch(() => ({ domains: [] as Domain[] })),
      api.get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`).catch(() => ({ websites: [] as Website[] })),
    ])
    setDomains(d.domains ?? [])
    setSites((w.websites ?? []).filter((x) => x.status !== 'deleted' && x.status !== 'deleting'))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  const siteName = (id: string) => sites.find((s) => s.id === id)?.primary_domain || sites.find((s) => s.id === id)?.name || ''

  const kindOf = (d: Domain): 'primary' | 'subdomain' | 'alias' => {
    if (d.kind === 'primary') return 'primary'
    const labels = d.domain.split('.')
    return labels.length > 2 ? 'subdomain' : 'alias'
  }

  const rows = useMemo(
    () => (domains ?? []).filter((d) => kindFilter === 'all' || kindOf(d) === kindFilter),
    [domains, kindFilter], // eslint-disable-line react-hooks/exhaustive-deps
  )

  const addDomain = async () => {
    if (!org || !form.website_id) return
    setErr('')
    setBusy(true)
    try {
      const body: { domain: string; docroot_suffix?: string } = { domain: form.domain.toLowerCase() }
      if (form.docroot_suffix.trim()) body.docroot_suffix = form.docroot_suffix.trim()
      await domainsApi.addAlias(org.id, form.website_id, body)
      setShow(false)
      setForm({ website_id: '', domain: '', docroot_suffix: '' })
      pushToast('success', 'Domain added — serving config is updating.')
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to add domain')
    } finally {
      setBusy(false)
    }
  }

  const verifyDns = async (d: Domain) => {
    if (!org) return
    try {
      await domainsApi.verifyDns(org.id, d.id)
      pushToast('success', `DNS verification queued for ${d.domain}.`)
      setTimeout(() => void load(), 1500)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Verification failed')
    }
  }

  const removeDomain = async (d: Domain) => {
    if (!org) return
    try {
      await domainsApi.remove(org.id, d.website_id, d.id)
      pushToast('success', 'Domain removed.')
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to remove domain')
    }
  }

  const columns: Column<Domain>[] = [
    {
      key: 'domain',
      header: 'Domain',
      filter: (d) => `${d.domain} ${siteName(d.website_id)}`,
      render: (d) => (
        <div>
          <div className="table-primary">{d.domain}</div>
          <div className="table-secondary">{d.kind === 'primary' ? `${siteName(d.website_id)} · primary` : siteName(d.website_id)}</div>
        </div>
      ),
    },
    {
      key: 'kind',
      header: 'Kind',
      filter: (d) => kindOf(d),
      render: (d) => <span className="status-chip">{kindOf(d) === 'primary' ? 'Primary' : kindOf(d) === 'subdomain' ? 'Subdomain' : 'Alias / addon'}</span>,
    },
    {
      key: 'ssl',
      header: 'SSL',
      filter: (d) => `${d.ssl_mode} ${d.ssl_state}`,
      render: (d) =>
        d.ssl_state === 'active' ? (
          <span className="status-chip status-live">Valid</span>
        ) : d.ssl_state === 'issuing' ? (
          <span className="status-chip status-warning">Issuing</span>
        ) : (
          <span className="status-chip">{d.ssl_mode === 'none' ? 'Off' : d.ssl_state}</span>
        ),
    },
    {
      key: 'dns',
      header: 'DNS',
      filter: (d) => String(d.dns_points_to_server ?? ''),
      render: (d) =>
        d.dns_points_to_server === true ? (
          <span className="status-chip status-live">Points here</span>
        ) : d.dns_points_to_server === false ? (
          <span className="status-chip status-warning">Not pointing</span>
        ) : (
          <span className="status-chip">Unverified</span>
        ),
    },
    {
      key: 'actions',
      header: '',
      render: (d) => (
        <RowActions>
          <button className="icon-btn" title="Verify DNS" aria-label="Verify DNS" onClick={() => void verifyDns(d)}><ShieldQuestion size={13} /></button>
          <Link to={`/websites/${d.website_id}`} className="icon-btn" title="Open website" aria-label="Open website"><ArrowRight size={13} /></Link>
          {d.kind !== 'primary' && (
            <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Remove" aria-label="Remove" onClick={() => void removeDomain(d)}>
              <Trash2 size={13} />
            </button>
          )}
        </RowActions>
      ),
    },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Domains"
        subtitle="Manage domains, subdomains and website roots without unnecessary complexity."
        actions={
          <>
            <button className="btn-ghost" onClick={() => void load()}><RefreshCw size={14} /> Refresh</button>
            <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add domain</button>
          </>
        }
      />

      <Card className="overflow-hidden !p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(d) => d.id}
          searchText={(d) => `${d.domain} ${siteName(d.website_id)} ${kindOf(d)}`}
          filterSlot={
            <select className="input w-[150px] cursor-pointer" value={kindFilter} onChange={(e) => setKindFilter(e.target.value)}>
              <option value="all">All</option>
              <option value="primary">Primary</option>
              <option value="subdomain">Subdomains</option>
              <option value="alias">Aliases</option>
            </select>
          }
          loading={domains === null}
          empty={
            <EmptyState
              icon={<Globe size={22} />}
              title="No domains yet"
              subtitle="Add a domain or subdomain and point it at any of your websites."
              action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={14} /> Add Domain</button>}
            />
          }
        />
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add Domain" subtitle="Aliases and subdomains serve an existing website.">
        <ErrorNote message={err} />
        <Field label="Website">
          <Select
            value={form.website_id}
            onChange={(v) => setForm({ ...form, website_id: v })}
            placeholder="Select a website..."
            options={sites.map((s) => ({ value: s.id, label: s.primary_domain || s.name }))}
          />
        </Field>
        <Field label="Domain" hint="e.g. shop.example.com or example.net">
          <input className="input" value={form.domain} onChange={(e) => setForm({ ...form, domain: e.target.value })} placeholder="www.example.com" />
        </Field>
        <Field label="Document root suffix (optional)" hint="Serve this domain from a subfolder, e.g. shop → /shop">
          <input className="input" value={form.docroot_suffix} onChange={(e) => setForm({ ...form, docroot_suffix: e.target.value })} placeholder="shop" />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={addDomain} disabled={busy || !form.domain || !form.website_id}>
          {busy ? (<><Spinner size={13} /> Adding…</>) : 'Add Domain'}
        </button>
      </Modal>
    </div>
  )
}
