import { lazy } from 'react'
import type { RouteObject } from 'react-router-dom'

/**
 * Phase 10 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 * These are WHM billing screens - all routes are admin-only server-side.
 * Lazy on purpose — the shell's <Suspense> owns the fallback.
 */
const AdminBillingPage = lazy(() => import('./pages/billing/AdminBilling').then((m) => ({ default: m.AdminBillingPage })))
const AdminInvoicesPage = lazy(() => import('./pages/billing/AdminInvoices').then((m) => ({ default: m.AdminInvoicesPage })))
const AdminBillingSettingsPage = lazy(() => import('./pages/billing/AdminBillingSettings').then((m) => ({ default: m.AdminBillingSettingsPage })))

export const routes: RouteObject[] = [
  { path: '/billing', element: <AdminBillingPage /> },
  { path: '/billing/invoices', element: <AdminInvoicesPage /> },
  { path: '/billing/settings', element: <AdminBillingSettingsPage /> },
]
