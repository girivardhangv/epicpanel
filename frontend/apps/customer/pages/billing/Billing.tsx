import { useCallback, useEffect, useState } from 'react'
import { CreditCard, Plus, RefreshCw } from 'lucide-react'
import { api, useAuth, timeAgo } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, FormRow } from '@epicpanel/forms'

/** Wire shapes (Phase 10 API). Money is integer minor units + currency. */
export interface Subscription {
  id: string
  organization_id: string
  product_id?: string
  plan_id?: string
  product_name?: string
  plan_name?: string
  status: string
  provision_state: string
  billing_period: string
  period_start: string
  period_end: string
  cancel_at_period_end: boolean
  workload_kind: string
  renewal_status: string
  grace_until?: string | null
  last_error?: string
  website_id?: string
  bot_id?: string
  instance_id?: string
  created_at: string
}

export interface Product {
  id: string
  name: string
  type: string
  description: string
  plan_name?: string
  price_minor: number
  currency: string
}

export interface PaymentMethodView {
  configured?: boolean
  provider?: string
  brand?: string
  last4?: string
}

const stateTone: Record<string, string> = {
  ACTIVE: 'text-ok',
  SUSPENDED: 'text-warn',
  SUSPENDING: 'text-warn',
  TERMINATING: 'text-warn',
  TERMINATED: 'text-muted',
  FAILED: 'text-danger',
  PENDING: 'text-muted',
  PROVISIONING: 'text-brand',
}

const renewalLabel: Record<string, string> = {
  ok: 'Renews automatically',
  renewal_due: 'Renewal due',
  in_grace: 'In grace period - payment required',
  grace_ended: 'Grace ended - suspending',
  cancels_at_period_end: 'Cancels at period end',
}

export function fmtMoney(minor: number, currency: string): string {
  return `${(minor / 100).toFixed(2)} ${currency}`
}

