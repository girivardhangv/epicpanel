import type { RouteObject } from 'react-router-dom'
import { Navigate } from 'react-router-dom'
import { DashboardPage } from './pages/Dashboard'
import { NodesPage, NodeDetailPage, ServersPage } from './pages/Nodes'

import { CustomersPage } from './pages/Customers'
import { AccountsPage } from './pages/Accounts'
import { PlansPage } from './pages/Plans'
import { DomainsPage } from './pages/Domains'
import { DnsPage } from './pages/Dns'
import { IpAddressesPage } from './pages/IpAddresses'
import { WebServersPage } from './pages/WebServers'
import { PhpPage } from './pages/Php'
import { DatabasesPage } from './pages/Databases'
import { BackupsPage } from './pages/Backups'
import { SecurityPage } from './pages/Security'
import { MonitoringPage } from './pages/Monitoring'
import { LogsPage } from './pages/Logs'
import { JobsPage } from './pages/Jobs'
import { BillingPage, SupportPage } from './pages/Billing'
import { SettingsPage } from './pages/Settings'

/**
 * Phase 6 route fragment (react-router v7 contract): the coordinator mounts
 * `routes` — the 20 verbatim WHM tools. Later-wave fragments (routes.billing
 * from Phase 10, routes.monitoring from Phase 13) replace the placeholder
 * entries for /billing and /monitoring.
 */
export const routes: RouteObject[] = [
  { path: '/', element: <DashboardPage /> },
  { path: '/nodes', element: <NodesPage /> },
  { path: '/nodes/:server_id', element: <NodeDetailPage /> },
  { path: '/servers', element: <ServersPage /> },
  { path: '/customers', element: <CustomersPage /> },
  { path: '/accounts', element: <AccountsPage /> },
  { path: '/plans', element: <PlansPage /> },
  { path: '/domains', element: <DomainsPage /> },
  { path: '/dns', element: <DnsPage /> },
  { path: '/ip-addresses', element: <IpAddressesPage /> },
  { path: '/web-servers', element: <WebServersPage /> },
  { path: '/php', element: <PhpPage /> },
  { path: '/databases', element: <DatabasesPage /> },
  { path: '/backups', element: <BackupsPage /> },
  { path: '/security', element: <SecurityPage /> },
  { path: '/monitoring', element: <MonitoringPage /> },
  { path: '/logs', element: <LogsPage /> },
  { path: '/jobs', element: <JobsPage /> },
  { path: '/billing', element: <BillingPage /> },
  { path: '/support', element: <SupportPage /> },
  { path: '/settings', element: <SettingsPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
]
