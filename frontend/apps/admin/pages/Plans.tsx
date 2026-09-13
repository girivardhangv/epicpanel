import { useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { Package, Plus, ShieldAlert } from 'lucide-react'
import { api, fmtBytes } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, ProgressBar, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, FormRow } from '@epicpanel/forms'
import { adminview } from '../adminview'
import type { AdminOrg } from '../adminview'

interface Pkg {
  id: string
  name: string
  kind: string
  max_websites: number
  max_databases: number
  max_disk_mb: number
  memory_limit_mb: number
  cpu_cores: number
  max_addon_domains: number
  max_subdomains: number
  allowed_runtimes: string[]
  max_bandwidth_mb: number
  io_weight: number
  max_processes: number
  max_ports: number
  max_backups: number
  price_monthly_cents: number
  is_default: boolean
}

interface AssignRow {
  pkg: Pkg
  orgs: AdminOrg[]
}

/** Plans: hosting packages (Phase 2 admin API) + assignment across customers. */
export function PlansPage() {
  const [pkgs, setPkgs] = useState<Pkg[] | null>(null)
  const [orgs, setOrgs] = useState<AdminOrg[]>([])
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({
    name: '', kind: 'web', max_websites: '10', max_databases: '10', max_disk_mb: '10240',
    memory_limit_mb: '512', cpu_cores: '1', max_addon_domains: '5', max_subdomains: '10',
    max_bandwidth_mb: '0', io_weight: '100', max_processes: '128', max_ports: '0', max_backups: '3',
    price_monthly_cents: '0', allowed_runtimes: 'static,php,node',
  })
  const [assign, setAssign] = useState<AssignRow | null>(null)
  const [assignOrg, setAssignOrg] = useState('')

  const load = async () => {
    const [p, o] = await Promise.all([
      api.get<{ packages: Pkg[] }>('/v1/admin/packages').catch(() => ({ packages: [] as Pkg[] })),
      adminview.organizations().catch(() => ({ organizations: [] as AdminOrg[] })),
    ])
    setPkgs(p.packages ?? [])
    setOrgs(o.organizations ?? [])
  }
  useEffect(() => {
    void load()
  }, [])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post('/v1/admin/packages', {
        name: form.name,
        kind: form.kind,
        max_websites: Number(form.max_websites),
        max_databases: Number(form.max_databases),
        max_disk_mb: Number(form.max_disk_mb),
        memory_limit_mb: Number(form.memory_limit_mb),
        cpu_cores: Number(form.cpu_cores),
        max_addon_domains: Number(form.max_addon_domains),
        max_subdomains: Number(form.max_subdomains),
        max_bandwidth_mb: Number(form.max_bandwidth_mb),
        io_weight: Number(form.io_weight),
        max_processes: Number(form.max_processes),
        max_ports: Number(form.max_ports),
        max_backups: Number(form.max_backups),
        allowed_runtimes: form.allowed_runtimes.split(',').map((s) => s.trim()).filter(Boolean),
        price_monthly_cents: Number(form.price_monthly_cents),
      })
      setShow(false)
      pushToast('success', `Plan ${form.name} created`)
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create plan')
    } finally {
      setBusy(false)
    }
  }

  const doAssign = async () => {
    if (!assign || !assignOrg) return
    setBusy(true)
    try {
      await api.post(`/v1/admin/organizations/${assignOrg}/package`, { package_id: assign.pkg.id })
      pushToast('success', `Plan ${assign.pkg.name} assigned`)
      setAssign(null)
      setAssignOrg('')
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Assignment failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Plans"
        subtitle="Hosting plans and the resource limits they enforce."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> New plan</button>}
      />
      {pkgs === null ? (
        <Card><SkeletonRows rows={2} /></Card>
      ) : pkgs.length === 0 ? (
        <Card><EmptyState icon={<Package size={20} />} title="No plans" subtitle="Create the first hosting plan." /></Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
          {pkgs.map((p) => {
            const using = orgs.filter((o) => o.plan === p.name)
            return (
              <Card key={p.id}>
                <CardHeader
                  title={p.name}
                  subtitle={`${p.kind} · ${p.is_default ? 'Default plan' : `$${(p.price_monthly_cents / 100).toFixed(2)}/mo`}`}
                  right={<span className="badge-ok">{using.length} active</span>}
                />
                <div className="space-y-3">
                  <Row label="Websites" value={String(p.max_websites)} pct={pct(using.reduce((a, o) => a + o.websites, 0), p.max_websites * Math.max(1, using.length))} color="#2563eb" />
                  <Row label="Databases" value={String(p.max_databases)} pct={0} color="#0f9d6e" />
                  <Row label="Disk" value={fmtBytes(p.max_disk_mb * 1024 * 1024)} pct={0} color="#7c4dff" />
                  <Row label="RAM" value={fmtBytes(p.memory_limit_mb * 1024 * 1024)} pct={0} color="#d88b00" />
                  <Row label="CPU" value={`${p.cpu_cores} core${p.cpu_cores === 1 ? '' : 's'}`} pct={0} color="#dc3d4b" />
                  {p.max_bandwidth_mb > 0 && <Row label="Bandwidth" value={fmtBytes(p.max_bandwidth_mb * 1024 * 1024)} pct={0} color="#00a3bf" />}
                  {p.max_ports > 0 && <Row label="Ports" value={String(p.max_ports)} pct={0} color="#8a63d2" />}
                  {p.max_backups > 0 && <Row label="Backups" value={String(p.max_backups)} pct={0} color="#c2410c" />}
                </div>
                <div className="mt-3 flex items-center justify-between border-t border-line pt-3">
                  <span className="text-[9px] text-muted">Resources only · runtimes are chosen per site</span>
                  <button className="btn-ghost !min-h-[28px] !px-2.5 !text-[10px]" onClick={() => setAssign({ pkg: p, orgs: using })}>Assign</button>
                </div>
              </Card>
            )
          })}
        </div>
      )}

        <Modal open={show} onClose={() => setShow(false)} title="New hosting plan" subtitle="Plans define resources only — the runtime stack is chosen per site, not locked by the plan." width="max-w-[560px]">
          <ErrorNote message={err} />
          <FormRow>
            <Field label="Plan name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Business Pro" /></Field>
            <Field label="Workload kind">
              <select className="input" value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
                <option value="web">Web hosting</option>
              </select>
            </Field>
          </FormRow>
          <FormRow>
            <Field label="Max websites">
              <input className="input" type="number" value={form.max_websites} onChange={(e) => setForm({ ...form, max_websites: e.target.value })} />
            </Field>
            <Field label="Max databases"><input className="input" type="number" value={form.max_databases} onChange={(e) => setForm({ ...form, max_databases: e.target.value })} /></Field>
          </FormRow>
        <FormRow>
          <Field label="Disk per site (MB)"><input className="input" type="number" value={form.max_disk_mb} onChange={(e) => setForm({ ...form, max_disk_mb: e.target.value })} /></Field>
          <Field label="RAM per site (MB)"><input className="input" type="number" value={form.memory_limit_mb} onChange={(e) => setForm({ ...form, memory_limit_mb: e.target.value })} /></Field>
        </FormRow>
        <FormRow>
          <Field label="CPU cores"><input className="input" type="number" step="0.25" value={form.cpu_cores} onChange={(e) => setForm({ ...form, cpu_cores: e.target.value })} /></Field>
          <Field label="Price (cents/mo)"><input className="input" type="number" value={form.price_monthly_cents} onChange={(e) => setForm({ ...form, price_monthly_cents: e.target.value })} /></Field>
        </FormRow>
        <FormRow>
          <Field label="Max addon domains"><input className="input" type="number" value={form.max_addon_domains} onChange={(e) => setForm({ ...form, max_addon_domains: e.target.value })} /></Field>
          <Field label="Max subdomains"><input className="input" type="number" value={form.max_subdomains} onChange={(e) => setForm({ ...form, max_subdomains: e.target.value })} /></Field>
        </FormRow>
        <FormRow>
          <Field label="Bandwidth (MB/mo)" hint="0 = unlimited"><input className="input" type="number" value={form.max_bandwidth_mb} onChange={(e) => setForm({ ...form, max_bandwidth_mb: e.target.value })} /></Field>
          <Field label="I/O weight" hint="1–10000, 0 = default"><input className="input" type="number" value={form.io_weight} onChange={(e) => setForm({ ...form, io_weight: e.target.value })} /></Field>
        </FormRow>
        <FormRow>
          <Field label="Max processes"><input className="input" type="number" value={form.max_processes} onChange={(e) => setForm({ ...form, max_processes: e.target.value })} /></Field>
          <Field label="Max ports"><input className="input" type="number" value={form.max_ports} onChange={(e) => setForm({ ...form, max_ports: e.target.value })} /></Field>
        </FormRow>
        <FormRow>
          <Field label="Max backups"><input className="input" type="number" value={form.max_backups} onChange={(e) => setForm({ ...form, max_backups: e.target.value })} /></Field>
          <Field label="Suggested runtimes" hint="UI hint only — no longer blocks site creation">
            <input className="input" value={form.allowed_runtimes} onChange={(e) => setForm({ ...form, allowed_runtimes: e.target.value })} />
          </Field>
        </FormRow>
        <div className="mt-3 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name}>{busy ? (<><Spinner size={13} /> Creating…</>) : 'Create plan'}</button>
        </div>
      </Modal>

      <Modal open={!!assign} onClose={() => setAssign(null)} title={`Assign ${assign?.pkg.name ?? ''}`} subtitle="Plan changes converge site limits via the resource engine.">
        <Field label="Organization">
          <select className="input" value={assignOrg} onChange={(e) => setAssignOrg(e.target.value)}>
            <option value="">Choose an organization...</option>
            {orgs.map((o) => <option key={o.id} value={o.id}>{o.name}{o.plan ? ` (currently ${o.plan})` : ''}</option>)}
          </select>
        </Field>
        <div className="mt-3 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setAssign(null)}>Cancel</button>
          <button className="btn-brand" onClick={doAssign} disabled={busy || !assignOrg}>{busy ? (<><Spinner size={13} /> Assigning…</>) : 'Assign plan'}</button>
        </div>
      </Modal>
    </div>
  )
}

function Row({ label, value, pct, color }: { label: string; value: string; pct: number; color: string }) {
  return (
    <div>
      <div className="mb-[5px] flex items-baseline justify-between gap-3">
        <strong className="text-[10.5px] text-ink">{label}</strong>
        <span className="text-[10px] font-bold text-muted">{value}</span>
      </div>
      <ProgressBar pct={Math.min(100, pct)} color={color} />
    </div>
  )
}

function pct(part: number, total: number): number {
  return total > 0 ? (part / total) * 100 : 0
}
