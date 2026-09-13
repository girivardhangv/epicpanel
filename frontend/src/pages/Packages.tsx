import { useCallback, useEffect, useState } from 'react'
import { Package as PackageIcon, Plus, Trash2, Check, RefreshCw } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows } from '@/components/cards'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { fmtBytes } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'
import { pushToast } from '@/components/ref'

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

interface OrgLite {
  id: string
  name: string
  package_id?: string
}

const RUNTIME_OPTIONS = [
  { value: 'static', label: 'Static files' },
  { value: 'php', label: 'PHP (FPM + extensions)' },
  { value: 'node', label: 'Node.js' },
  { value: 'python', label: 'Python' },
  { value: 'go', label: 'Go' },
  { value: 'apache', label: 'Apache (backend)' },
  { value: 'openlitespeed', label: 'OpenLiteSpeed (backend)' },
]

const emptyForm = {
  name: '', kind: 'web', max_websites: 5, max_databases: 5, max_disk_mb: 5120,
  memory_limit_mb: 256, cpu_cores: 1,
  max_addon_domains: 3, max_subdomains: 5,
  max_bandwidth_mb: 0, io_weight: 100, max_processes: 128, max_ports: 0, max_backups: 3,
  allowed_runtimes: ['static', 'php'] as string[], price_monthly_cents: 499,
}

