import { useCallback, useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Network, Plus, RefreshCw, Trash2, Pencil, Check, X, UploadCloud } from 'lucide-react'
import { api, ApiError, dnsApi } from '@/lib/api'
import type { DnsRecord, DnsZone } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, EmptyState, SkeletonRows } from '@/components/cards'
import { Field, ErrorNote, Select } from '@/components/ui'
import { timeAgo } from '@/lib/types'
import type { Website } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV', 'CAA']

export function DnsZonePage() {
  const { org, user } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const isAdmin = !!user?.is_platform_admin
  const [site, setSite] = useState<Website | null>(null)
  const [zone, setZone] = useState<DnsZone | null>(null)
  const [records, setRecords] = useState<DnsRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [missing, setMissing] = useState(false)
  const [err, setErr] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [createForm, setCreateForm] = useState({ domain: '', ttl: '3600' })
  const [form, setForm] = useState({ name: '', type: 'A', value: '', ttl: '3600', priority: '10' })
  const [editId, setEditId] = useState('')
  const [editForm, setEditForm] = useState({ value: '', ttl: '', priority: '' })

  const load = useCallback(async () => {
    if (!org) return
    setLoading(true)
    setErr('')
    try {
      void api
        .get<Website>(`/v1/organizations/${org.id}/websites/${websiteId}`)
        .then((ws) => setSite(ws))
        .catch(() => setSite(null))
      const res = await dnsApi.getZone(org.id, websiteId)
      setZone(res.zone ?? null)
      setRecords(res.records ?? [])
      setMissing(false)
    } catch (ex: any) {
      if (ex instanceof ApiError && ex.status === 404) {
        setMissing(true)
        setZone(null)
        setRecords([])
      } else {
        setErr(ex.message ?? 'Failed to load the DNS zone')
      }
    } finally {
      setLoading(false)
    }
  }, [org?.id, websiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  // Prefill the create form with the site's primary domain.
  useEffect(() => {
    if (site) setCreateForm((f) => (f.domain ? f : { ...f, domain: site.primary_domain }))
  }, [site])

  const createZone = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: { domain: string; ttl?: number } = { domain: createForm.domain.toLowerCase() }
      if (createForm.ttl) body.ttl = Number(createForm.ttl)
      await dnsApi.createZone(org.id, websiteId, body)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const publishZone = async () => {
    if (!org || !zone) return
    setErr('')
    setPublishing(true)
    try {
      await dnsApi.publish(org.id, zone.id)
      setNotice('Publish job enqueued — the zone file is being written on the server.')
      setTimeout(() => setNotice(''), 5000)
      setTimeout(() => void load(), 1000)
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setPublishing(false)
    }
  }

  const removeZone = async () => {
    if (!org || !zone) return
    if (!(await confirmAction({ title: 'Delete DNS Zone', message: `Delete the DNS zone for "${zone.domain}"? Its records stop resolving once the nameservers drop it.`, confirmLabel: 'Delete Zone' }))) return
    setErr('')
    try {
      await dnsApi.deleteZone(org.id, zone.id)
      setZone(null)
      setRecords([])
      setMissing(true)
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const addRecord = async () => {
    if (!org || !zone) return
    setErr('')
    setBusy(true)
    try {
      const body: { name: string; type: string; value: string; ttl?: number; priority?: number } = {
        name: form.name,
        type: form.type,
        value: form.value,
      }
      if (form.ttl) body.ttl = Number(form.ttl)
      if (form.type === 'MX' || form.type === 'SRV') body.priority = Number(form.priority || '0')
      await dnsApi.addRecord(org.id, zone.id, body)
      setForm({ name: '', type: form.type, value: '', ttl: form.ttl, priority: '10' })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const startEdit = (r: DnsRecord) => {
    setEditId(r.id)
    setEditForm({ value: r.value, ttl: String(r.ttl ?? ''), priority: r.priority != null ? String(r.priority) : '' })
  }

  const saveEdit = async (r: DnsRecord) => {
    if (!org) return
    setErr('')
    try {
      const body: { value: string; ttl?: number; priority?: number } = { value: editForm.value }
      if (editForm.ttl) body.ttl = Number(editForm.ttl)
      if (r.type === 'MX' || r.type === 'SRV') body.priority = Number(editForm.priority || '0')
      await dnsApi.updateRecord(org.id, r.id, body)
      setEditId('')
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const removeRecord = async (r: DnsRecord) => {
    if (!org) return
    if (!(await confirmAction({ title: 'Delete DNS Record', message: `Delete the ${r.type} record for "${r.name || '@'}"?`, confirmLabel: 'Delete Record' }))) return
    setErr('')
    try {
      await dnsApi.deleteRecord(org.id, r.id)
      setRecords((prev) => prev.filter((x) => x.id !== r.id))
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const needsPriority = form.type === 'MX' || form.type === 'SRV'

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <Link to={`/sites/${websiteId}`} className="icon-btn !h-[34px] !w-[34px]" title="Back"><ArrowLeft size={15} /></Link>
          <div>
            <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">DNS Zone</h1>
            <p className="mt-[5px] text-[12px] text-muted">
              {site ? `Authoritative records for ${site.primary_domain || site.name}, served by the panel nameservers.` : 'Authoritative records served by the panel nameservers.'}
            </p>
          </div>
        </div>
        {zone && (
          <div className="flex items-center gap-2">
            <button className="btn-ghost" onClick={() => void load()} disabled={loading}>
              <RefreshCw size={14} /> Refresh
            </button>
            <button className="btn-primary" onClick={() => void publishZone()} disabled={publishing}>
              <UploadCloud size={14} /> {publishing ? 'Publishing…' : 'Publish zone'}
            </button>
            {isAdmin && (
              <button
                className="icon-btn !h-[36px] !w-[36px] hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                onClick={() => void removeZone()}
                title="Delete zone"
              >
                <Trash2 size={15} />
              </button>
            )}
          </div>
        )}
      </div>

      <ErrorNote message={err} />

      {loading ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : !zone ? (
        <Card className="mx-auto max-w-[560px]">
          {missing ? (
            <EmptyState
              icon={<Network size={22} />}
              title="No DNS zone yet"
              subtitle="Create a zone to manage A, MX, TXT and other records on the panel nameservers."
            />
          ) : null}
          <CardHeader title="Create DNS zone" subtitle="The zone is created instantly; publish it whenever you change records." />
          <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
            <div className="sm:col-span-2">
              <Field label="Domain">
                <input className="input" value={createForm.domain} onChange={(e) => setCreateForm({ ...createForm, domain: e.target.value })} placeholder="example.com" />
              </Field>
            </div>
            <Field label="Default TTL (seconds)">
              <input className="input" type="number" min={60} value={createForm.ttl} onChange={(e) => setCreateForm({ ...createForm, ttl: e.target.value })} />
            </Field>
          </div>
          <div className="mt-2 flex justify-end">
            <button className="btn-brand" onClick={() => void createZone()} disabled={busy || !createForm.domain}>
              {busy ? 'Creating...' : 'Create Zone'}
            </button>
          </div>
        </Card>
      ) : (
        <>
          <Card className="mb-4">
            <CardHeader
              title={`Zone: ${zone.domain}`}
              subtitle={zone.soa_mname ? `SOA ${zone.soa_mname}${zone.soa_rname ? ` · ${zone.soa_rname}` : ''}` : 'Publish writes the zone file to the nameservers'}
              right={<span className={`status-chip ${zone.status === 'active' || zone.status === 'published' ? 'status-live' : 'status-warning'}`}>{zone.status}</span>}
            />
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <ZoneStat label="Serial" value={zone.serial != null ? String(zone.serial) : '—'} />
              <ZoneStat label="Default TTL" value={zone.ttl != null ? `${zone.ttl}s` : '—'} />
              <ZoneStat label="Updated" value={zone.updated_at ? timeAgo(zone.updated_at) : '—'} />
              <ZoneStat label="Records" value={String(records.length)} />
            </div>
            {notice && <div className="mt-3 rounded-[9px] border border-[#cef0e1] bg-ok-soft px-3 py-2 text-[11px] font-semibold text-ok">{notice}</div>}
          </Card>

          <Card className="overflow-hidden !p-0">
            <CardHeader className="mx-4 mt-4 !mb-0" title="Records" subtitle="Changes are local until you publish the zone" />
            {records.length === 0 ? (
              <EmptyState
                icon={<Network size={20} />}
                title="No records"
                subtitle="Add the first record below — for example an A record pointing at your server."
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[760px] border-collapse">
                  <thead>
                    <tr>
                      {['Name', 'Type', 'Value', 'TTL', 'Priority', ''].map((h) => (
                        <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {records.map((r) => (
                      <tr key={r.id}>
                        <td className="border-b border-line px-4 py-[11px]">
                          <span className="font-mono text-[10.5px] font-bold text-[#243047]">{r.name || '@'}</span>
                        </td>
                        <td className="border-b border-line px-4 py-[11px]"><span className="status-chip">{r.type}</span></td>
                        <td className="border-b border-line px-4 py-[11px]">
                          {editId === r.id ? (
                            <input
                              className="input h-[30px] w-[260px] font-mono text-[11px]"
                              value={editForm.value}
                              onChange={(e) => setEditForm({ ...editForm, value: e.target.value })}
                            />
                          ) : (
                            <span className="block max-w-[360px] truncate font-mono text-[10.5px] text-ink" title={r.value}>{r.value}</span>
                          )}
                        </td>
                        <td className="border-b border-line px-4 py-[11px]">
                          {editId === r.id ? (
                            <input className="input h-[30px] w-[84px]" type="number" min={60} value={editForm.ttl} onChange={(e) => setEditForm({ ...editForm, ttl: e.target.value })} />
                          ) : (
                            <span className="text-[10.5px] text-ink">{r.ttl}</span>
                          )}
                        </td>
                        <td className="border-b border-line px-4 py-[11px]">
                          {editId === r.id && (r.type === 'MX' || r.type === 'SRV') ? (
                            <input className="input h-[30px] w-[76px]" type="number" value={editForm.priority} onChange={(e) => setEditForm({ ...editForm, priority: e.target.value })} />
                          ) : (
                            <span className="text-[10.5px] text-ink">{r.priority ?? '—'}</span>
                          )}
                        </td>
                        <td className="border-b border-line px-4 py-[11px]">
                          <div className="flex justify-end gap-[5px]">
                            {editId === r.id ? (
                              <>
                                <button className="icon-btn" onClick={() => void saveEdit(r)} disabled={!editForm.value} title="Save changes"><Check size={13} /></button>
                                <button className="icon-btn" onClick={() => setEditId('')} title="Cancel"><X size={13} /></button>
                              </>
                            ) : (
                              <>
                                <button className="icon-btn" onClick={() => startEdit(r)} title="Edit record"><Pencil size={13} /></button>
                                <button
                                  className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger"
                                  onClick={() => void removeRecord(r)}
                                  title="Delete record"
                                >
                                  <Trash2 size={13} />
                                </button>
                              </>
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div className="border-t border-line bg-[#fbfcfe] px-4 py-3">
              <div className="mb-2 flex items-center gap-1.5 text-[10px] font-extrabold uppercase tracking-[.06em] text-[#566278]">
                <Plus size={12} /> Add record
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <input
                  className="input h-[34px] w-[180px] font-mono text-[11px]"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="name (@ for root)"
                />
                <div className="w-[104px]">
                  <Select
                    value={form.type}
                    onChange={(v) => setForm({ ...form, type: v })}
                    options={RECORD_TYPES.map((t) => ({ value: t, label: t }))}
                  />
                </div>
                <input
                  className="input h-[34px] min-w-[220px] flex-1 font-mono text-[11px]"
                  value={form.value}
                  onChange={(e) => setForm({ ...form, value: e.target.value })}
                  placeholder={form.type === 'MX' ? '10 mail.example.com' : form.type === 'CNAME' ? 'target.example.com' : 'value'}
                />
                <input
                  className="input h-[34px] w-[88px] text-[11px]"
                  type="number"
                  min={60}
                  value={form.ttl}
                  onChange={(e) => setForm({ ...form, ttl: e.target.value })}
                  title="TTL (seconds)"
                />
                {needsPriority && (
                  <input
                    className="input h-[34px] w-[88px] text-[11px]"
                    type="number"
                    value={form.priority}
                    onChange={(e) => setForm({ ...form, priority: e.target.value })}
                    title="Priority"
                  />
                )}
                <button
                  className="btn-primary !min-h-[32px] !px-3 !text-[11px]"
                  onClick={() => void addRecord()}
                  disabled={busy || !form.name || !form.value}
                >
                  {busy ? 'Adding…' : 'Add record'}
                </button>
              </div>
              <div className="mt-1.5 text-[10px] text-muted">
                MX and SRV records need a priority. Changes are published when you press "Publish zone".
              </div>
            </div>
          </Card>
        </>
      )}
    </div>
  )
}

function ZoneStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[10px] border border-line bg-surface-2 px-3 py-2.5">
      <div className="text-[9.5px] font-bold uppercase tracking-[.06em] text-muted">{label}</div>
      <div className="mt-[3px] truncate text-[15px] font-extrabold tracking-[-.02em] text-ink">{value}</div>
    </div>
  )
}
