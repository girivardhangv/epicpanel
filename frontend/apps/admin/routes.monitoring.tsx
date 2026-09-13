import type { RouteObject } from 'react-router-dom'
import { MonitoringOutlet } from './pages/monitoring/Monitoring'

/**
 * Phase 13 route fragment (react-router v7 contract). Both paths render the
 * monitoring outlet: without a section it is the hub (overview tree + alert
 * feed); with a section it is the drill-down (alerts | rules | nodes |
 * services | customers — unknown values fall back to the hub). All routes are platform-admin-only server-side.
 */
export const routes: RouteObject[] = [
  { path: '/monitoring', element: <MonitoringOutlet /> },
  { path: '/monitoring/:section', element: <MonitoringOutlet /> },
]
