import React, { ReactNode, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ChevronRight, Search, CheckCircle2, AlertTriangle, Info, X } from 'lucide-react'

/* ---------------------------------------------------------------
   Page title block — reference: h1 + subtitle + actions row
---------------------------------------------------------------- */
export function PageTitle({ title, subtitle, actions }: {
  title: ReactNode
  subtitle?: string
  actions?: ReactNode
}) {
  return (
    <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">{title}</h1>
        {subtitle && <p className="mt-[5px] text-[12px] text-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

/* ---------------------------------------------------------------
   Breadcrumbs — UX kit: Home / Section / Page with back action
---------------------------------------------------------------- */
export interface Crumb {
  label: string
  to?: string
}

export function Breadcrumbs({ crumbs }: { crumbs: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="mb-3 flex flex-wrap items-center gap-1 text-[10.5px]">
      {crumbs.map((c, i) => {
        const last = i === crumbs.length - 1
        return (
          <span key={i} className="flex items-center gap-1">
            {i > 0 && <ChevronRight size={11} className="text-muted" />}
            {c.to && !last ? (
              <Link to={c.to} className="rounded-[6px] px-1.5 py-0.5 font-semibold text-muted transition hover:bg-surface-2 hover:text-brand">
                {c.label}
              </Link>
            ) : (
              <span
                aria-current={last ? 'page' : undefined}
                className={`px-1.5 py-0.5 font-bold ${last ? 'text-ink' : 'text-muted'}`}
              >
                {c.label}
              </span>
            )}
          </span>
        )
      })}
    </nav>
  )
}

/* ---------------------------------------------------------------
   Toolbar — reference: search + selects inside table card headers
---------------------------------------------------------------- */
export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="toolbar">{children}</div>
}

export function ToolbarSearch({ value, onChange, placeholder }: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <div className="relative min-w-[180px] flex-1">
      <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[#98a2b3]" />
      <input
        className="h-[34px] w-full rounded-[8px] border border-line bg-white px-2.5 pl-[31px] text-[11px] outline-none placeholder:text-[#98a2b3] focus:border-[#9db8ff]"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}

/* ---------------------------------------------------------------
   Mini item — reference: icon + copy + chip list rows
---------------------------------------------------------------- */
export function MiniItem({ icon, title, sub, right, tone = 'default' }: {
  icon: ReactNode
  title: ReactNode
  sub?: ReactNode
  right?: ReactNode
  tone?: 'default' | 'green' | 'amber' | 'blue' | 'red' | 'purple'
}) {
  const toneCls: Record<string, string> = {
    default: 'bg-surface-2 text-[#667085]',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    blue: 'bg-brand-soft text-brand',
    red: 'bg-danger-soft text-danger',
    purple: 'bg-purple-soft text-purple',
  }
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <div className={`grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] ${toneCls[tone]}`}>{icon}</div>
      <div className="min-w-0 flex-1">
        <strong className="block truncate text-[11px] text-ink">{title}</strong>
        {sub && <span className="block truncate text-[9.5px] text-muted">{sub}</span>}
      </div>
      {right}
    </div>
  )
}

/* ---------------------------------------------------------------
   Usage card — reference: icon + title + big number + bar + note
---------------------------------------------------------------- */
export function UsageCard({ icon, title, sub, value, pct, note, note2, color = '#2563eb', tone = 'blue', children }: {
  icon: ReactNode
  title: string
  sub: string
  value: ReactNode
  pct: number
  note?: ReactNode
  note2?: ReactNode
  color?: string
  tone?: 'blue' | 'green' | 'amber' | 'purple' | 'red'
  children?: ReactNode
}) {
  const toneCls: Record<string, string> = {
    blue: 'bg-brand-soft text-brand',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    purple: 'bg-purple-soft text-purple',
    red: 'bg-danger-soft text-danger',
  }
  return (
    <div className="card p-[14px]">
      <div className="flex items-start justify-between gap-2.5">
        <div className="flex items-center gap-2">
          <div className={`grid h-[29px] w-[29px] place-items-center rounded-[8px] ${toneCls[tone]}`}>{icon}</div>
          <div>
            <strong className="block text-[10.5px] text-ink">{title}</strong>
            <span className="mt-px block text-[9px] text-muted">{sub}</span>
          </div>
        </div>
        <div className="text-right text-[17px] font-extrabold tracking-[-.03em] text-ink">{value}</div>
      </div>
      <div className="mt-3 h-[7px] w-full overflow-hidden rounded-full bg-line-soft">
        <div className="h-full rounded-full transition-all" style={{ width: `${Math.max(0, Math.min(100, pct))}%`, background: color }} />
      </div>
      {(note || note2) && (
        <div className="mt-1.5 flex justify-between text-[9.5px] text-muted">
          <span>{note}</span>
          <span>{note2}</span>
        </div>
      )}
      {children}
    </div>
  )
}

/* ---------------------------------------------------------------
   Quick action tile — reference: icon + label + desc, hover lift
---------------------------------------------------------------- */
export function QuickAction({ icon, label, desc, to, tone = 'blue' }: {
  icon: ReactNode
  label: string
  desc: string
  to: string
  tone?: 'blue' | 'green' | 'amber' | 'purple' | 'red'
}) {
  const toneCls: Record<string, string> = {
    blue: 'bg-brand-soft text-brand',
    green: 'bg-ok-soft text-ok',
    amber: 'bg-warn-soft text-warn',
    purple: 'bg-purple-soft text-purple',
    red: 'bg-danger-soft text-danger',
  }
  return (
    <Link
      to={to}
      className="min-h-[100px] rounded-[12px] border border-line bg-white p-3 text-left transition hover:-translate-y-[2px] hover:border-[#cddaff] hover:shadow-pop"
    >
      <div className={`mb-2 grid h-[31px] w-[31px] place-items-center rounded-[9px] ${toneCls[tone]}`}>{icon}</div>
      <strong className="mb-0.5 block text-[10.5px] text-ink">{label}</strong>
      <span className="block text-[9px] leading-[1.35] text-muted">{desc}</span>
    </Link>
  )
}

/* ---------------------------------------------------------------
   Feature tile — reference "All features" grid (icon + copy)
---------------------------------------------------------------- */
export function FeatureTile({ icon, label, desc, to }: {
  icon: ReactNode
  label: string
  desc: string
  to: string
}) {
  return (
    <Link
      to={to}
      className="flex items-center gap-2.5 rounded-[11px] border border-line bg-white p-3 text-left transition hover:border-[#cddaff] hover:shadow-card"
    >
      <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[9px] border border-line bg-surface-2 text-[#39465a]">{icon}</div>
      <div className="min-w-0">
        <strong className="block truncate text-[10.5px] text-ink">{label}</strong>
        <span className="block truncate text-[9px] text-muted">{desc}</span>
      </div>
    </Link>
  )
}

/* ---------------------------------------------------------------
   Initials avatar
---------------------------------------------------------------- */
export function Initials({ text, size = 27, rounded = 'rounded-[8px]' }: {
  text: string
  size?: number
  rounded?: string
}) {
  const parts = text.trim().split(/[\s.@_-]+/).filter(Boolean)
  const init = (parts[0]?.[0] ?? 'U').toUpperCase() + (parts[1]?.[0] ?? '').toUpperCase()
  return (
    <div
      className={`grid flex-none place-items-center bg-brand-soft font-extrabold text-[#5268ba] ${rounded}`}
      style={{ width: size, height: size, fontSize: Math.round(size * 0.33) }}
    >
      {init}
    </div>
  )
}

/* ---------------------------------------------------------------
   Contextual row actions (ux kit): visible action set for a row
---------------------------------------------------------------- */
export function RowActions({ children }: { children: ReactNode }) {
  return <div className="flex justify-end gap-[5px]">{children}</div>
}

/* ---------------------------------------------------------------
   Toasts — global notifications (no emoji, no animation libs)
---------------------------------------------------------------- */
export interface Toast {
  id: number
  kind: 'success' | 'error' | 'info'
  message: string
}

let toastSeq = 1
const toastSubs = new Set<(t: Toast[]) => void>()
let toasts: Toast[] = []

function emit() {
  const snapshot = toasts.slice()
  toastSubs.forEach((cb) => cb(snapshot))
}

export function pushToast(kind: Toast['kind'], message: string) {
  const t: Toast = { id: toastSeq++, kind, message }
  toasts = [...toasts, t]
  emit()
  window.setTimeout(() => {
    toasts = toasts.filter((x) => x.id !== t.id)
    emit()
  }, 4200)
}

export function Toaster() {
  const [list, setList] = useState<Toast[]>([])
  useEffect(() => {
    toastSubs.add(setList)
    return () => {
      toastSubs.delete(setList)
    }
  }, [])
  if (list.length === 0) return null
  const iconFor = (k: Toast['kind']) =>
    k === 'success' ? <CheckCircle2 size={14} className="text-ok" /> : k === 'error' ? <AlertTriangle size={14} className="text-danger" /> : <Info size={14} className="text-brand" />
  return (
    <div className="pointer-events-none fixed bottom-5 right-5 z-[120] flex w-[300px] flex-col gap-2">
      {list.map((t) => (
        <div key={t.id} className="pointer-events-auto flex items-start gap-2.5 rounded-[11px] border border-line bg-white px-3.5 py-2.5 shadow-toast">
          <span className="mt-px shrink-0">{iconFor(t.kind)}</span>
          <span className="min-w-0 flex-1 break-words text-[11.5px] font-semibold text-ink">{t.message}</span>
          <button
            className="grid h-[20px] w-[20px] flex-none cursor-pointer place-items-center rounded-[6px] text-muted transition hover:bg-surface-2"
            onClick={() => {
              toasts = toasts.filter((x) => x.id !== t.id)
              emit()
            }}
            aria-label="Dismiss"
          >
            <X size={12} />
          </button>
        </div>
      ))}
    </div>
  )
}
