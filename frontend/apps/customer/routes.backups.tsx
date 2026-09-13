import { lazy } from 'react'
import type { RouteObject } from 'react-router-dom'

/**
 * Phase 11 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 * /backups2/:workload_type narrows the unified list to one type
 * (account | database | website | website_files). Lazy on purpose — the
 * shell's <Suspense> owns the fallback.
 */
const Backups2Page = lazy(() => import('./pages/backups2/Backups2').then((m) => ({ default: m.Backups2Page })))
const TargetsPage = lazy(() => import('./pages/backups2/Targets').then((m) => ({ default: m.TargetsPage })))
const SchedulesPage = lazy(() => import('./pages/backups2/Schedules').then((m) => ({ default: m.SchedulesPage })))

export const routes: RouteObject[] = [
  { path: '/backups2', element: <Backups2Page /> },
  { path: '/backups2/targets', element: <TargetsPage /> },
  { path: '/backups2/schedules', element: <SchedulesPage /> },
  { path: '/backups2/:workload_type', element: <Backups2Page /> },
]
