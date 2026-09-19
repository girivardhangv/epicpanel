import { useEffect, useState, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useRoutes } from 'react-router-dom'
import {
  LayoutGrid, Server, ServerCog, Users, Globe2, Package, Network, Database, HardDrive,
  Globe, Layers, ServerOff, History, ShieldCheck, Activity, ScrollText, ListChecks,
  Wallet, LifeBuoy, Settings as SettingsIcon, CloudCog, Menu, ShieldAlert, HardDriveDownload,
} from 'lucide-react'
import { AuthProvider, useAuth, useMetrics, seedFrames, normalizeBatch } from '@epicpanel/core'
import { AppSidebar, Toaster, FreshnessBadge } from '@epicpanel/ui'
import type { SidebarGroup } from '@epicpanel/ui'
import { adminview } from './adminview'
import { RouteFallback, ScreenFallback } from './loading'

// Login is the cold path for every admin session — keep it eager so the
// guard redirect paints instantly; all WHM screens behind the guard split
// into per-route chunks (see routes.*.tsx).
import { LoginPage } from './pages/Login'
import { routes } from './routes.admin'
import { routes as billingRoutes } from './routes.billing'
import { routes as monitoringRoutes } from './routes.monitoring'

/**
 * Admin WHM navigation — the 20 verbatim tools from the master doc:
 * Dashboard | Nodes | Servers | Customers | Accounts | Plans | Domains | DNS |
 * IP Addresses | Web Servers | PHP | Databases | Backups | Security |
 * Monitoring | Logs | Jobs | Billing | Support | Settings
 */
const NAV: SidebarGroup[] = [
  { section: 'Overview', items: [{ to: '/', label: 'Dashboard', icon: LayoutGrid }] },
  {
    section: 'Fleet',
    items: [
      { to: '/nodes', label: 'Nodes', icon: Server },
      { to: '/servers', label: 'Servers', icon: ServerCog },
      { to: '/ip-addresses', label: 'IP Addresses', icon: HardDrive },
      { to: '/web-servers', label: 'Web Servers', icon: ServerOff },
      { to: '/software', label: 'Software', icon: HardDriveDownload },
    ],
  },
  {
    section: 'Hosting',
    items: [
      { to: '/customers', label: 'Customers', icon: Users },
      { to: '/accounts', label: 'Accounts', icon: Globe2 },
      { to: '/plans', label: 'Plans', icon: Package },
      { to: '/domains', label: 'Domains', icon: Network },
      { to: '/dns', label: 'DNS', icon: Globe },
      { to: '/php', label: 'PHP', icon: Layers },
      { to: '/databases', label: 'Databases', icon: Database },
    ],
  },
  {
    section: 'Operations',
    items: [
      { to: '/backups', label: 'Backups', icon: History },
      { to: '/monitoring', label: 'Monitoring', icon: Activity },
      { to: '/jobs', label: 'Jobs', icon: ListChecks },
      { to: '/logs', label: 'Logs', icon: ScrollText },
      { to: '/security', label: 'Security', icon: ShieldCheck },
    ],
  },
  {
    section: 'Platform',
    items: [
      { to: '/billing', label: 'Billing', icon: Wallet },
      { to: '/support', label: 'Support', icon: LifeBuoy },
      { to: '/settings', label: 'Settings', icon: SettingsIcon },
    ],
  },
]

const TITLES: { match: (p: string) => boolean; title: string; sub: string }[] = [
  { match: (p) => p === '/', title: 'Server overview', sub: 'Everything about your hosting platform in one place' },
  { match: (p) => p.startsWith('/nodes/'), title: 'Node', sub: 'Live host metrics and services' },
  { match: (p) => p.startsWith('/nodes'), title: 'Nodes', sub: 'Fleet health, capacity and services' },
  { match: (p) => p.startsWith('/servers'), title: 'Servers', sub: 'Enroll, maintain and retire nodes' },
  { match: (p) => p.startsWith('/customers'), title: 'Customers', sub: 'Cross-organization management' },
  { match: (p) => p.startsWith('/accounts'), title: 'Accounts', sub: 'Hosting accounts across nodes' },
  { match: (p) => p.startsWith('/plans'), title: 'Plans', sub: 'Hosting plans and resource limits' },
  { match: (p) => p.startsWith('/domains'), title: 'Domains', sub: 'Assignments, DNS and SSL status' },
  { match: (p) => p.startsWith('/dns'), title: 'DNS', sub: 'Authoritative zones across the fleet' },
  { match: (p) => p.startsWith('/ip-addresses'), title: 'IP Addresses', sub: 'Allocation and port pools' },
  { match: (p) => p.startsWith('/web-servers'), title: 'Web Servers', sub: 'Web server stack across nodes' },
  { match: (p) => p.startsWith('/php'), title: 'PHP', sub: 'PHP runtimes per node' },
  { match: (p) => p.startsWith('/databases'), title: 'Databases', sub: 'Databases across the fleet' },
  { match: (p) => p.startsWith('/backups'), title: 'Backups', sub: 'Snapshots and restore points' },
  { match: (p) => p.startsWith('/security'), title: 'Security', sub: 'Platform security posture' },
  { match: (p) => p.startsWith('/monitoring'), title: 'Monitoring', sub: 'Alerts and platform signals' },
  { match: (p) => p.startsWith('/logs'), title: 'Activity log', sub: 'Audit trail for administrative actions' },
  { match: (p) => p.startsWith('/jobs'), title: 'Jobs', sub: 'Queue, retries and dead-letter' },
  { match: (p) => p.startsWith('/billing'), title: 'Billing', sub: 'Plans, invoices and payments' },
  { match: (p) => p.startsWith('/support'), title: 'Support', sub: 'Customer support queue' },
  { match: (p) => p.startsWith('/settings'), title: 'WHM settings', sub: 'Panel configuration' },
]

