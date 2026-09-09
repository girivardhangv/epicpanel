import type { RouteObject } from 'react-router-dom'
import { Backups2Page } from './pages/backups2/Backups2'
import { TargetsPage } from './pages/backups2/Targets'
import { SchedulesPage } from './pages/backups2/Schedules'

/**
 * Phase 11 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 * /backups2/:workload_type narrows the unified list to one verbatim type
 * (account | database | website | minecraft_world | discord_bot |
 * full_instance | website_files).
 */
export const routes: RouteObject[] = [
  { path: '/backups2', element: <Backups2Page /> },
  { path: '/backups2/targets', element: <TargetsPage /> },
  { path: '/backups2/schedules', element: <SchedulesPage /> },
  { path: '/backups2/:workload_type', element: <Backups2Page /> },
]
