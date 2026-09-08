import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Search, Bell, Menu, ChevronDown } from 'lucide-react'
import { useAuth } from '@/context/AuthContext'
import type { Alert } from '@/lib/types'

const TITLES: { match: (p: string) => boolean; title: string; sub: string }[] = [
  { match: (p) => p === '/', title: 'Dashboard', sub: 'Everything important about your hosting in one place' },
  { match: (p) => p.startsWith('/sites/') && p.endsWith('/application'), title: 'Application', sub: 'Manage the application deployed on your site' },
  { match: (p) => p.startsWith('/sites/') && p.endsWith('/files'), title: 'File Manager', sub: 'Files and directories for this site' },
  { match: (p) => p.startsWith('/sites/') && p.endsWith('/crons'), title: 'Cron Jobs', sub: 'Scheduled tasks for this site' },
  { match: (p) => p.startsWith('/sites/') && p.endsWith('/ssh-keys'), title: 'SSH Keys', sub: 'Key-based access for this site' },
  { match: (p) => p.startsWith('/sites/') && p.endsWith('/terminal'), title: 'Terminal', sub: 'Direct shell access to this site' },
  { match: (p) => p.startsWith('/sites/') && p.split('/').length === 3, title: 'Site Detail', sub: 'Manage this website and its settings' },
  { match: (p) => p.startsWith('/sites'), title: 'Sites', sub: 'Websites, applications and deployments' },
  { match: (p) => p.startsWith('/files'), title: 'Files', sub: 'Pick a site to browse its files' },
  { match: (p) => p.startsWith('/databases'), title: 'Databases', sub: 'MySQL databases and users' },
  { match: (p) => p.startsWith('/servers'), title: 'Servers', sub: 'Connected nodes and live health' },
  { match: (p) => p.startsWith('/software'), title: 'Software', sub: 'Runtimes, PHP versions and extensions' },
  { match: (p) => p.startsWith('/backups'), title: 'Backups', sub: 'Snapshots and restore points' },
  { match: (p) => p.startsWith('/security'), title: 'SSL / Security', sub: 'Certificates and HTTPS protection' },
  { match: (p) => p.startsWith('/activity'), title: 'Activity Log', sub: 'Audit trail of important actions' },
  { match: (p) => p.startsWith('/team'), title: 'Users', sub: 'People with access to this workspace' },
  { match: (p) => p.startsWith('/admin/users'), title: 'Manage Accounts', sub: 'Platform user administration' },
  { match: (p) => p.startsWith('/admin/packages'), title: 'Packages', sub: 'Hosting plans and resource limits' },
  { match: (p) => p.startsWith('/settings'), title: 'Settings', sub: 'Workspace defaults and preferences' },
]

/**
 * Phase 14: the header no longer carries its own search implementation —
 * every search flows through the shared ⌘K CommandPalette (RBAC-filtered
 * results come from the providers in App.tsx). This trigger just opens it;
 * "/" is the keyboard shortcut.
 */
export function Header({ onMenu, onOpenPalette, alerts }: { onMenu: () => void; onOpenPalette: () => void; alerts: Alert[] }) {
  const { user, org } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const [paletteHint, setPaletteHint] = useState<'⌘K' | 'Ctrl K'>('⌘K')

  useEffect(() => {
    setPaletteHint(/mac|iphone|ipad/i.test(navigator.platform) ? '⌘K' : 'Ctrl K')
  }, [])

  // "/" opens the command palette (except while typing in a field).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && !(e.target instanceof HTMLInputElement) && !(e.target instanceof HTMLTextAreaElement) && !(e.target instanceof HTMLSelectElement)) {
        e.preventDefault()
        onOpenPalette()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onOpenPalette])

  const ctx = TITLES.find((t) => t.match(location.pathname)) ?? { title: 'Control Center', sub: 'Hosting administration' }

  const roleLabel = user?.is_platform_admin ? 'Administrator' : 'Member'

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

        {/* Page context */}
        <div className="min-w-0">
          <strong className="block truncate text-[14px] tracking-[-.015em] text-ink">{ctx.title}</strong>
          <span className="block truncate text-[10px] text-muted">
            {org ? `${org.name} · ` : ''}{ctx.sub}
          </span>
        </div>

        <div className="flex-1" />

        {/* Command palette trigger (⌘K / Ctrl+K / "/") */}
        <button
          onClick={onOpenPalette}
          aria-label="Open command palette"
          className="hidden h-[38px] w-[240px] cursor-pointer items-center gap-2.5 rounded-[9px] border border-line bg-surface-2 px-3 text-left transition hover:border-[#9db8ff] hover:bg-white min-[500px]:flex"
        >
          <Search size={15} className="shrink-0 text-[#98a2b3]" />
          <span className="flex-1 truncate text-[12px] text-[#98a2b3]">Search tools and accounts...</span>
          <kbd className="shrink-0 rounded-[5px] border border-line bg-white px-1.5 py-0.5 text-[9px] font-bold text-muted">
            {paletteHint}
          </kbd>
        </button>
        <button
          onClick={onOpenPalette}
          aria-label="Open command palette"
          title="Search (press /)"
          className="grid h-[38px] w-[38px] cursor-pointer place-items-center rounded-[9px] border border-line bg-white text-[#596579] transition hover:bg-surface-2 min-[500px]:hidden"
        >
          <Search size={16} />
        </button>

        {/* Notifications */}
        <button
          onClick={() => navigate('/activity')}
          title="Notifications"
          aria-label={`Notifications${alerts.length ? ` (${alerts.length} unresolved)` : ''}`}
          className="relative grid h-[38px] w-[38px] cursor-pointer place-items-center rounded-[9px] border border-line bg-white text-[#596579] transition hover:bg-surface-2"
        >
          <Bell size={17} strokeWidth={1.8} />
          {alerts.length > 0 && (
            <span className="absolute right-2 top-2 h-1.5 w-1.5 rounded-full border border-white bg-danger" aria-hidden="true" />
          )}
        </button>

        {/* Profile */}
        <button
          onClick={() => navigate('/settings')}
          title={user?.email}
          className="flex cursor-pointer items-center gap-[9px] pl-[7px]"
        >
          <span className="grid h-[34px] w-[34px] place-items-center rounded-full bg-brand-soft text-[11px] font-extrabold text-brand">
            {(user?.name ?? 'U').charAt(0).toUpperCase()}
          </span>
          <span className="hidden text-left min-[680px]:block">
            <strong className="block max-w-[140px] truncate text-[11px] text-ink">{user?.name ?? '...'}</strong>
            <span className="block text-[9px] text-muted">{roleLabel}</span>
          </span>
          <ChevronDown size={13} className="hidden text-[#98a2b3] min-[680px]:block" />
        </button>
      </div>
    </header>
  )
}
