import { lazy, Suspense, useEffect, useState, ReactNode, useMemo, useCallback } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import { Globe, LogOut, PanelLeftClose, UserRound } from 'lucide-react'
import { AuthProvider, useAuth } from '@/context/AuthContext'
import { CommandPalette, ErrorBoundary, RouteFallback, SkeletonScreen, Toaster } from '@epicpanel/ui'
import type { CommandResultsProvider } from '@epicpanel/ui'
import { Sidebar, filterNavForUser } from '@/components/Sidebar'
import { Header } from '@/components/Header'
import { api } from '@/lib/api'
import { Field, ErrorNote } from '@/components/ui'
import type { Alert } from '@/lib/types'

/* Route-level code splitting: every page is a lazy chunk, so the entry bundle
 * carries only the shell + shared UI and each route downloads what it needs.
 * Named exports are mapped to default for React.lazy. */
const LoginPage = lazy(() => import('@/pages/Auth').then((m) => ({ default: m.LoginPage })))
const RegisterPage = lazy(() => import('@/pages/Auth').then((m) => ({ default: m.RegisterPage })))
const SetupPage = lazy(() => import('@/pages/Setup').then((m) => ({ default: m.SetupPage })))
const DashboardPage = lazy(() => import('@/pages/Dashboard').then((m) => ({ default: m.DashboardPage })))
const SitesPage = lazy(() => import('@/pages/Sites').then((m) => ({ default: m.SitesPage })))
const FileManagerPage = lazy(() => import('@/pages/FileManager').then((m) => ({ default: m.FileManagerPage })))
const CronJobsPage = lazy(() => import('@/pages/CronJobs').then((m) => ({ default: m.CronJobsPage })))
const SSHKeysPage = lazy(() => import('@/pages/SSHKeys').then((m) => ({ default: m.SSHKeysPage })))
const DnsZonePage = lazy(() => import('@/pages/DnsZone').then((m) => ({ default: m.DnsZonePage })))
const SiteDetailPage = lazy(() => import('@/pages/SiteDetail').then((m) => ({ default: m.SiteDetailPage })))
const ApplicationDetailPage = lazy(() => import('@/pages/ApplicationDetail').then((m) => ({ default: m.ApplicationDetailPage })))
const TerminalPage = lazy(() => import('@/pages/Terminal').then((m) => ({ default: m.TerminalPage })))
const FilesLandingPage = lazy(() => import('@/pages/FilesLanding').then((m) => ({ default: m.FilesLandingPage })))
const DatabasesPage = lazy(() => import('@/pages/Databases').then((m) => ({ default: m.DatabasesPage })))
const ServersPage = lazy(() => import('@/pages/Servers').then((m) => ({ default: m.ServersPage })))
const SoftwarePage = lazy(() => import('@/pages/Software').then((m) => ({ default: m.SoftwarePage })))
const ActivityPage = lazy(() => import('@/pages/Activity').then((m) => ({ default: m.ActivityPage })))
const TeamPage = lazy(() => import('@/pages/Team').then((m) => ({ default: m.TeamPage })))
const AdminUsersPage = lazy(() => import('@/pages/AdminUsers').then((m) => ({ default: m.AdminUsersPage })))
const PackagesPage = lazy(() => import('@/pages/Packages').then((m) => ({ default: m.PackagesPage })))
const BackupsPage = lazy(() => import('@/pages/Backups').then((m) => ({ default: m.BackupsPage })))
const SecurityPage = lazy(() => import('@/pages/Security').then((m) => ({ default: m.SecurityPage })))
const SettingsPage = lazy(() => import('@/pages/Settings').then((m) => ({ default: m.SettingsPage })))
const SoftwareInstallerPage = lazy(() => import('@/pages/SoftwareInstaller').then((m) => ({ default: m.SoftwareInstallerPage })))
const BillingPage = lazy(() => import('@/pages/Billing').then((m) => ({ default: m.BillingPage })))

const BASE_TITLE = 'EpicHost — Hosting Control Center'

