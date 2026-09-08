import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Search, Bell, Menu, ChevronDown } from 'lucide-react'
import { useAuth } from '@/context/AuthContext'
import { filterNavForUser } from '@/components/Sidebar'
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

export function Header({ onMenu, alerts }: { onMenu: () => void; alerts: Alert[] }) {
  const { user, org, myRole } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [focused, setFocused] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)

  // "/" focuses the toolbar search
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && !focused && !(e.target instanceof HTMLInputElement) && !(e.target instanceof HTMLTextAreaElement)) {
        e.preventDefault()
        searchRef.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [focused])

  const results = useMemo(() => {
    const q = query.toLowerCase().trim()
    if (!q) return []
    // Same gating as the sidebar: admin-only tools never appear for members.
    return filterNavForUser(user, myRole)
      .filter((it) => it.label.toLowerCase().includes(q))
      .slice(0, 8)
  }, [query, user, myRole])

  const go = (to: string) => {
    setQuery('')
    setFocused(false)
    navigate(to)
  }

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

        {/* Search */}
        <div className="relative hidden min-[500px]:block" style={{ width: 'min(300px, 28vw)' }}>
          <Search size={16} className="pointer-events-none absolute left-[11px] top-1/2 -translate-y-1/2 text-[#98a2b3]" />
          <input
            ref={searchRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onFocus={() => setFocused(true)}
            onBlur={() => setTimeout(() => setFocused(false), 150)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && results[0]) go(results[0].to)
              if (e.key === 'Escape') { setQuery(''); searchRef.current?.blur() }
            }}
            placeholder="Search tools and accounts..."
            className="h-[38px] w-full rounded-[9px] border border-line bg-surface-2 pl-[35px] pr-3 text-[12px] text-ink outline-none transition placeholder:text-[#98a2b3] focus:border-[#9db8ff] focus:bg-white focus:ring-[3px] focus:ring-brand/10"
          />
          {focused && results.length > 0 && (
            <div className="fade-up absolute right-0 top-[44px] z-40 w-[260px] rounded-[12px] border border-line bg-white py-1.5 shadow-pop">
              {results.map((r) => {
                const Icon = r.icon
                return (
                  <button
                    key={r.to}
                    onMouseDown={() => go(r.to)}
                    className="flex w-full items-center gap-2.5 px-3.5 py-2 text-left text-[12px] font-semibold text-ink transition hover:bg-surface-2"
                  >
                    <Icon size={14} strokeWidth={1.8} className="text-muted" /> {r.label}
                  </button>
                )
              })}
            </div>
          )}
        </div>

        {/* Notifications */}
        <button
          onClick={() => navigate('/activity')}
          title="Notifications"
          className="relative grid h-[38px] w-[38px] cursor-pointer place-items-center rounded-[9px] border border-line bg-white text-[#596579] transition hover:bg-surface-2"
        >
          <Bell size={17} strokeWidth={1.8} />
          {alerts.length > 0 && (
            <span className="absolute right-2 top-2 h-1.5 w-1.5 rounded-full border border-white bg-danger" />
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