export function PackagesPage() {
  const { user } = useAuth()
  const [packages, setPackages] = useState<Pkg[] | null>(null)
  const [orgs, setOrgs] = useState<OrgLite[]>([])
  const [show, setShow] = useState(false)
  const [editing, setEditing] = useState<Pkg | null>(null)
  const [assigning, setAssigning] = useState<Pkg | null>(null)
  const [assignOrg, setAssignOrg] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ ...emptyForm })
  const isAdmin = !!user?.is_platform_admin

  const load = useCallback(async () => {
    const [p, o] = await Promise.all([
      api.get<{ packages: Pkg[] }>('/v1/admin/packages'),
      api.get<{ organizations: OrgLite[] }>('/v1/organizations').catch(() => ({ organizations: [] as OrgLite[] })),
    ])
    setPackages(p.packages ?? [])
    setOrgs(o.organizations ?? [])
  }, [])

  useEffect(() => {
    if (isAdmin) void load()
  }, [isAdmin, load])

  const openEdit = (p: Pkg) => {
    setEditing(p)
    setForm({
      name: p.name, kind: p.kind || 'web', max_websites: p.max_websites, max_databases: p.max_databases,
      max_disk_mb: p.max_disk_mb, memory_limit_mb: p.memory_limit_mb || 256,
      cpu_cores: p.cpu_cores || 1,
      max_addon_domains: p.max_addon_domains ?? p.max_websites,
      max_subdomains: p.max_subdomains ?? p.max_websites,
      max_bandwidth_mb: p.max_bandwidth_mb || 0,
      io_weight: p.io_weight || 0,
      max_processes: p.max_processes || 0,
      max_ports: p.max_ports || 0,
      max_backups: p.max_backups || 0,
      allowed_runtimes: p.allowed_runtimes ?? ['static', 'php'],
      price_monthly_cents: p.price_monthly_cents,
    })
    setErr('')
    setShow(true)
  }

  const toggleRuntime = (v: string) => {
    setForm((f) => ({
      ...f,
      allowed_runtimes: f.allowed_runtimes.includes(v)
        ? f.allowed_runtimes.filter((x) => x !== v)
        : [...f.allowed_runtimes, v],
    }))
  }

  const save = async () => {
    setErr('')
    setBusy(true)
    try {
      const body = {
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
        allowed_runtimes: form.allowed_runtimes,
        price_monthly_cents: Number(form.price_monthly_cents),
      }
      if (editing) await api.patch(`/v1/admin/packages/${editing.id}`, body)
      else await api.post('/v1/admin/packages', body)
      setShow(false)
      setEditing(null)
      setForm({ ...emptyForm })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (p: Pkg) => {
    if (!(await confirmAction({ title: 'Delete Package', message: `Delete package "${p.name}"?`, confirmLabel: 'Delete Package' }))) return
    try {
      await api.del(`/v1/admin/packages/${p.id}`)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message)
    }
  }

  const assign = async () => {
    if (!assigning || !assignOrg) return
    setBusy(true)
    try {
      const r = await api.post<{ sites_reconciled: number }>(`/v1/admin/organizations/${assignOrg}/package`, {
        package_id: assigning.id,
      })
      pushToast('success', `Assigned. ${r.sites_reconciled} site(s) reconciled with new limits.`)
      setAssigning(null)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
      pushToast('error', ex.message)
    } finally {
      setBusy(false)
    }
  }

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
        <h1 className="text-[23px] font-bold tracking-[-.025em] text-ink">Packages</h1>
        <Card className="mt-6">
          <EmptyState icon={<PackageIcon size={22} />} title="Administrator-only" subtitle="Hosting plans define what each customer account can use." />
        </Card>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Packages</h1>
          <p className="mt-[5px] max-w-[560px] text-[12px] text-muted">
            Build simple hosting plans: domain counts, strict memory + CPU limits (kernel-enforced) and allowed runtimes.
          </p>
        </div>
        <button className="btn-primary" onClick={() => { setEditing(null); setForm({ ...emptyForm }); setErr(''); setShow(true) }}>
          <Plus size={14} /> New package
        </button>
      </div>

      {packages === null ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 md:grid-cols-2 xl:grid-cols-3">
          {packages.map((p) => (
            <div key={p.id} className="card relative p-5">
              {p.is_default && (
                <span className="absolute right-4 top-4 rounded-full border border-[#cef0e1] bg-ok-soft px-2 py-0.5 text-[9px] font-extrabold uppercase text-ok">default</span>
              )}
              <div className="flex items-center gap-3">
                <div className="grid h-[34px] w-[34px] place-items-center rounded-[10px] bg-brand-soft text-brand">
                  <PackageIcon size={16} strokeWidth={1.8} />
                </div>
                <div>
                  <div className="text-[13px] font-bold tracking-[-.01em] text-ink">{p.name}</div>
                  <div className="text-[10px] text-muted">${(p.price_monthly_cents / 100).toFixed(2)}/mo</div>
                </div>
              </div>
              <div className="my-4 h-px bg-line" />
              <ul className="space-y-2 text-[11px] text-ink">
                <li className="flex justify-between"><span className="text-muted">Addon domains</span><span className="font-bold">{p.max_addon_domains ?? p.max_websites}</span></li>
                <li className="flex justify-between"><span className="text-muted">Subdomains</span><span className="font-bold">{p.max_subdomains ?? p.max_websites}</span></li>
                <li className="flex justify-between"><span className="text-muted">Databases</span><span className="font-bold">{p.max_databases}</span></li>
                <li className="flex justify-between"><span className="text-muted">Disk</span><span className="font-bold">{fmtBytes(p.max_disk_mb * 1024 * 1024)}</span></li>
                <li className="flex justify-between"><span className="text-muted">Memory limit</span><span className="font-bold">{p.memory_limit_mb || 256} MB <span className="text-[10.5px] font-bold text-ok">strict</span></span></li>
                <li className="flex justify-between"><span className="text-muted">CPU</span><span className="font-bold">{p.cpu_cores || 1} core(s) <span className="text-[10.5px] font-bold text-ok">strict</span></span></li>
              </ul>
              <div className="mb-4 mt-3 flex flex-wrap gap-1.5">
                {(p.allowed_runtimes ?? []).map((r) => (
                  <span key={r} className="status-chip">{r}</span>
                ))}
              </div>
              <div className="mt-5 flex items-center gap-2">
                <button className="btn-ghost flex-1 justify-center" onClick={() => openEdit(p)}><RefreshCw size={13} /> Edit</button>
                <button className="btn-brand flex-1 justify-center" onClick={() => { setAssigning(p); setErr(''); setAssignOrg(orgs[0]?.id ?? '') }}>
                  <Check size={14} /> Assign
                </button>
                {!p.is_default && (
                  <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => remove(p)} title="Delete package">
                    <Trash2 size={14} />
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create/Edit */}
      <Modal open={show} onClose={() => setShow(false)} title={editing ? `Edit ${editing.name}` : 'New Package'} width="max-w-lg">
        <ErrorNote message={err} />
        <div className="grid grid-cols-2 gap-x-4">
          <div className="col-span-2">
            <Field label="Name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Starter" /></Field>
          </div>
          <div className="col-span-2">
            <Field label="Workload kind" hint="Plans define resources only; the workload is chosen by the customer">
              <Select value={form.kind} onChange={(v) => setForm({ ...form, kind: v })} options={[
                { value: 'web', label: 'Web hosting' },
              ]} />
            </Field>
          </div>
          <Field label="Addon domains" hint="Root domains like example.com">
            <input className="input" type="number" min={0} value={form.max_addon_domains} onChange={(e) => setForm({ ...form, max_addon_domains: Number(e.target.value) })} />
          </Field>
          <Field label="Subdomains" hint="Like app.example.com">
            <input className="input" type="number" min={0} value={form.max_subdomains} onChange={(e) => setForm({ ...form, max_subdomains: Number(e.target.value) })} />
          </Field>
          <Field label="Max databases"><input className="input" type="number" value={form.max_databases} onChange={(e) => setForm({ ...form, max_databases: Number(e.target.value) })} /></Field>
          <Field label="Disk (MB)"><input className="input" type="number" value={form.max_disk_mb} onChange={(e) => setForm({ ...form, max_disk_mb: Number(e.target.value) })} /></Field>
          <Field label="Memory limit (MB)" hint="Hard cap, enforced by PHP + kernel cgroups">
            <input className="input" type="number" min={32} max={4096} value={form.memory_limit_mb} onChange={(e) => setForm({ ...form, memory_limit_mb: Number(e.target.value) })} />
          </Field>
          <Field label="CPU cores" hint="e.g. 0.5 or 1 — hard cgroup limit">
            <input className="input" type="number" min={0.1} max={16} step={0.1} value={form.cpu_cores} onChange={(e) => setForm({ ...form, cpu_cores: Number(e.target.value) })} />
          </Field>
          <Field label="Bandwidth (MB/mo)" hint="0 = unlimited"><input className="input" type="number" value={form.max_bandwidth_mb} onChange={(e) => setForm({ ...form, max_bandwidth_mb: Number(e.target.value) })} /></Field>
          <Field label="I/O weight" hint="1–10000, 0 = kernel default"><input className="input" type="number" value={form.io_weight} onChange={(e) => setForm({ ...form, io_weight: Number(e.target.value) })} /></Field>
          <Field label="Max processes"><input className="input" type="number" value={form.max_processes} onChange={(e) => setForm({ ...form, max_processes: Number(e.target.value) })} /></Field>
          <Field label="Max ports"><input className="input" type="number" value={form.max_ports} onChange={(e) => setForm({ ...form, max_ports: Number(e.target.value) })} /></Field>
          <Field label="Max backups"><input className="input" type="number" value={form.max_backups} onChange={(e) => setForm({ ...form, max_backups: Number(e.target.value) })} /></Field>
          <Field label="Price ($/mo)"><input className="input" type="number" value={form.price_monthly_cents / 100} onChange={(e) => setForm({ ...form, price_monthly_cents: Math.round(Number(e.target.value) * 100) })} /></Field>
          <div className="col-span-2">
            <Field label="Suggested software / runtimes" hint="UI hint only — packages no longer lock runtimes">
              <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
                {RUNTIME_OPTIONS.map((o) => (
                  <label key={o.value} className={`flex cursor-pointer items-center gap-2.5 rounded border p-2.5 transition ${form.allowed_runtimes.includes(o.value) ? 'border-brand bg-brand/[0.04]' : 'border-line hover:border-brand/40'}`}>
                    <input
                      type="checkbox"
                      className="accent-[color:var(--brand)]"
                      checked={form.allowed_runtimes.includes(o.value)}
                      onChange={() => toggleRuntime(o.value)}
                    />
                    <span className="text-[13px] font-semibold text-ink">{o.label}</span>
                  </label>
                ))}
              </div>
            </Field>
          </div>
        </div>
        <button className="btn-brand w-full justify-center" onClick={save} disabled={busy || !form.name}>
          {busy ? 'Saving...' : editing ? 'Save Changes' : 'Create Package'}
        </button>
      </Modal>

      {/* Assign to org */}
      <Modal open={!!assigning} onClose={() => setAssigning(null)} title={`Assign "${assigning?.name}"`}>
        <ErrorNote message={err} />
        <Field label="Organization" hint="Existing sites are reconciled: memory/CPU limits + disk quota applied by the agent.">
          <Select value={assignOrg} onChange={setAssignOrg} placeholder="Select organization..."
            options={orgs.map((o) => ({ value: o.id, label: o.name }))} />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={assign} disabled={busy || !assignOrg}>
          {busy ? 'Assigning...' : 'Assign Package'}
        </button>
      </Modal>
    </div>
  )
}