/** Sets the per-route document title (restores the base title on unmount). */
function DocumentTitle({ title }: { title: string }) {
  useEffect(() => {
    document.title = `${title} · EpicHost`
    return () => {
      document.title = BASE_TITLE
    }
  }, [title])
  return null
}

/** Attach a document title to a route element without touching the page. */
function titled(title: string, el: ReactNode) {
  return (
    <>
      <DocumentTitle title={title} />
      {el}
    </>
  )
}

function Shell({ children }: { children: ReactNode }) {
  const { user, org, myRole, logout } = useAuth()
  const navigate = useNavigate()
  const [navOpen, setNavOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem('eh.sidebar.collapsed') === '1')
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [paletteOpen, setPaletteOpen] = useState(false)

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

  /* Command palette providers — RBAC-filtered (same gate as the sidebar):
   * 1. Tools: every nav item the user may see.
   * 2. Actions: things the user can DO from anywhere.
   * 3. Accounts: org members, fetched on demand (admin tools only surface
   *    for admins — see the minRole gate in filterNavForUser).
   */
  const doLogout = useCallback(async () => {
    await logout()
    navigate('/login', { replace: true })
  }, [logout, navigate])

  const paletteProviders = useMemo<CommandResultsProvider[]>(() => {
    const tools = filterNavForUser(user, myRole)
    return [
      (q) =>
        tools.map((t) => ({
          id: `tool:${t.to}`,
          label: t.label,
          group: 'Tools',
          hint: t.to,
          icon: <t.icon size={14} strokeWidth={1.8} />,
          perform: () => navigate(t.to),
        })),
      () => [
        {
          id: 'act:toggle-sidebar',
          label: collapsed ? 'Expand sidebar' : 'Collapse sidebar',
          group: 'Actions',
          icon: <PanelLeftClose size={14} strokeWidth={1.8} />,
          perform: () => setCollapsed((c) => !c),
        },
        {
          id: 'act:sign-out',
          label: 'Sign out',
          group: 'Actions',
          icon: <LogOut size={14} strokeWidth={1.8} />,
          dangerous: true,
          perform: () => void doLogout(),
        },
      ],
      async (q) => {
        if (!org || !q) return []
        try {
          const r = await api.get<{ members: { name: string; email: string; role: string }[] }>(
            `/v1/organizations/${org.id}/members`,
          )
          return (r.members ?? []).map((m) => ({
            id: `acct:${m.email}`,
            label: m.name || m.email,
            group: 'Accounts',
            hint: `${m.email} · ${m.role || 'member'}`,
            icon: <UserRound size={14} strokeWidth={1.8} />,
            keywords: m.email,
            perform: () => navigate('/team'),
          }))
        } catch {
          return []
        }
      },
    ]
  }, [user, myRole, org, collapsed, navigate, doLogout])

  return (
    <div className="min-h-screen bg-app">
      <Sidebar open={navOpen} onClose={() => setNavOpen(false)} collapsed={collapsed} onToggleCollapsed={setCollapsed} />
      <div className={`transition-[padding] duration-200 ${collapsed ? 'lg:pl-[68px]' : 'lg:pl-[248px]'}`}>
        <Header onMenu={() => setNavOpen(true)} onOpenPalette={() => setPaletteOpen(true)} alerts={alerts} />
        <main className="fade-up">{children}</main>
        <footer className="pb-8 pt-4 text-center text-[11px] text-muted">
          EpicHost — powered by EpicPanel
        </footer>
      </div>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        providers={paletteProviders}
        placeholder="Search tools, accounts and actions..."
      />
      <Toaster />
      {!user && <Navigate to="/login" replace />}
    </div>
  )
}