export function BillingPage() {
  const { org } = useAuth()
  const [subs, setSubs] = useState<Subscription[] | null>(null)
  const [products, setProducts] = useState<Product[]>([])
  const [method, setMethod] = useState<PaymentMethodView>({})
  const [overview, setOverview] = useState<{ grace_days?: number; providers?: string[] }>({})
  const [showOrder, setShowOrder] = useState(false)
  const [showMethod, setShowMethod] = useState(false)
  const [orderForm, setOrderForm] = useState({ product_id: '', billing_period: 'monthly', provider: 'fake' })
  const [methodForm, setMethodForm] = useState({ provider: 'fake', token: '', brand: '', last4: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ subscriptions: Subscription[]; payment_method: PaymentMethodView; grace_days?: number; providers?: string[] }>(
        `/v1/organizations/${org.id}/billing/overview`,
      )
      .then((r) => {
        setSubs(r.subscriptions ?? [])
        setMethod(r.payment_method ?? {})
        setOverview({ grace_days: r.grace_days, providers: r.providers })
      })
      .catch((e) => {
        setSubs([])
        setErr(e.message ?? 'Failed to load billing overview')
      })
    api
      .get<{ products: Product[] }>(`/v1/organizations/${org.id}/billing/products`)
      .then((r) => setProducts(r.products ?? []))
      .catch(() => setProducts([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
  }, [load])

  const order = async () => {
    if (!org || !orderForm.product_id) return
    setErr('')
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/billing/orders`, {
        product_id: orderForm.product_id,
        billing_period: orderForm.billing_period,
        provider: orderForm.provider,
      })
      pushToast('success', 'Order created - complete the payment to provision')
      setShowOrder(false)
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Order failed')
    } finally {
      setBusy(false)
    }
  }

  const payOrder = async (orderID: string) => {
    if (!org) return
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/billing/orders/${orderID}/pay`, {})
      pushToast('success', 'Payment captured - provisioning')
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Payment failed')
    } finally {
      setBusy(false)
    }
  }

  const saveMethod = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      await api.put(`/v1/organizations/${org.id}/billing/payment-method`, methodForm)
      pushToast('success', 'Payment method saved (encrypted at rest)')
      setShowMethod(false)
      setMethodForm({ provider: methodForm.provider, token: '', brand: '', last4: '' })
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  const cancelSub = async (subID: string) => {
    if (!org) return
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/billing/subscriptions/${subID}/cancel`, {})
      pushToast('success', 'Subscription will terminate at period end')
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Cancel failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Billing"
        subtitle="Subscriptions, renewals and payment method"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-ghost" onClick={() => setShowMethod(true)}>
              <CreditCard size={15} /> Payment Method
            </button>
            <button className="btn-brand" onClick={() => setShowOrder(true)}>
              <Plus size={16} /> New Subscription
            </button>
          </>
        }
      />

      <Card>
        <CardHeader
          title="Subscriptions"
          subtitle={`Renewal window, grace period and termination status${overview.grace_days != null ? ` - grace ${overview.grace_days} day(s)` : ''}`}
        />
        {subs === null ? (
          <SkeletonRows rows={2} />
        ) : subs.length === 0 ? (
          <EmptyState
            icon={<CreditCard size={22} strokeWidth={1.7} />}
            title="No subscriptions yet"
            subtitle="Pick a product below to start a subscription; it provisions once payment is captured."
            action={<button className="btn-brand" onClick={() => setShowOrder(true)}>Browse products</button>}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Product</th>
                  <th className="px-4 py-2.5 font-semibold">Plan</th>
                  <th className="px-4 py-2.5 font-semibold">State</th>
                  <th className="px-4 py-2.5 font-semibold">Period ends</th>
                  <th className="px-4 py-2.5 font-semibold">Renewal</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {subs.map((s) => (
                  <tr key={s.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{s.product_name || 'Subscription'}</strong>
                      <span className="block text-[10.5px] text-muted">
                        {s.billing_period} - started {timeAgo(s.period_start)}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-sub">{s.plan_name || '-'}</td>
                    <td className="px-4 py-3">
                      <span className={`text-[12px] font-semibold ${stateTone[s.provision_state] ?? ''}`}>
                        {s.provision_state}
                      </span>
                      {s.last_error ? <span className="block text-[10px] text-danger">{s.last_error}</span> : null}
                    </td>
                    <td className="px-4 py-3 text-sub">{new Date(s.period_end).toLocaleDateString()}</td>
                    <td className="px-4 py-3 text-sub">{renewalLabel[s.renewal_status] ?? s.renewal_status}</td>
                    <td className="px-4 py-3 text-right">
                      {!s.cancel_at_period_end && s.provision_state === 'ACTIVE' ? (
                        <button className="btn-ghost" disabled={busy} onClick={() => cancelSub(s.id)}>
                          Cancel at period end
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <div className="mt-3.5 grid grid-cols-1 gap-3.5 xl:grid-cols-2">
        <Card>
          <CardHeader title="Available products" subtitle="Purchase starts an order; payment activates provisioning" />
          {products.length === 0 ? (
            <EmptyState title="No products published" subtitle="The operator has not published purchasable products yet." />
          ) : (
            <div className="space-y-2 px-4 pb-4">
              {products.map((p) => (
                <div key={p.id} className="flex items-center justify-between rounded-[9px] border border-line px-3 py-2.5">
                  <div className="min-w-0">
                    <strong className="block text-[12px] text-ink">{p.name}</strong>
                    <span className="block text-[10.5px] text-muted">
                      {p.type}
                      {p.plan_name ? ` - plan ${p.plan_name}` : ''}
                      {p.description ? ` - ${p.description}` : ''}
                    </span>
                  </div>
                  <span className="text-[12px] font-semibold text-ink">{fmtMoney(p.price_minor, p.currency)}</span>
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader title="Payment method" subtitle="The instrument token is stored encrypted and never displayed" />
          <div className="px-4 pb-4">
            {method.configured ? (
              <div className="rounded-[9px] border border-line px-3 py-2.5">
                <strong className="block text-[12px] text-ink capitalize">{method.provider || 'gateway'}</strong>
                <span className="block text-[10.5px] text-muted">
                  {method.brand ? `${method.brand} ` : ''}
                  {method.last4 ? `ending ${method.last4}` : 'on file'}
                </span>
              </div>
            ) : (
              <EmptyState
                title="No payment method on file"
                subtitle="Renewals charge the saved instrument; without one, failed renewals enter the grace period."
                action={<button className="btn-brand" onClick={() => setShowMethod(true)}>Add payment method</button>}
              />
            )}
          </div>
        </Card>
      </div>

      <Modal open={showOrder} onClose={() => setShowOrder(false)} title="New Subscription" subtitle="Order now, pay, then the workload provisions.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Product">
            <Select
              value={orderForm.product_id}
              onChange={(v) => setOrderForm({ ...orderForm, product_id: v })}
              options={products.map((p) => ({ value: p.id, label: `${p.name} (${fmtMoney(p.price_minor, p.currency)})` }))}
              placeholder="pick a product"
            />
          </Field>
          <Field label="Billing period">
            <Select
              value={orderForm.billing_period}
              onChange={(v) => setOrderForm({ ...orderForm, billing_period: v })}
              options={[
                { value: 'monthly', label: 'Monthly' },
                { value: 'quarterly', label: 'Quarterly' },
                { value: 'yearly', label: 'Yearly' },
              ]}
            />
          </Field>
        </FormRow>
        <Field label="Gateway">
          <Select
            value={orderForm.provider}
            onChange={(v) => setOrderForm({ ...orderForm, provider: v })}
            options={(overview.providers ?? ['fake', 'manual']).map((p) => ({ value: p, label: p }))}
          />
        </Field>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowOrder(false)}>Cancel</button>
          <button className="btn-brand" onClick={order} disabled={busy || !orderForm.product_id}>
            {busy ? 'Working…' : 'Create order'}
          </button>
        </div>
      </Modal>

      <Modal open={showMethod} onClose={() => setShowMethod(false)} title="Payment Method" subtitle="The token is encrypted at rest; only brand and last4 are ever displayed.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Gateway">
            <Select
              value={methodForm.provider}
              onChange={(v) => setMethodForm({ ...methodForm, provider: v })}
              options={(overview.providers ?? ['fake', 'manual']).map((p) => ({ value: p, label: p }))}
            />
          </Field>
          <Field label="Instrument token" hint="from your gateway">
            <input className="input" type="password" value={methodForm.token} onChange={(e) => setMethodForm({ ...methodForm, token: e.target.value })} placeholder="tok_…" />
          </Field>
        </FormRow>
        <FormRow cols={2}>
          <Field label="Brand" hint="display only">
            <input className="input" value={methodForm.brand} onChange={(e) => setMethodForm({ ...methodForm, brand: e.target.value })} placeholder="visa" />
          </Field>
          <Field label="Last 4" hint="display only">
            <input className="input" value={methodForm.last4} onChange={(e) => setMethodForm({ ...methodForm, last4: e.target.value })} placeholder="4242" />
          </Field>
        </FormRow>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowMethod(false)}>Cancel</button>
          <button className="btn-brand" onClick={saveMethod} disabled={busy || !methodForm.token}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
      </Modal>
    </div>
  )
}
