import { lazy } from 'react'
import type { RouteObject } from 'react-router-dom'

/**
 * Phase 10 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 * Lazy on purpose — the shell's <Suspense> owns the fallback.
 */
const BillingPage = lazy(() => import('./pages/billing/Billing').then((m) => ({ default: m.BillingPage })))
const InvoicesPage = lazy(() => import('./pages/billing/Invoices').then((m) => ({ default: m.InvoicesPage })))

export const routes: RouteObject[] = [
  { path: '/billing', element: <BillingPage /> },
  { path: '/billing/invoices', element: <InvoicesPage /> },
]