function Guarded() {
  const { user, org, orgs, loading, createOrg } = useAuth()
  const location = useLocation()
  if (loading) return <SkeletonScreen label="Loading your workspace" />
  if (!user) return <Navigate to="/login" replace />
  if (!org) {
    return <CreateOrganizationScreen hasOrgs={orgs.length > 0} onCreate={createOrg} />
  }
  return (
    <Shell>
      {/* Page crashes stay inside the content area (shell keeps working);
          Suspense shows the branded page skeleton while the chunk loads. */}
      <ErrorBoundary resetKey={location.pathname} label="Page content">
        <Suspense fallback={<RouteFallback />}>
          <Routes>
            <Route path="/" element={titled('Dashboard', <DashboardPage />)} />
            <Route path="/sites" element={titled('Sites', <SitesPage />)} />
            <Route path="/sites/:website_id" element={titled('Site', <SiteDetailPage />)} />
            <Route path="/sites/:website_id/application" element={titled('Application', <ApplicationDetailPage />)} />
            <Route path="/sites/:website_id/files" element={titled('File Manager', <FileManagerPage />)} />
            <Route path="/sites/:website_id/crons" element={titled('Cron Jobs', <CronJobsPage />)} />
            <Route path="/sites/:website_id/ssh-keys" element={titled('SSH Keys', <SSHKeysPage />)} />
            <Route path="/sites/:website_id/dns" element={titled('DNS Zone', <DnsZonePage />)} />
            <Route path="/sites/:website_id/terminal" element={titled('Terminal', <TerminalPage />)} />
            <Route path="/files" element={titled('Files', <FilesLandingPage />)} />
            <Route path="/billing" element={titled('Billing', <BillingPage />)} />
            <Route path="/databases" element={titled('Databases', <DatabasesPage />)} />
            <Route path="/servers" element={<RequireAdmin>{titled('Servers', <ServersPage />)}</RequireAdmin>} />
            <Route path="/software" element={<RequireAdmin>{titled('Software', <SoftwarePage />)}</RequireAdmin>} />
            <Route path="/software/installer" element={<RequireAdmin>{titled('Software Installer', <SoftwareInstallerPage />)}</RequireAdmin>} />
            <Route path="/activity" element={titled('Activity', <ActivityPage />)} />
            <Route path="/team" element={titled('Team', <TeamPage />)} />
            <Route path="/admin/users" element={<RequireAdmin>{titled('Users', <AdminUsersPage />)}</RequireAdmin>} />
            <Route path="/admin/packages" element={<RequireAdmin>{titled('Packages', <PackagesPage />)}</RequireAdmin>} />
            <Route path="/backups" element={titled('Backups', <BackupsPage />)} />
            <Route path="/security" element={titled('Security', <SecurityPage />)} />
            <Route path="/settings" element={titled('Settings', <SettingsPage />)} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </ErrorBoundary>
    </Shell>
  )
}

// Admin-only pages redirect members to the dashboard instead of showing a
// dead-end "administrator-only" screen.
function RequireAdmin({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  if (!user?.is_platform_admin) return <Navigate to="/" replace />
  return <>{children}</>
}

function AppRoutes() {
  const location = useLocation()
  return (
    <ErrorBoundary resetKey={location.pathname} label="EpicHost application">
      <AuthProvider>
        <Suspense fallback={<SkeletonScreen label="Loading" />}>
          <Routes>
            <Route path="/login" element={titled('Sign in', <LoginPage />)} />
            <Route path="/setup" element={titled('Setup', <SetupPage />)} />
            <Route path="/register" element={titled('Create account', <RegisterPage />)} />
            <Route path="/*" element={<Guarded />} />
          </Routes>
        </Suspense>
      </AuthProvider>
    </ErrorBoundary>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  )
}

// Shown when an authenticated user has no organization selected (fresh
// accounts). The API makes every user the owner of orgs they create.
function CreateOrganizationScreen({ hasOrgs, onCreate }: {
  hasOrgs: boolean
  onCreate: (name: string) => Promise<unknown>
}) {
  const { logout, setOrg, orgs } = useAuth()
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // If orgs exist but none selected (e.g. storage cleared), pick the first.
  useEffect(() => {
    if (hasOrgs && orgs.length > 0) setOrg(orgs[0])
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
            {busy ? 'Creating...' : 'Create Organization'}
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
