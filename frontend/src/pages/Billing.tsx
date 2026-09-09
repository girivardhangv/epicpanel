import { useCallback, useEffect, useState } from 'react'
import { CreditCard, RefreshCw, Wallet, Receipt, Ban, CircleCheck } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { timeAgo } from '@/lib/types'

interface Product {
  id: string
  name: string
  type: string
  description: string
  plan_name?: string
  plan_kind?: string
  price_minor: number
  currency: string
  active: boolean
}

interface Subscription {
  id: string
  provision_state: string
  billing_period: string
  period_end: string
  cancel_at_period_end: boolean
  workload_kind: string
  grace_until?: string
}

interface Invoice {
  id: string
  number: string
  status: string
  currency: string
  total_minor: number
  kind: string
  issued_at?: string
  due_at?: string
  created_at: string
}

function money(minor: number, currency: string) {
  return `${(minor / 100).toFixed(2)} ${currency}`
}

export function BillingPage() {
  const { org, user } = useAuth()
  const [products, setProducts] = useState<Product[]>([])
  const [subs, setSubs] = useState<Subscription[]>([])
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [pm, setPm] = useState<{ brand?: string; last4?: string; configured?: boolean; provider?: string } | null>(null)
  const [providers, setProviders] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [buyProduct, setBuyProduct] = useState<Product | null>(null)
  const isAdmin = !!user?.is_platform_admin

  const load = useCallback(async () => {
    if (!org) return
    try {
      const [ov, pr, inv] = await Promise.all([
        api.get<{ subscriptions: Subscription[]; payment_method: any; providers: string[] }>(`/v1/organizations/${org.id}/billing/overview`),
        api.get<{ products: Product[] }>(`/v1/organizations/${org.id}/billing/products`),
        api.get<{ invoices: Invoice[] }>(`/v1/organizations/${org.id}/billing/invoices`),
      ])
      setSubs(ov.subscriptions ?? [])
      setPm(ov.payment_method ?? null)
      setProviders(ov.providers ?? [])
      setProducts((pr.products ?? []).filter((p) => p.active))
      setInvoices(inv.invoices ?? [])
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { load() }, [load])

  const cancelSub = async (s: Subscription) => {
    try {
      await api.post(`/v1/organizations/${org!.id}/billing/subscriptions/${s.id}/cancel`, {})
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const payInvoice = async (inv: Invoice) => {
    try {
      await api.post(`/v1/organizations/${org!.id}/billing/invoices/${inv.id}/pay`, {})
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <div className="p-6">
      <PageTitle
        title="Billing"
        subtitle="Plans, subscriptions and invoices for this organization"
        actions={<button className="btn-ghost" onClick={load}><RefreshCw size={14} /> Refresh</button>}
      />
      <ErrorNote message={err} />

      <div className="mb-6 grid gap-3 md:grid-cols-3">
        <Card>
          <CardHeader title="Payment method" right={<CreditCard size={16} className="text-brand" />} />
          <div className="px-4 pb-4 text-[12.5px] text-ink">
            {pm?.configured ? (
              <>{pm.brand ?? 'card'} •••• {pm.last4} <span className="text-muted">({pm.provider})</span></>
            ) : (
              <span className="text-muted">No payment method on file</span>
            )}
          </div>
        </Card>
        <Card>
          <CardHeader title="Active subscriptions" right={<CircleCheck size={16} className="text-brand" />} />
          <div className="px-4 pb-4 text-[12.5px] text-ink">
            {subs.filter((s) => s.provision_state === 'active').length} active of {subs.length} total
          </div>
        </Card>
        <Card>
          <CardHeader title="Open invoices" right={<Receipt size={16} className="text-brand" />} />
          <div className="px-4 pb-4 text-[12.5px] text-ink">
            {invoices.filter((i) => i.status === 'open' || i.status === 'pending').length} awaiting payment
          </div>
        </Card>
      </div>

      <h2 className="mb-2 text-[15px] font-semibold text-ink">Plans</h2>
      <div className="mb-6 grid gap-3 md:grid-cols-3">
        {products.map((p) => (
          <Card key={p.id}>
            <div className="flex flex-col gap-2 p-4">
              <div className="flex items-center justify-between">
                <span className="text-[14px] font-semibold text-ink">{p.name}</span>
                <StatusBadge status={p.type} />
              </div>
              <div className="text-[22px] font-bold text-ink">
                {money(p.price_minor, p.currency)}<span className="text-[12px] font-normal text-muted"> /mo</span>
              </div>
              {p.description && <div className="text-[12px] text-muted">{p.description}</div>}
              {p.plan_name && <div className="text-[11.5px] text-muted">Plan: {p.plan_name} ({p.plan_kind})</div>}
              <button className="btn-brand mt-1" disabled={!isAdmin} title={isAdmin ? '' : 'org admin required'}
                onClick={() => setBuyProduct(p)}>
                <Wallet size={13} /> Purchase
              </button>
            </div>
          </Card>
        ))}
        {products.length === 0 && (
          <Card><div className="p-4 text-[12.5px] text-muted">No purchasable products yet — the platform admin creates them under Admin → Billing.</div></Card>
        )}
      </div>

      <h2 className="mb-2 text-[15px] font-semibold text-ink">Subscriptions</h2>
      <div className="mb-6 grid gap-2">
        {subs.map((s) => (
          <Card key={s.id}>
            <div className="flex flex-wrap items-center gap-3 p-3.5 text-[12.5px]">
              <StatusBadge status={s.provision_state} />
              <span className="font-semibold capitalize">{s.workload_kind || 'service'}</span>
              <span className="text-muted">{s.billing_period}</span>
              <span className="text-muted">renews {timeAgo(s.period_end)}</span>
              {s.grace_until && <span className="text-danger">grace until {timeAgo(s.grace_until)}</span>}
              {s.cancel_at_period_end && <span className="text-muted">cancels at period end</span>}
              <span className="flex-1" />
              {isAdmin && !s.cancel_at_period_end && ['active', 'grace', 'past_due'].includes(s.provision_state) && (
                <button className="btn-ghost" onClick={() => cancelSub(s)}><Ban size={13} /> Cancel</button>
              )}
            </div>
          </Card>
        ))}
        {subs.length === 0 && <Card><div className="p-4 text-[12.5px] text-muted">No subscriptions yet.</div></Card>}
      </div>

      <h2 className="mb-2 text-[15px] font-semibold text-ink">Invoices</h2>
      <div className="grid gap-2">
        {invoices.map((i) => (
          <Card key={i.id}>
            <div className="flex flex-wrap items-center gap-3 p-3.5 text-[12.5px]">
              <span className="font-mono font-semibold text-ink">{i.number}</span>
              <StatusBadge status={i.status} />
              <span className="font-semibold">{money(i.total_minor, i.currency)}</span>
              <span className="text-muted capitalize">{i.kind}</span>
              <span className="flex-1" />
              <span className="text-[11px] text-muted">{timeAgo(i.created_at)}</span>
              {isAdmin && ['open', 'pending', 'past_due'].includes(i.status) && (
                <button className="btn-brand" onClick={() => payInvoice(i)}>Pay now</button>
              )}
            </div>
          </Card>
        ))}
        {invoices.length === 0 && <Card><div className="p-4 text-[12.5px] text-muted">No invoices yet.</div></Card>}
      </div>

      {buyProduct && (
        <PurchaseModal product={buyProduct} providers={providers} onClose={() => setBuyProduct(null)} onDone={() => { setBuyProduct(null); load() }} />
      )}
    </div>
  )
}

function PurchaseModal({ product, providers, onClose, onDone }: {
  product: Product
  providers: string[]
  onClose: () => void
  onDone: () => void
}) {
  const { org } = useAuth()
  const [provider, setProvider] = useState(providers[0] ?? '')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const order = await api.post<{ id: string }>(`/v1/organizations/${org!.id}/billing/orders`, {
        product_id: product.id, provider,
      })
      await api.post(`/v1/organizations/${org!.id}/billing/orders/${order.id}/pay`, {})
      onDone()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Purchase ${product.name}`} subtitle={`${money(product.price_minor, product.currency)} / month — the subscription provisions automatically after payment.`}>
      <Field label="Payment provider">
        <Select value={provider} onChange={setProvider} options={providers.map((p) => ({ value: p, label: p }))} />
      </Field>
      <ErrorNote message={err} />
      <div className="mt-4 flex justify-end gap-2">
        <button className="btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn-brand" onClick={submit} disabled={busy || !provider}>{busy ? 'Processing…' : 'Pay & subscribe'}</button>
      </div>
    </Modal>
  )
}
