import { useEffect, useMemo, useRef, useState } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { CloudCog, LogOut, Search, ChevronLeft, ChevronRight } from 'lucide-react'

export interface SidebarNavItem {
  to: string
  label: string
  icon: React.ComponentType<{ size?: number | string; className?: string; strokeWidth?: number }>
}

export interface SidebarGroup {
  section?: string
  items: SidebarNavItem[]
}

export interface SidebarAccount {
  name: string
  roleLabel: string
  orgName?: string
  onLogout: () => void
}

/**
 * Shared responsive sidebar shell (Phase 14 UX kit, contract #7): one
 * implementation for both apps — apps pass their own nav groups and account
 * block. Dark navy (#0c1526) gradient, collapsible, Ctrl+F tool search.
 */
export function AppSidebar({ groups, account, open, onClose, collapsed, onToggleCollapsed, top, hidden }: {
  groups: SidebarGroup[]
  account: SidebarAccount
  open: boolean
  onClose: () => void
  collapsed: boolean
  onToggleCollapsed: (v: boolean) => void
  /** Slot above the nav (org switcher etc.). */
  top?: React.ReactNode
  /** Extra filter hook: return false to drop an item. */
  hidden?: (item: SidebarNavItem) => boolean
}) {
  const location = useLocation()
  const [query, setQuery] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'f') {
        e.preventDefault()
        searchRef.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const visible = useMemo(() => {
    const q = query.toLowerCase().trim()
    return groups
      .map((g) => ({
        ...g,
        items: g.items.filter((it) => !(hidden?.(it) ?? false) && (!q || it.label.toLowerCase().includes(q))),
      }))
      .filter((g) => g.items.length > 0)
  }, [groups, query, hidden])

  const link = (item: SidebarNavItem) => {
    const active = item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to)
    const Icon = item.icon
    return (
      <NavLink
        key={item.to}
        to={item.to}
        onClick={onClose}
        title={collapsed ? item.label : undefined}
        className={`group flex w-full items-center rounded-[9px] text-[12.5px] font-semibold transition ${
          collapsed ? 'justify-center py-2.5' : 'gap-[11px] px-[11px] py-2.5'
        } ${active ? 'text-white' : 'text-sidebar-text hover:text-white'}`}
        style={active ? { background: 'rgba(52, 106, 255, .2)', boxShadow: 'inset 2px 0 0 #3b82f6' } : undefined}
        onMouseEnter={(e) => { if (!active) e.currentTarget.style.background = 'rgba(255,255,255,.06)' }}
        onMouseLeave={(e) => { if (!active) e.currentTarget.style.background = 'transparent' }}
      >
        <Icon size={17} strokeWidth={1.8} className={collapsed ? '' : 'shrink-0'} />
        {!collapsed && <span className="truncate">{item.label}</span>}
      </NavLink>
    )
  }

  return (
    <>
      {open && <div className="fixed inset-0 z-40 bg-navy/50 lg:hidden" onClick={onClose} />}
      <aside
        className={`fixed inset-y-0 left-0 z-50 flex flex-col text-sidebar-text transition-all duration-200 lg:translate-x-0 ${
          open ? 'translate-x-0' : '-translate-x-full'
        } ${collapsed ? 'w-[68px]' : 'w-[248px]'}`}
        style={{
          background:
            'radial-gradient(circle at 20% 0%, rgba(37, 99, 235, .2), transparent 31%), linear-gradient(180deg, #0c1526, #09111e 100%)',
        }}
      >
        {/* Brand */}
        <div
          className={`flex min-h-[70px] items-center border-b border-white/[.07] ${collapsed ? 'justify-center px-2' : 'gap-3 px-5 py-[18px]'}`}
        >
          <div
            className="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px] text-white"
            style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)', boxShadow: '0 8px 18px rgba(37,99,235,.27)' }}
          >
            <CloudCog size={18} strokeWidth={1.8} />
          </div>
          {!collapsed && (
            <div className="min-w-0">
              <strong className="block text-[15px] font-bold tracking-[-.02em] text-white">EpicHost</strong>
              <span className="mt-px block text-[11px] text-[#7f8ca2]">Hosting Control Center</span>
            </div>
          )}
          {!collapsed && (
            <button
              onClick={() => onToggleCollapsed(!collapsed)}
              title={collapsed ? 'Expand' : 'Collapse'}
              className="ml-auto hidden h-7 w-7 place-items-center rounded-[7px] border border-white/10 text-[#8794aa] transition hover:bg-white/10 hover:text-white lg:grid"
            >
              <ChevronLeft size={14} />
            </button>
          )}
        </div>
        {collapsed && (
          <button
            onClick={() => onToggleCollapsed(!collapsed)}
            title="Expand"
            className="mx-auto mt-2 hidden h-7 w-7 place-items-center rounded-[7px] border border-white/10 text-[#8794aa] transition hover:bg-white/10 hover:text-white lg:grid"
          >
            <ChevronRight size={14} />
          </button>
        )}

        {/* Tool search */}
        <div className={collapsed ? 'px-2.5 pt-3' : 'px-4 pb-1 pt-4'}>
          {collapsed ? (
            <button
              onClick={() => searchRef.current?.focus()}
              className="grid h-[33px] w-full place-items-center rounded-[8px] border border-white/[.07] bg-white/[.04] text-[#8794aa]"
              title="Search tools (Ctrl+F)"
            >
              <Search size={14} />
            </button>
          ) : (
            <div className="relative">
              <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[#64738a]" />
              <input
                ref={searchRef}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search Tools (Ctrl+F)"
                className="h-[33px] w-full rounded-[8px] border border-white/[.07] bg-white/[.04] pl-8 pr-2 text-[11.5px] text-white placeholder:text-[#64738a] outline-none transition focus:border-[#3b82f6] focus:bg-white/[.08]"
              />
            </div>
          )}
        </div>

        {/* Top slot (org switcher etc.) */}
        {!collapsed && top}

        {/* Nav */}
        <nav className={`flex-1 overflow-y-auto overflow-x-hidden pb-4 ${collapsed ? 'px-2 pt-3' : 'px-2.5 pt-3'}`}>
          {visible.map((group, gi) => (
            <div key={gi} className="mb-[5px]">
              {group.section && !collapsed && (
                <div className="px-2 pb-2 pt-3 text-[10px] font-extrabold uppercase tracking-[.12em] text-[#64738a]">
                  {group.section}
                </div>
              )}
              {collapsed && gi > 0 && <div className="mx-auto mb-2 h-px w-8 bg-white/[.06]" />}
              <div className="space-y-[5px]">
                {group.items.map(link)}
              </div>
            </div>
          ))}
          {visible.length === 0 && !collapsed && (
            <div className="px-2.5 py-3 text-[11px] text-[#64738a]">No tools match “{query}”.</div>
          )}
        </nav>

        {/* Bottom: account pill + logout */}
        <div className="mt-auto border-t border-white/[.06] p-3">
          <div className={`rounded-[11px] border border-white/[.07] bg-white/[.035] p-[11px] ${collapsed ? 'px-2' : ''}`}>
            {collapsed ? (
              <div className="flex flex-col items-center gap-1.5">
                <div className="grid h-[30px] w-[30px] place-items-center rounded-full bg-white/10 text-[11px] font-extrabold text-white">
                  {(account.name ?? 'U').charAt(0).toUpperCase()}
                </div>
                <span className="status-dot" />
              </div>
            ) : (
              <>
                <div className="mb-[7px] flex items-center justify-between gap-2.5">
                  <span className="text-[11px] font-bold text-[#edf2fb]">{account.orgName ?? 'No workspace'}</span>
                  <span className="status-dot" />
                </div>
                <small className="block truncate text-[10px] text-[#77869c]">
                  {account.name ?? '...'} · {account.roleLabel}
                </small>
              </>
            )}
          </div>
          <button
            onClick={account.onLogout}
            title="Log out"
            className={`mt-2 flex w-full items-center justify-center gap-2 rounded-[9px] border border-white/10 px-2 py-2 text-[11px] font-bold text-sidebar-text transition hover:bg-white/10 hover:text-white ${
              collapsed ? 'px-0' : ''
            }`}
          >
            <LogOut size={14} /> {!collapsed && 'Log out'}
          </button>
        </div>
      </aside>
    </>
  )
}
