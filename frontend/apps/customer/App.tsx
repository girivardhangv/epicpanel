import { useEffect, useMemo, useState, lazy, Suspense, ReactNode } from 'react'
import { BrowserRouter, Routes, Route, Navigate, Link, useLocation, useNavigate } from 'react-router-dom'
import {
  LayoutGrid, Globe, FolderOpen, Database, History, Lock, UserRound,
  Server as ServerIcon, Network, Clock, KeyRound, Menu, Bell, ShieldCheck,
  CreditCard, Receipt,
} from 'lucide-react'
import { AuthProvider, useAuth, roleLabel, api, ROLE_RANK } from '@epicpanel/core'
import type { Organization, Alert } from '@epicpanel/core'
import { useMetrics, useFreshness } from '@epicpanel/core'
import { AppSidebar, Toaster, FreshnessBadge, CommandPalette, useCommandPaletteHotkey } from '@epicpanel/ui'
import type { SidebarGroup, CommandResultsProvider } from '@epicpanel/ui'
import { Field, ErrorNote } from '@epicpanel/forms'
import { RouteFallback, ScreenFallback, Spinner } from './loading'
import { LoginPage, RegisterPage } from './pages/Auth'
import { routes as billingRoutes } from './routes.billing'
import { routes as backupsRoutes } from './routes.backups'

// Route-level code splitting: every screen after the auth gate loads as its
// own chunk. Guards stay eager (App/Shell/AuthProvider below), so redirects
// never wait on a chunk download; only page bodies suspend.
const DashboardPage = lazy(() => import('./pages/Dashboard').then((m) => ({ default: m.DashboardPage })))
const WebsitesPage = lazy(() => import('./pages/Websites').then((m) => ({ default: m.WebsitesPage })))
const DomainsPage = lazy(() => import('./pages/Domains').then((m) => ({ default: m.DomainsPage })))
const DnsZonePage = lazy(() => import('./pages/DnsZone').then((m) => ({ default: m.DnsZonePage })))
const FileManagerPage = lazy(() => import('./pages/FileManager').then((m) => ({ default: m.FileManagerPage })))
const FtpPage = lazy(() => import('./pages/Ftp').then((m) => ({ default: m.FtpPage })))
const DatabasesPage = lazy(() => import('./pages/Databases').then((m) => ({ default: m.DatabasesPage })))
const PhpPage = lazy(() => import('./pages/Php').then((m) => ({ default: m.PhpPage })))
const CronJobsPage = lazy(() => import('./pages/CronJobs').then((m) => ({ default: m.CronJobsPage })))
const BackupsPage = lazy(() => import('./pages/Backups').then((m) => ({ default: m.BackupsPage })))
const MetricsPage = lazy(() => import('./pages/Metrics').then((m) => ({ default: m.MetricsPage })))
const SslPage = lazy(() => import('./pages/Ssl').then((m) => ({ default: m.SslPage })))
const SecurityPage = lazy(() => import('./pages/Security').then((m) => ({ default: m.SecurityPage })))
const AccountPage = lazy(() => import('./pages/Account').then((m) => ({ default: m.AccountPage })))

/**
 * Customer cPanel navigation. Raw Terminal & SSH keys are intentionally
 * ABSENT — admin-only tools (server-side gated at admin rank too; see the
 * Phase 5 authz fix in internal/terminal + internal/sshkeys).
 */
const NAV: SidebarGroup[] = [
  { section: 'Overview', items: [{ to: '/', label: 'Dashboard', icon: LayoutGrid }] },
  {
    section: 'Websites',
    items: [
      { to: '/websites', label: 'Websites', icon: Globe },
      { to: '/domains', label: 'Domains', icon: Network },
      { to: '/dns', label: 'DNS Zone', icon: Network },
      { to: '/files', label: 'File Manager', icon: FolderOpen },
      { to: '/ftp', label: 'FTP Accounts', icon: KeyRound },
    ],
  },
  {
    section: 'Data',
    items: [
      { to: '/databases', label: 'Databases', icon: Database },
      { to: '/php', label: 'PHP', icon: ServerIcon },
      { to: '/crons', label: 'Cron Jobs', icon: Clock },
      { to: '/backups', label: 'Backups', icon: History },
      { to: '/metrics', label: 'Metrics', icon: ServerIcon },
    ],
  },
  {
    section: 'Account',
    items: [
      { to: '/ssl', label: 'SSL / TLS', icon: Lock },
      { to: '/security', label: 'Security', icon: ShieldCheck },
      { to: '/account', label: 'Account', icon: UserRound },
    ],
  },
]