function Shell() {
  const { user, logout } = useAuth()
  const [navOpen, setNavOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem('eh.admin.collapsed') === '1')
  const { frames, connected } = useMetrics()
  // Hook hoisted so its call order stays identical on every render path
  // (including the non-admin early return below).
  const routeElement = useRoutes([...monitoringRoutes, ...routes, ...billingRoutes])

  useEffect(() => {
    localStorage.setItem('eh.admin.collapsed', collapsed ? '1' : '0')
  }, [collapsed])

  // One-shot seed so the dashboard paints before the first WS frame arrives;
  // after that the live stream is the only source (never polled).
  useEffect(() => {
    if (!user?.is_platform_admin) return
    adminview
      .live()
      .then((r) => seedFrames(normalizeBatch({ metrics: r.frames })))
      .catch(() => undefined)
  }, [user?.is_platform_admin])

  const doLogout = async () => {
    await logout()
    window.location.hash = ''
  }

  // Platform-admin sessions only — authorization is also enforced
  // server-side on every /v1/adminview route; this screen is honest about it.
  if (user && !user.is_platform_admin) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-app p-4">
        <div className="card w-full max-w-[440px] p-8 text-center">
          <div className="mx-auto mb-4 grid h-11 w-11 place-items-center rounded-[12px] bg-danger-soft text-danger">
            <ShieldAlert size={20} />
          </div>
          <div className="text-[15px] font-bold text-ink">Administrator access required</div>
          <p className="mt-1.5 text-[12px] text-muted">
            The WHM is restricted to platform administrators. Signed in as {user.email}.
          </p>
          <button className="btn-ghost mt-5 w-full justify-center" onClick={doLogout}>Sign out</button>
        </div>
      </div>
    )
  }

  const frameList = Object.values(frames)
  const newest = frameList.length > 0
    ? frameList.reduce((best, f) => (new Date(f.collected_at) > new Date(best.collected_at) ? f : best), frameList[0])
    : null
  const state = newest?.node_state === 'ONLINE' ? 'LIVE' : newest?.node_state === 'STALE' ? 'STALE' : 'OFFLINE'
  const ageMs = newest ? Date.now() - new Date(newest.collected_at).getTime() : 0

  return (
    <div className="min-h-screen bg-app">
      <AppSidebar
        groups={NAV}
        account={{
          name: user?.name ?? '',
          roleLabel: 'Administrator',
          orgName: 'Platform WHM',
          onLogout: doLogout,
        }}
        open={navOpen}
        onClose={() => setNavOpen(false)}
        collapsed={collapsed}
        onToggleCollapsed={setCollapsed}
      />
      <div className={`transition-[padding] duration-200 ${collapsed ? 'lg:pl-[68px]' : 'lg:pl-[248px]'}`}>
        <AdminHeader
          fresh={{ state, ageMs }}
          wsConnected={connected}
          onMenu={() => setNavOpen(true)}
        />
        <main className="fade-up">
          <Suspense fallback={<RouteFallback />}>
            {routeElement}
          </Suspense>
        </main>
        <footer className="pb-8 pt-4 text-center text-[11px] text-muted">
          EpicHost WHM — powered by EpicPanel
        </footer>
      </div>
      <Toaster />
      {!user && <Navigate to="/login" replace />}
    </div>
  )
}

function AdminHeader({ fresh, wsConnected, onMenu }: {
  fresh: { state: 'LIVE' | 'STALE' | 'OFFLINE'; ageMs: number }
  wsConnected: boolean
  onMenu: () => void
}) {
  const { user } = useAuth()
  return (
    <header className="sticky top-0 z-30 border-b border-line bg-white/95 backdrop-blur-[12px]">
      <div className="flex h-[70px] items-center gap-3 px-[22px]">
        <button
          onClick={onMenu}
          aria-label="Open menu"
          className="grid h-[38px] w-[38px] place-items-center rounded-[9px] border border-line bg-white text-ink lg:hidden"
        >
          <Menu size={17} />
        </button>
        <div className="grid h-[34px] w-[34px] place-items-center rounded-[10px] text-white"
          style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)' }}>
          <CloudCog size={17} strokeWidth={1.8} />
        </div>
        <div className="min-w-0">
          <strong className="block text-[13px] tracking-[-.015em] text-ink">EpicHost WHM</strong>
          <span className="block text-[10px] text-muted">
            {wsConnected ? 'Live stream connected' : 'Live stream offline'} · fleet-wide administration
          </span>
        </div>
        <div className="flex-1" />
        <div className="flex cursor-pointer items-center gap-[9px] pl-[7px]">
          <span className="grid h-[34px] w-[34px] place-items-center rounded-full bg-brand-soft text-[11px] font-extrabold text-brand">
            {(user?.name ?? 'A').charAt(0).toUpperCase()}
          </span>
          <span className="hidden text-left min-[680px]:block">
            <strong className="block max-w-[140px] truncate text-[11px] text-ink">{user?.name ?? '...'}</strong>
            <span className="block text-[9px] text-muted">Administrator</span>
          </span>
        </div>
      </div>
    </header>
  )
}

function Guarded() {
  const { user, loading } = useAuth()
  if (loading) return <ScreenFallback />
  if (!user) return <Navigate to="/login" replace />
  return <Shell />
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Suspense fallback={<ScreenFallback />}>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/*" element={<Guarded />} />
          </Routes>
        </Suspense>
      </AuthProvider>
    </BrowserRouter>
  )
}
