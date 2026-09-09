import { useEffect, useState, ReactNode, useMemo, useCallback } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate } from 'react-router-dom'
import { Globe, LogOut, PanelLeftClose, UserRound } from 'lucide-react'
import { AuthProvider, useAuth } from '@/context/AuthContext'
import { CommandPalette, SkeletonScreen, Toaster } from '@epicpanel/ui'
import type { CommandResultsProvider } from '@epicpanel/ui'
import { Sidebar, filterNavForUser } from '@/components/Sidebar'
import { Header } from '@/components/Header'
import { api } from '@/lib/api'
import { Field, ErrorNote } from '@/components/ui'
import type { Alert } from '@/lib/types'
import { LoginPage, RegisterPage } from '@/pages/Auth'
import { SetupPage } from '@/pages/Setup'
import { DashboardPage } from '@/pages/Dashboard'
import { SitesPage } from '@/pages/Sites'
import { FileManagerPage } from '@/pages/FileManager'
import { CronJobsPage } from '@/pages/CronJobs'
import { SSHKeysPage } from '@/pages/SSHKeys'
import { DnsZonePage } from '@/pages/DnsZone'
import { SiteDetailPage } from '@/pages/SiteDetail'
import { ApplicationDetailPage } from '@/pages/ApplicationDetail'
import { TerminalPage } from '@/pages/Terminal'
import { FilesLandingPage } from '@/pages/FilesLanding'
import { DatabasesPage } from '@/pages/Databases'
import { ServersPage } from '@/pages/Servers'
import { SoftwarePage } from '@/pages/Software'
import { ActivityPage } from '@/pages/Activity'
import { TeamPage } from '@/pages/Team'
import { AdminUsersPage } from '@/pages/AdminUsers'
import { PackagesPage } from '@/pages/Packages'
import { BackupsPage } from '@/pages/Backups'
import { SecurityPage } from '@/pages/Security'
import { SettingsPage } from '@/pages/Settings'
import { MinecraftPage, MinecraftDetailPage } from '@/pages/Minecraft'
import { BotsPage, BotDetailPage } from '@/pages/Bots'
import { BillingPage } from '@/pages/Billing'

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
  if (loading) return <SkeletonScreen label="Loading your workspace" />
  if (!user) return <Navigate to="/login" replace />
  if (!org) {
    return <CreateOrganizationScreen hasOrgs={orgs.length > 0} onCreate={createOrg} />
  }
  return (
    <Shell>
      <Routes>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/sites" element={<SitesPage />} />
        <Route path="/sites/:website_id" element={<SiteDetailPage />} />
        <Route path="/sites/:website_id/application" element={<ApplicationDetailPage />} />
        <Route path="/sites/:website_id/files" element={<FileManagerPage />} />
        <Route path="/sites/:website_id/crons" element={<CronJobsPage />} />
        <Route path="/sites/:website_id/ssh-keys" element={<SSHKeysPage />} />
        <Route path="/sites/:website_id/dns" element={<DnsZonePage />} />
        <Route path="/sites/:website_id/terminal" element={<TerminalPage />} />
        <Route path="/files" element={<FilesLandingPage />} />
        <Route path="/minecraft" element={<MinecraftPage />} />
        <Route path="/minecraft/:id" element={<MinecraftDetailPage />} />
        <Route path="/bots" element={<BotsPage />} />
        <Route path="/bots/:id" element={<BotDetailPage />} />
        <Route path="/billing" element={<BillingPage />} />
        <Route path="/databases" element={<DatabasesPage />} />
        <Route path="/servers" element={<RequireAdmin><ServersPage /></RequireAdmin>} />
        <Route path="/software" element={<RequireAdmin><SoftwarePage /></RequireAdmin>} />
        <Route path="/activity" element={<ActivityPage />} />
        <Route path="/team" element={<TeamPage />} />
        <Route path="/admin/users" element={<RequireAdmin><AdminUsersPage /></RequireAdmin>} />
        <Route path="/admin/packages" element={<RequireAdmin><PackagesPage /></RequireAdmin>} />
        <Route path="/backups" element={<BackupsPage />} />
        <Route path="/security" element={<SecurityPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
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

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/setup" element={<SetupPage />} />
          <Route path="/register" element={<RegisterPage />} />
          <Route path="/*" element={<Guarded />} />
        </Routes>
      </AuthProvider>
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
