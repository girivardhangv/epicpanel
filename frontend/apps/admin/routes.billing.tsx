import type { RouteObject } from 'react-router-dom'
import { AdminBillingPage } from './pages/billing/AdminBilling'
import { AdminInvoicesPage } from './pages/billing/AdminInvoices'
import { AdminBillingSettingsPage } from './pages/billing/AdminBillingSettings'

/**
 * Phase 10 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 * These are WHM billing screens - all routes are admin-only server-side.
 */
export const routes: RouteObject[] = [
  { path: '/billing', element: <AdminBillingPage /> },
  { path: '/billing/invoices', element: <AdminInvoicesPage /> },
  { path: '/billing/settings', element: <AdminBillingSettingsPage /> },
]