const TITLES: { match: (p: string) => boolean; title: string; sub: string }[] = [
  { match: (p) => p === '/', title: 'Dashboard', sub: 'Your websites and hosting resources at a glance' },
  { match: (p) => p.startsWith('/websites/'), title: 'Website', sub: 'Files, DNS, crons and access' },
  { match: (p) => p.startsWith('/websites'), title: 'Websites', sub: 'Sites, runtimes and deployments' },
  { match: (p) => p.startsWith('/domains'), title: 'Domains', sub: 'Domains, subdomains and aliases' },
  { match: (p) => p.startsWith('/dns'), title: 'DNS Zone', sub: 'Authoritative DNS records' },
  { match: (p) => p.startsWith('/files'), title: 'File Manager', sub: 'Browse, edit and upload files' },
  { match: (p) => p.startsWith('/ftp'), title: 'FTP Accounts', sub: 'FTP and SFTP access' },
  { match: (p) => p.startsWith('/databases'), title: 'Databases', sub: 'MySQL databases and users' },
  { match: (p) => p.startsWith('/php'), title: 'PHP', sub: 'Runtime versions and extensions' },
  { match: (p) => p.startsWith('/crons'), title: 'Cron Jobs', sub: 'Scheduled tasks' },
  { match: (p) => p.startsWith('/backups'), title: 'Backups', sub: 'Restore points and schedules' },
  { match: (p) => p.startsWith('/metrics'), title: 'Metrics', sub: 'Resource usage over time' },
  { match: (p) => p.startsWith('/ssl'), title: 'SSL / TLS', sub: 'Certificates and HTTPS protection' },
  { match: (p) => p.startsWith('/security'), title: 'Security', sub: 'Two-factor and account security' },
  { match: (p) => p.startsWith('/account'), title: 'Account', sub: 'Your profile and details' },
]

