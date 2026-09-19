import { lazy } from 'react'
import type { RouteObject } from 'react-router-dom'
import { Navigate } from 'react-router-dom'

/**
 * Phase 6 route fragment (react-router v7 contract): the coordinator mounts
 * `routes` — the 20 verbatim WHM tools. Later-wave fragments (routes.billing
 * from Phase 10, routes.monitoring from Phase 13) replace the placeholder
 * entries for /billing and /monitoring.
 * Lazy on purpose — the shell's <Suspense> in App.tsx owns the fallback;
 * the platform-admin guard is evaluated before any chunk is requested.
 */
const DashboardPage = lazy(() => import('./pages/Dashboard').then((m) => ({ default: m.DashboardPage })))
const NodesPage = lazy(() => import('./pages/Nodes').then((m) => ({ default: m.NodesPage })))
const NodeDetailPage = lazy(() => import('./pages/Nodes').then((m) => ({ default: m.NodeDetailPage })))
const ServersPage = lazy(() => import('./pages/Nodes').then((m) => ({ default: m.ServersPage })))
const CustomersPage = lazy(() => import('./pages/Customers').then((m) => ({ default: m.CustomersPage })))
const AccountsPage = lazy(() => import('./pages/Accounts').then((m) => ({ default: m.AccountsPage })))
const PlansPage = lazy(() => import('./pages/Plans').then((m) => ({ default: m.PlansPage })))
const DomainsPage = lazy(() => import('./pages/Domains').then((m) => ({ default: m.DomainsPage })))
const DnsPage = lazy(() => import('./pages/Dns').then((m) => ({ default: m.DnsPage })))
const IpAddressesPage = lazy(() => import('./pages/IpAddresses').then((m) => ({ default: m.IpAddressesPage })))
const WebServersPage = lazy(() => import('./pages/WebServers').then((m) => ({ default: m.WebServersPage })))
const PhpPage = lazy(() => import('./pages/Php').then((m) => ({ default: m.PhpPage })))
const DatabasesPage = lazy(() => import('./pages/Databases').then((m) => ({ default: m.DatabasesPage })))
const BackupsPage = lazy(() => import('./pages/Backups').then((m) => ({ default: m.BackupsPage })))
const SecurityPage = lazy(() => import('./pages/Security').then((m) => ({ default: m.SecurityPage })))
const MonitoringPage = lazy(() => import('./pages/Monitoring').then((m) => ({ default: m.MonitoringPage })))
const LogsPage = lazy(() => import('./pages/Logs').then((m) => ({ default: m.LogsPage })))
const JobsPage = lazy(() => import('./pages/Jobs').then((m) => ({ default: m.JobsPage })))
const BillingPage = lazy(() => import('./pages/Billing').then((m) => ({ default: m.BillingPage })))
const SupportPage = lazy(() => import('./pages/Billing').then((m) => ({ default: m.SupportPage })))
const SettingsPage = lazy(() => import('./pages/Settings').then((m) => ({ default: m.SettingsPage })))
const SoftwarePage = lazy(() => import('./pages/Software').then((m) => ({ default: m.SoftwarePage })))

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
  { path: '/software', element: <SoftwarePage /> },
  { path: '*', element: <Navigate to="/" replace /> },
]
