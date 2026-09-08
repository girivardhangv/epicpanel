import { useCallback, useEffect, useState } from 'react'
import { FileText, RefreshCw } from 'lucide-react'
import { api, useAuth } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, pushToast } from '@epicpanel/ui'
import { ConfirmDialog } from '@epicpanel/forms'
import { fmtMoney, type Subscription } from './Billing'

/** Wire shape of an invoice (Phase 10 API). Issued invoices are immutable. */
export interface Invoice {
  id: string
  number: string
  status: string
  currency: string
  subtotal_minor: number
  tax_minor: number
  total_minor: number
  line_items?: { description: string; quantity: number; unit_minor: number; total_minor: number }[]
  kind: string
  subscription_id?: string | null
  issued_at?: string | null
  due_at?: string | null
  paid_at?: string | null
  created_at: string
}

const statusToBadge: Record<string, string> = {
  paid: 'ok',
  open: 'warn',
  void: 'muted',
  draft: 'muted',
  refunded: 'muted',
}

export function InvoicesPage() {
  const { org } = useAuth()
  const [invoices, setInvoices] = useState<Invoice[] | null>(null)
  const [subs, setSubs] = useState<Subscription[]>([])
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<{ invoice: Invoice } | null>(null)

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ invoices: Invoice[] }>(`/v1/organizations/${org.id}/billing/invoices`)
      .then((r) => setInvoices(r.invoices ?? []))
      .catch(() => setInvoices([]))
    api
      .get<{ subscriptions: Subscription[] }>(`/v1/organizations/${org.id}/billing/subscriptions`)
      .then((r) => setSubs(r.subscriptions ?? []))
      .catch(() => setSubs([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
  }, [load])

  const payInvoice = async (invoice: Invoice) => {
    if (!org) return
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/billing/invoices/${invoice.id}/pay`, {})
      pushToast('success', `Invoice ${invoice.number} paid`)
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Payment failed')
    } finally {
      setBusy(false)
    }
  }

  const subName = (subID?: string | null) => {
    const s = subs.find((x) => x.id === subID)
    return s?.product_name ?? ''
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Invoices"
        subtitle="Issued invoices are immutable - amounts can never change"
        actions={
          <button className="btn-ghost" onClick={load} aria-label="Refresh">
            <RefreshCw size={15} /> Refresh
          </button>
        }
      />

      <Card>
        <CardHeader title="Invoice history" subtitle="Open invoices can be paid from here; renewal recovery extends the period" />
        {invoices === null ? (
          <SkeletonRows rows={2} />
        ) : invoices.length === 0 ? (
          <EmptyState
            icon={<FileText size={22} strokeWidth={1.7} />}
            title="No invoices yet"
            subtitle="Invoices appear here when a purchase or renewal is billed."
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Number</th>
                  <th className="px-4 py-2.5 font-semibold">Kind</th>
                  <th className="px-4 py-2.5 font-semibold">Status</th>
                  <th className="px-4 py-2.5 font-semibold">Total</th>
                  <th className="px-4 py-2.5 font-semibold">Issued</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {invoices.map((inv) => (
                  <tr key={inv.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{inv.number}</strong>
                      <span className="block text-[10.5px] text-muted">
                        {inv.line_items?.[0]?.description ?? subName(inv.subscription_id) ?? ''}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-sub capitalize">{inv.kind}</td>
                    <td className="px-4 py-3">
                      <StatusBadge
                        status={statusToBadge[inv.status] === 'ok' ? 'ready' : inv.status === 'open' ? 'suspended' : 'stopped'}
                      />
                      <span className="ml-2 text-[11px] text-muted">{inv.status}</span>
                    </td>
                    <td className="px-4 py-3 text-sub">{fmtMoney(inv.total_minor, inv.currency)}</td>
                    <td className="px-4 py-3 text-sub">{inv.issued_at ? new Date(inv.issued_at).toLocaleDateString() : '-'}</td>
                    <td className="px-4 py-3 text-right">
                      {inv.status === 'open' ? (
                        <button className="btn-ghost" disabled={busy} onClick={() => setConfirm({ invoice: inv })}>
                          Pay now
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

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={() => confirm && payInvoice(confirm.invoice)}
        title="Pay invoice"
        message={`Charge the saved payment method for ${confirm ? fmtMoney(confirm.invoice.total_minor, confirm.invoice.currency) : ''}?`}
        confirmLabel="Pay"
        busy={busy}
        danger={false}
      />
    </div>
  )
}
