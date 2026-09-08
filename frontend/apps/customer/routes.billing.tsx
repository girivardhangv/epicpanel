import type { RouteObject } from 'react-router-dom'
import { BillingPage } from './pages/billing/Billing'
import { InvoicesPage } from './pages/billing/Invoices'

/**
 * Phase 10 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 */
export const routes: RouteObject[] = [
  { path: '/billing', element: <BillingPage /> },
  { path: '/billing/invoices', element: <InvoicesPage /> },
]