function Shell({ children }: { children: ReactNode }) {
  const { user, org, orgs, setOrg, myRole, logout } = useAuth()
  const navigate = useNavigate()
  const [navOpen, setNavOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem('eh.sidebar.collapsed') === '1')
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [paletteOpen, setPaletteOpen] = useState(false)
  const { frames } = useMetrics()

  useEffect(() => {
    localStorage.setItem('eh.sidebar.collapsed', collapsed ? '1' : '0')
  }, [collapsed])

  useEffect(() => {
    if (!org) return
    api
      .get<{ alerts: Alert[] }>(`/v1/organizations/${org.id}/alerts`)
      .then((r) => setAlerts((r.alerts ?? []).filter((a) => !a.resolved_at)))
      .catch(() => setAlerts([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // Header platform freshness badge — WS live stream only (contract #4).
  const frameList = org ? Object.values(frames) : []
  const liveFrame = frameList.length > 0 ? frameList[0] : null
  const fresh = useFreshness(liveFrame)

  const doLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  // Command palette (Phase 14, verbatim component list) — Cmd/Ctrl+K.
  // Results providers are RBAC-filtered: entries whose backing routes are
  // gated server-side at RoleBilling (billing) only surface for members with
  // that rank or above; server enforcement stays the law.
  const canBilling = useMemo(() => {
    if (user?.is_platform_admin) return true
    return (ROLE_RANK[myRole] ?? 0) >= ROLE_RANK.billing
  }, [user?.is_platform_admin, myRole])
  useCommandPaletteHotkey(() => setPaletteOpen(true))
  const paletteProviders = useMemo<CommandResultsProvider[]>(() => {
    const nav = (to: string, label: string, group: string, icon: ReactNode, keywords?: string) => ({
      id: `${group.toLowerCase()}:${to}`,
      label,
      group,
      hint: to,
      icon,
      keywords,
      perform: () => navigate(to),
    })
    return [
      () => [
        nav('/', 'Dashboard', 'Tools', <LayoutGrid size={14} strokeWidth={1.8} />),
        nav('/websites', 'Websites', 'Tools', <Globe size={14} strokeWidth={1.8} />),
        nav('/domains', 'Domains', 'Tools', <Network size={14} strokeWidth={1.8} />),
        nav('/dns', 'DNS Zone', 'Tools', <Network size={14} strokeWidth={1.8} />),
        nav('/files', 'File Manager', 'Tools', <FolderOpen size={14} strokeWidth={1.8} />),
        nav('/ftp', 'FTP Accounts', 'Tools', <KeyRound size={14} strokeWidth={1.8} />),
        nav('/databases', 'Databases', 'Tools', <Database size={14} strokeWidth={1.8} />),
        nav('/php', 'PHP', 'Tools', <ServerIcon size={14} strokeWidth={1.8} />),
        nav('/crons', 'Cron Jobs', 'Tools', <Clock size={14} strokeWidth={1.8} />),
        nav('/backups', 'Backups', 'Tools', <History size={14} strokeWidth={1.8} />),
        nav('/metrics', 'Metrics', 'Tools', <ServerIcon size={14} strokeWidth={1.8} />),
        nav('/ssl', 'SSL / TLS', 'Tools', <Lock size={14} strokeWidth={1.8} />),
        nav('/security', 'Security', 'Tools', <ShieldCheck size={14} strokeWidth={1.8} />),
        nav('/account', 'Account', 'Tools', <UserRound size={14} strokeWidth={1.8} />),
        ...(canBilling
          ? [
              nav('/billing', 'Billing', 'Tools', <CreditCard size={14} strokeWidth={1.8} />, 'invoices subscriptions'),
              nav('/billing/invoices', 'Invoices', 'Tools', <Receipt size={14} strokeWidth={1.8} />, 'billing'),
            ]
          : []),
      ],
    ]
  }, [canBilling, navigate])

  return (
    <div className="min-h-screen bg-app">
      <AppSidebar
        groups={NAV}
        account={{
          name: user?.name ?? '',
          roleLabel: roleLabel(user, myRole),
          orgName: org?.name,
          onLogout: doLogout,
        }}
        open={navOpen}
        onClose={() => setNavOpen(false)}
        collapsed={collapsed}
        onToggleCollapsed={setCollapsed}
        top={
          // Invisible tenancy (ADR-060): a customer lives in exactly one
          // auto-created org — the picker only exists for multi-org members.
          orgs.length > 1 && (
            <div className="px-4 pt-2">
              <select
                value={org?.id ?? ''}
                onChange={(e) => {
                  const o = orgs.find((x) => x.id === e.target.value)
                  if (o) setOrg(o)
                }}
                className="w-full cursor-pointer rounded-[9px] border border-white/[.07] bg-white/[.035] px-2.5 py-2 text-[11.5px] font-semibold text-[#edf2fb] outline-none transition focus:border-[#3b82f6]"
              >
                {orgs.map((o) => (
                  <option key={o.id} value={o.id} className="text-ink">{o.name}</option>
                ))}
              </select>
            </div>
          )
        }
      />
      <div className={`transition-[padding] duration-200 ${collapsed ? 'lg:pl-[68px]' : 'lg:pl-[248px]'}`}>
        <CustomerHeader alerts={alerts} fresh={fresh} onMenu={() => setNavOpen(true)} />
        <main className="fade-up">{children}</main>
        <footer className="pb-8 pt-4 text-center text-[11px] text-muted">
          EpicHost — powered by EpicPanel
        </footer>
      </div>
      <Toaster />
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        providers={paletteProviders}
        placeholder="Search tools and actions..."
      />
      {!user && <Navigate to="/login" replace />}
    </div>
  )
}

function CustomerHeader({ alerts, fresh, onMenu }: { alerts: Alert[]; fresh: { state: 'LIVE' | 'STALE' | 'OFFLINE'; ageMs: number }; onMenu: () => void }) {
  const { user, org } = useAuth()
  const location = useLocation()

  const ctx = TITLES.find((t) => t.match(location.pathname)) ?? { title: 'Control Panel', sub: 'Hosting management' }

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
        <div className="min-w-0">
          <strong className="block truncate text-[14px] tracking-[-.015em] text-ink">{ctx.title}</strong>
          <span className="block truncate text-[10px] text-muted">
            {org ? `${org.name} · ` : ''}{ctx.sub}
          </span>
        </div>
        <div className="flex-1" />
        {org && <FreshnessBadge state={fresh.state} ageMs={fresh.ageMs} label="Platform" />}
        <Link
          to="/metrics"
          title="Alerts"
          className="relative grid h-[38px] w-[38px] cursor-pointer place-items-center rounded-[9px] border border-line bg-white text-[#596579] transition hover:bg-surface-2"
        >
          <Bell size={17} strokeWidth={1.8} />
          {alerts.length > 0 && (
            <span className="absolute right-2 top-2 h-1.5 w-1.5 rounded-full border border-white bg-danger" />
          )}
        </Link>
        <Link to="/account" title={user?.email} className="flex cursor-pointer items-center gap-[9px] pl-[7px]">
          <span className="grid h-[34px] w-[34px] place-items-center rounded-full bg-brand-soft text-[11px] font-extrabold text-brand">
            {(user?.name ?? 'U').charAt(0).toUpperCase()}
          </span>
          <span className="hidden text-left min-[680px]:block">
            <strong className="block max-w-[140px] truncate text-[11px] text-ink">{user?.name ?? '...'}</strong>
            <span className="block text-[9px] text-muted">cPanel</span>
          </span>
        </Link>
      </div>
    </header>
  )
}

function Guarded() {
  const { user, org, orgs, loading, createOrg, setOrg } = useAuth()
  if (loading) return <ScreenFallback />
  if (!user) return <Navigate to="/login" replace />
  if (!org) {
    return <CreateOrganizationScreen hasOrgs={orgs.length > 0} onCreate={createOrg} onPick={setOrg} orgs={orgs} />
  }
  return (
    <Shell>
      <Suspense fallback={<RouteFallback />}>
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/websites" element={<WebsitesPage />} />
          <Route path="/domains" element={<DomainsPage />} />
          <Route path="/dns" element={<DnsZonePage />} />
          <Route path="/dns/:website_id" element={<DnsZonePage />} />
          <Route path="/files" element={<Navigate to="/websites" replace />} />
          <Route path="/files/:website_id" element={<FileManagerPage />} />
          <Route path="/ftp" element={<FtpPage />} />
          <Route path="/databases" element={<DatabasesPage />} />
          <Route path="/php" element={<PhpPage />} />
          <Route path="/crons" element={<CronJobsPage />} />
          <Route path="/crons/:website_id" element={<CronJobsPage />} />
          <Route path="/backups" element={<BackupsPage />} />
          <Route path="/metrics" element={<MetricsPage />} />
          <Route path="/ssl" element={<SslPage />} />
          <Route path="/security" element={<SecurityPage />} />
          <Route path="/account" element={<AccountPage />} />
          {[...billingRoutes, ...backupsRoutes].map((r, i) => (
            <Route key={i} path={r.path} element={r.element} />
          ))}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
    </Shell>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Suspense fallback={<ScreenFallback />}>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/register" element={<RegisterPage />} />
            <Route path="/*" element={<Guarded />} />
          </Routes>
        </Suspense>
      </AuthProvider>
    </BrowserRouter>
  )
}

// Shown when an authenticated user has no organization selected (fresh
// accounts). The API makes every user the owner of orgs they create.
function CreateOrganizationScreen({ hasOrgs, onCreate, onPick, orgs }: {
  hasOrgs: boolean
  onCreate: (name: string) => Promise<unknown>
  onPick: (o: Organization) => void
  orgs: Organization[]
}) {
  const { logout } = useAuth()
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // If orgs exist but none selected (e.g. storage cleared), pick the first.
  useEffect(() => {
    if (hasOrgs && orgs.length > 0) onPick(orgs[0])
  }, [hasOrgs, orgs]) // eslint-disable-line react-hooks/exhaustive-deps

  const submit = async () => {
    setErr('')
    setBusy(true)
    try {
      await onCreate(name)
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create organization')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-app p-4">
      <div className="w-full max-w-[420px] fade-up">
        <div className="mb-6 flex flex-col items-center gap-3">
          <div
            className="flex h-[42px] w-[42px] items-center justify-center rounded-[12px] text-white"
            style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)', boxShadow: '0 8px 18px rgba(37,99,235,.27)' }}
          >
            <Globe size={21} strokeWidth={1.8} />
          </div>
          <div className="text-[21px] font-bold tracking-[-.025em] text-ink">Create your workspace</div>
          <p className="max-w-[300px] text-center text-[12px] text-muted">
            An organization holds your sites, databases and servers. You'll be its owner.
          </p>
        </div>
        <div className="card p-6">
          <ErrorNote message={err} />
          <Field label="Organization name" hint="e.g. your company or project name.">
            <input
              className="input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Acme Hosting"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && name && submit()}
            />
          </Field>
          <button className="btn-brand w-full justify-center" onClick={submit} disabled={busy || !name}>
            {busy ? (<><Spinner size={13} /> Creating…</>) : 'Create Organization'}
          </button>
          <button
            className="mt-3 w-full text-center text-[13px] text-muted transition hover:text-sub"
            onClick={logout}
          >
            Sign out
          </button>
        </div>
      </div>
    </div>
  )
}
