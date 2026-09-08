import { useCallback, useEffect, useState } from 'react'
import { FileText, Plus, RefreshCw } from 'lucide-react'
import { api, useAuth } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, FormRow, ConfirmDialog } from '@epicpanel/forms'

/** Wire shape of an admin-side invoice (Phase 10 admin API). */
export interface AdminInvoice {
  id: string
  number: string
  status: string
  currency: string
  subtotal_minor: number
  tax_minor: number
  total_minor: number
  line_items?: { description: string; quantity: number; unit_minor: number; total_minor: number }[]
  kind: string
  issued_at?: string | null
  due_at?: string | null
  paid_at?: string | null
  created_at: string
}

function money(minor: number, currency: string): string {
  return `${(minor / 100).toFixed(2)} ${currency}`
}

export function AdminInvoicesPage() {
  const { org } = useAuth()
  const [invoices, setInvoices] = useState<AdminInvoice[] | null>(null)
  const [statusFilter, setStatusFilter] = useState('')
  const [showIssue, setShowIssue] = useState(false)
  const [form, setForm] = useState({ organization_id: '', description: '', amount_minor: '', currency: 'USD' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirmVoid, setConfirmVoid] = useState<{ invoice: AdminInvoice } | null>(null)

  const load = useCallback(() => {
    const q = statusFilter ? `?status=${encodeURIComponent(statusFilter)}` : ''
    api
      .get<{ invoices: AdminInvoice[] }>(`/v1/admin/billing/invoices${q}`)
      .then((r) => setInvoices(r.invoices ?? []))
      .catch((e) => {
        setInvoices([])
        setErr(e.message ?? 'Failed to load invoices')
      })
  }, [statusFilter])

  useEffect(() => {
    load()
  }, [load])

  const issue = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post('/v1/admin/billing/invoices', {
        organization_id: form.organization_id,
        description: form.description,
        amount_minor: Number(form.amount_minor || '0'),
        currency: form.currency,
      })
      pushToast('success', 'Invoice issued (immutable from here on)')
      setShowIssue(false)
      setForm({ organization_id: '', description: '', amount_minor: '', currency: 'USD' })
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Issue failed')
    } finally {
      setBusy(false)
    }
  }

  const voidInvoice = async () => {
    if (!confirmVoid) return
    setBusy(true)
    try {
      await api.post(`/v1/admin/billing/invoices/${confirmVoid.invoice.id}/void`, {})
      pushToast('success', `Invoice ${confirmVoid.invoice.number} voided`)
      setConfirmVoid(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Void failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Invoices"
        subtitle="Issue manual invoices, void open ones - issued invoices are immutable"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-brand" onClick={() => setShowIssue(true)}>
              <Plus size={16} /> Issue Invoice
            </button>
          </>
        }
      />

      <Card>
        <CardHeader
          title="All invoices"
          subtitle="Across every organization"
          right={
            <Select
              value={statusFilter}
              onChange={setStatusFilter}
              options={[
                { value: '', label: 'All statuses' },
                { value: 'open', label: 'Open' },
                { value: 'paid', label: 'Paid' },
                { value: 'void', label: 'Void' },
              ]}
              placeholder="filter"
            />
          }
        />
        {invoices === null ? (
          <SkeletonRows rows={2} />
        ) : invoices.length === 0 ? (
          <EmptyState
            icon={<FileText size={22} strokeWidth={1.7} />}
            title="No invoices"
            subtitle="Purchase, renewal and manual invoices appear here."
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
                      <span className="block text-[10.5px] text-muted">{inv.line_items?.[0]?.description ?? ''}</span>
                    </td>
                    <td className="px-4 py-3 text-sub capitalize">{inv.kind}</td>
                    <td className="px-4 py-3">
                      <StatusBadge status={inv.status === 'paid' ? 'ready' : inv.status === 'open' ? 'pending' : 'offline'} />
                      <span className="ml-2 text-[11px] text-muted">{inv.status}</span>
                    </td>
                    <td className="px-4 py-3 text-sub">{money(inv.total_minor, inv.currency)}</td>
                    <td className="px-4 py-3 text-sub">
                      {inv.issued_at ? new Date(inv.issued_at).toLocaleDateString() : '-'}
                    </td>
                    <td className="px-4 py-3 text-right">
                      {inv.status === 'open' ? (
                        <button className="btn-ghost" onClick={() => setConfirmVoid({ invoice: inv })}>Void</button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={showIssue} onClose={() => setShowIssue(false)} title="Issue Invoice" subtitle="Manual invoice for an organization (offline payments, overages).">
        <ErrorNote message={err} />
        <Field label="Organization ID" hint="UUID of the customer organization">
          <input className="input" value={form.organization_id} onChange={(e) => setForm({ ...form, organization_id: e.target.value })} placeholder="00000000-…" />
        </Field>
        <Field label="Description">
          <input className="input" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} placeholder="Overage - September bandwidth" />
        </Field>
        <FormRow cols={2}>
          <Field label="Amount (minor units)" hint="integer; 1999 = 19.99">
            <input className="input" value={form.amount_minor} onChange={(e) => setForm({ ...form, amount_minor: e.target.value.replace(/[^0-9]/g, '') })} placeholder="1999" />
          </Field>
          <Field label="Currency">
            <input className="input" value={form.currency} onChange={(e) => setForm({ ...form, currency: e.target.value.toUpperCase().slice(0, 3) })} placeholder="USD" />
          </Field>
        </FormRow>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowIssue(false)}>Cancel</button>
          <button className="btn-brand" onClick={issue} disabled={busy || !form.organization_id}>
            {busy ? 'Issuing…' : 'Issue'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirmVoid}
        onClose={() => setConfirmVoid(null)}
        onConfirm={voidInvoice}
        title="Void invoice"
        message={`Void ${confirmVoid?.invoice.number ?? ''}? Only open invoices can be voided; issued amounts never change.`}
        confirmLabel="Void"
        busy={busy}
      />
    </div>
  )
}
