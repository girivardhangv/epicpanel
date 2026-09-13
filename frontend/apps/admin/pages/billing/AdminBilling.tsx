import { useCallback, useEffect, useState } from 'react'
import { Package, Plus, RefreshCw, Save } from 'lucide-react'
import { api, useAuth, fmtBytes } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, FormRow } from '@epicpanel/forms'


/** Wire shapes (Phase 10 admin API). Plans are the Phase 9 matrix rows. */
export interface PlanRow {
  id: string
  name: string
  kind: string
  price_minor: number
  max_websites: number
  max_databases: number
  max_disk_mb: number
  memory_limit_mb: number
  cpu_cores: number
  max_bandwidth_mb: number
  max_ports: number
  max_backups: number
  is_default: boolean
  product_count: number
}

export interface AdminProduct {
  id: string
  name: string
  type: string
  description: string
  plan_id?: string
  plan_name?: string
  plan_kind?: string
  price_minor: number
  currency: string
  active: boolean
}

export function fmtMoneyAdmin(minor: number, currency: string): string {
  return `${(minor / 100).toFixed(2)} ${currency}`
}

const kindLabel: Record<string, string> = {
  web: 'Web hosting',
}

export function AdminBillingPage() {
  const { org } = useAuth()
  const [plans, setPlans] = useState<PlanRow[] | null>(null)
  const [products, setProducts] = useState<AdminProduct[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [linking, setLinking] = useState<{ product: AdminProduct } | null>(null)
  const [linkPlan, setLinkPlan] = useState('')
  const [form, setForm] = useState({ name: '', type: 'hosting', description: '', price_minor: '', currency: 'USD', plan_id: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    api
      .get<{ plans: PlanRow[]; products: AdminProduct[] }>('/v1/admin/billing/plans')
      .then((r) => {
        setPlans(r.plans ?? [])
        setProducts(r.products ?? [])
      })
      .catch((e) => {
        setPlans([])
        setErr(e.message ?? 'Failed to load billing plans')
      })
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name: form.name,
        type: form.type,
        description: form.description,
        currency: form.currency,
      }
      if (form.price_minor !== '') body.price_minor = Number(form.price_minor)
      if (form.plan_id) body.plan_id = form.plan_id
      await api.post('/v1/admin/billing/products', body)
      pushToast('success', 'Product created')
      setShowCreate(false)
      setForm({ name: '', type: form.type, description: '', price_minor: '', currency: 'USD', plan_id: '' })
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Create failed')
    } finally {
      setBusy(false)
    }
  }

  const link = async () => {
    if (!linking) return
    setBusy(true)
    try {
      await api.patch(`/v1/admin/billing/products/${linking.product.id}`, { plan_id: linkPlan })
      pushToast('success', 'Plan linked - purchases grant it')
      setLinking(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Link failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Billing"
        subtitle="Plans, products and the link between them - purchases grant the linked plan"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-brand" onClick={() => setShowCreate(true)}>
              <Plus size={16} /> New Product
            </button>
          </>
        }
      />

      <Card>
        <CardHeader title="Plans" subtitle="The Phase 9 resource matrix - one row per plan, plans are data" />
        {plans === null ? (
          <SkeletonRows rows={2} />
        ) : plans.length === 0 ? (
          <EmptyState icon={<Package size={22} strokeWidth={1.7} />} title="No plans" subtitle="Plans come from the resource engine (hosting_packages)." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Plan</th>
                  <th className="px-4 py-2.5 font-semibold">Kind</th>
                  <th className="px-4 py-2.5 font-semibold">Monthly</th>
                  <th className="px-4 py-2.5 font-semibold">Sites / DBs</th>
                  <th className="px-4 py-2.5 font-semibold">RAM / CPU</th>
                  <th className="px-4 py-2.5 font-semibold">Disk</th>
                  <th className="px-4 py-2.5 font-semibold">Products</th>
                </tr>
              </thead>
              <tbody>
                {(plans ?? []).map((p) => (
                  <tr key={p.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{p.name}</strong>
                      {p.is_default ? <span className="ml-2 text-[10px] text-muted">default</span> : null}
                    </td>
                    <td className="px-4 py-3 text-sub">{kindLabel[p.kind] ?? p.kind}</td>
                    <td className="px-4 py-3 text-sub">{fmtMoneyAdmin(p.price_minor, 'USD')}</td>
                    <td className="px-4 py-3 text-sub">
                      {p.max_websites} / {p.max_databases}
                    </td>
                    <td className="px-4 py-3 text-sub">
                      {p.memory_limit_mb} MB / {p.cpu_cores} core{p.cpu_cores === 1 ? '' : 's'}
                    </td>
                    <td className="px-4 py-3 text-sub">{fmtBytes(p.max_disk_mb * 1024 * 1024)}</td>
                    <td className="px-4 py-3 text-sub">{p.product_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card>
        <CardHeader title="Products" subtitle="What customers buy; the linked plan is granted on purchase" />
        {products.length === 0 ? (
          <EmptyState title="No products" subtitle="Create a product and link it to a plan to sell it." action={<button className="btn-brand" onClick={() => setShowCreate(true)}>New Product</button>} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Name</th>
                  <th className="px-4 py-2.5 font-semibold">Type</th>
                  <th className="px-4 py-2.5 font-semibold">Plan</th>
                  <th className="px-4 py-2.5 font-semibold">Price</th>
                  <th className="px-4 py-2.5 font-semibold">Status</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {products.map((p) => (
                  <tr key={p.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{p.name}</strong>
                      <span className="block text-[10.5px] text-muted">{p.description}</span>
                    </td>
                    <td className="px-4 py-3 text-sub capitalize">{p.type}</td>
                    <td className="px-4 py-3 text-sub">{p.plan_name || '-'}</td>
                    <td className="px-4 py-3 text-sub">{fmtMoneyAdmin(p.price_minor, p.currency)}</td>
                    <td className="px-4 py-3">
                      <StatusBadge status={p.active ? 'ready' : 'stopped'} />
                    </td>
                    <td className="px-4 py-3 text-right">
                      <button className="btn-ghost" onClick={() => { setLinking({ product: p }); setLinkPlan(p.plan_id ?? '') }}>
                        <Save size={14} /> Link plan
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="New Product" subtitle="Purchasable item; link a plan so purchases grant it.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Name">
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Business 4GB" autoFocus />
          </Field>
          <Field label="Type">
            <Select
              value={form.type}
              onChange={(v) => setForm({ ...form, type: v })}
              options={[
                { value: 'hosting', label: 'Web hosting' },
                { value: 'service', label: 'Service (manual fulfilment)' },
              ]}
            />
          </Field>
        </FormRow>
        <FormRow cols={2}>
          <Field label="Price (minor units)" hint="integer; 1999 = 19.99">
            <input className="input" value={form.price_minor} onChange={(e) => setForm({ ...form, price_minor: e.target.value.replace(/[^0-9]/g, '') })} placeholder="1999" />
          </Field>
          <Field label="Currency">
            <input className="input" value={form.currency} onChange={(e) => setForm({ ...form, currency: e.target.value.toUpperCase().slice(0, 3) })} placeholder="USD" />
          </Field>
        </FormRow>
        <Field label="Description">
          <input className="input" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} placeholder="What the customer gets" />
        </Field>
        <Field label="Linked plan" hint="purchases switch the org to this plan">
          <Select
            value={form.plan_id}
            onChange={(v) => setForm({ ...form, plan_id: v })}
            options={(plans ?? []).map((p) => ({ value: p.id, label: `${p.name} (${kindLabel[p.kind] ?? p.kind})` }))}
            placeholder="none"
          />
        </Field>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name}>
            {busy ? 'Creating…' : 'Create product'}
          </button>
        </div>
      </Modal>

      <Modal open={!!linking} onClose={() => setLinking(null)} title="Link Plan" subtitle={`Grant plan on purchase: ${linking?.product.name ?? ''}`}>
        <ErrorNote message={err} />
        <Field label="Plan">
          <Select
            value={linkPlan}
            onChange={setLinkPlan}
            options={(plans ?? []).map((p) => ({ value: p.id, label: `${p.name} (${kindLabel[p.kind] ?? p.kind})` }))}
            placeholder="none (service product)"
          />
        </Field>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setLinking(null)}>Cancel</button>
          <button className="btn-brand" onClick={link} disabled={busy}>
            {busy ? 'Saving…' : 'Link plan'}
          </button>
        </div>
      </Modal>

    </div>
  )
}
