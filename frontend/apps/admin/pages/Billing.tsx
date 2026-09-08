import { useState } from 'react'
import { Wallet, Hammer } from 'lucide-react'
import { Card, EmptyState, PageTitle } from '@epicpanel/ui'

/**
 * Billing: honest empty state. The billing engine (orders, payments,
 * invoices, subscriptions, provisioning hooks) lands with Phase 10 — this
 * screen deliberately shows no fake data.
 */
export function BillingPage() {
  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Billing" subtitle="Plans, invoices and payments." />
      <Card>
        <EmptyState
          icon={<Wallet size={22} />}
          title="Billing arrives with Phase 10"
          subtitle="Orders, payments, invoices and subscription states are built by the billing phase. Plan management lives under Plans; this screen will light up when the engine lands."
        />
      </Card>
    </div>
  )
}

/** Support: honest empty state until the support backlog fills it. */
export function SupportPage() {
  const [at] = useState(() => new Date().getFullYear())
  void at
  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Support" subtitle="Customer support queue." />
      <Card>
        <EmptyState
          icon={<Hammer size={22} />}
          title="Support tools are on the roadmap"
          subtitle="The ticket queue and assignment flow are not implemented yet. Customer questions route through your existing channels for now."
        />
      </Card>
    </div>
  )
}
