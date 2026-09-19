import { useNavigate } from 'react-router-dom'
import {
  LayoutGrid, Globe, Database, Folder, History, Lock, UserRound,
  Settings, Server, Activity, Package, ShieldCheck, CreditCard, HardDriveDownload,
} from 'lucide-react'
import { AppSidebar } from '@epicpanel/ui'
import type { SidebarGroup, SidebarNavItem } from '@epicpanel/ui'
import { useAuth } from '@/context/AuthContext'

interface NavItem extends SidebarNavItem {
  /** Minimum organization role rank required to see this item. */
  minRole?: 'owner' | 'admin' | 'developer' | 'billing'
  /** Platform administrator only. */
  adminOnly?: boolean
}

const NAV: { section: string; items: NavItem[] }[] = [
  {
    section: 'Overview',
    items: [{ to: '/', label: 'Dashboard', icon: LayoutGrid }],
  },
  {
    section: 'Websites',
    items: [
      { to: '/sites', label: 'Sites', icon: Globe },
      { to: '/files', label: 'Files', icon: Folder },
      { to: '/databases', label: 'Databases', icon: Database },
    ],
  },
  {
    section: 'Server',
    items: [
      { to: '/servers', label: 'Servers', icon: Server, adminOnly: true },
      { to: '/software', label: 'Software', icon: Package, adminOnly: true },
      { to: '/software/installer', label: 'Installer', icon: HardDriveDownload, adminOnly: true },
      { to: '/backups', label: 'Backups', icon: History },
      { to: '/security', label: 'SSL / Security', icon: Lock },
      { to: '/billing', label: 'Billing', icon: CreditCard },
    ],
  },
  {
    section: 'Platform',
    items: [
      { to: '/activity', label: 'Activity Log', icon: Activity },
      { to: '/team', label: 'Users', icon: UserRound, minRole: 'admin' },
      { to: '/admin/users', label: 'Manage Accounts', icon: ShieldCheck, adminOnly: true },
      { to: '/admin/packages', label: 'Packages', icon: Package, adminOnly: true },
      { to: '/settings', label: 'Settings', icon: Settings },
    ],
  },
]

const ROLE_RANK: Record<string, number> = { support: 1, billing: 1, developer: 2, admin: 3, owner: 4 }

const ALL_ITEMS = NAV.flatMap((g) => g.items)

/**
 * Exported so the command palette (and anything else) can filter nav the
 * same way the sidebar does. Applies platform-admin gating + org-role
 * ranking. RBAC for the sidebar AND the ⌘K palette comes from this single
 * function — one gate, no drift.
 */
export function filterNavForUser(user: { is_platform_admin?: boolean } | null | undefined, myRole: string | null | undefined) {
  const rank = user?.is_platform_admin ? 4 : ROLE_RANK[myRole ?? ''] ?? 0
  const minOk = (min?: string) => !min || rank >= (ROLE_RANK[min] ?? 4)
  return ALL_ITEMS.filter(
    (it) => (!it.adminOnly || user?.is_platform_admin) && minOk(it.minRole),
  )
}

export const SIDEBAR_W = { expanded: 248, collapsed: 68 }

/**
 * Phase 14 dedupe: the root app previously carried a ~230-line copy of the
 * shared AppSidebar. It now wraps @epicpanel/ui AppSidebar — the ONE
 * implementation both apps render — and only maps the org switcher slot.
 */
export function Sidebar({ open, onClose, collapsed, onToggleCollapsed }: {
  open: boolean
  onClose: () => void
  collapsed: boolean
  onToggleCollapsed: (v: boolean) => void
}) {
  const { user, org, orgs, setOrg, myRole, logout } = useAuth()
  const navigate = useNavigate()

  const doLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  // RBAC gate (same function the ⌘K palette uses) applied BEFORE the items
  // reach the shared sidebar — admin-only tools never render for members.
  const allowed = new Set(filterNavForUser(user, myRole).map((n) => n.to))
  const groups: SidebarGroup[] = NAV.map((g) => ({
    section: g.section,
    items: g.items.filter((it) => allowed.has(it.to)),
  })).filter((g) => g.items.length > 0)

  return (
    <AppSidebar
      groups={groups}
      account={{
        name: user?.name ?? '',
        roleLabel: user?.is_platform_admin ? 'Administrator' : myRole ? myRole.charAt(0).toUpperCase() + myRole.slice(1) : 'Member',
        orgName: org?.name,
        onLogout: doLogout,
      }}
      open={open}
      onClose={onClose}
      collapsed={collapsed}
      onToggleCollapsed={onToggleCollapsed}
      top={
        // Invisible tenancy (ADR-060): single-org members never see the
        // switcher — the org picker only appears for multi-org users.
        orgs.length > 1 && (
          <div className="px-4 pt-2">
            <select
              value={org?.id ?? ''}
              onChange={(e) => {
                const o = orgs.find((x) => x.id === e.target.value)
                if (o) setOrg(o)
              }}
              aria-label="Switch organization"
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
  )
}

export { ALL_ITEMS }
