import { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { TrendingUp, TrendingDown, ArrowRight } from 'lucide-react'

const TONE: Record<string, { bg: string; fg: string }> = {
  blue: { bg: '#eef4ff', fg: '#2563eb' },
  green: { bg: '#eaf9f3', fg: '#0f9d6e' },
  amber: { bg: '#fff7e7', fg: '#d88b00' },
  purple: { bg: '#f2edff', fg: '#7c4dff' },
  red: { bg: '#fff0f2', fg: '#dc3d4b' },
}

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`card p-5 ${className}`}>{children}</div>
}

export function CardHeader({ title, subtitle, right, className = '' }: { title: string; subtitle?: string; right?: ReactNode; className?: string }) {
  return (
    <div className={`mb-4 flex items-center justify-between gap-3 border-b border-line pb-3 ${className}`}>
      <div>
        <h3 className="text-[12px] font-bold tracking-[-.01em] text-ink">{title}</h3>
        {subtitle && <span className="mt-0.5 block text-[10px] text-muted">{subtitle}</span>}
      </div>
      {right}
    </div>
  )
}

export function StatCard({ icon, value, label, change, up, tone = 'blue', linkTo, linkLabel, loading, children }: {
  icon: ReactNode
  value: ReactNode
  label: string
  change?: string
  up?: boolean
  tone?: 'blue' | 'green' | 'amber' | 'purple' | 'red'
  linkTo?: string
  linkLabel?: string
  loading?: boolean
  children?: ReactNode
}) {
  const t = TONE[tone] ?? TONE.blue
  return (
    <div className="card min-w-0 p-[15px]">
      <div className="flex items-start justify-between gap-2.5">
        <div className="min-w-0">
          <div className="text-[10px] font-bold uppercase tracking-[.06em] text-muted">{label}</div>
          {loading ? (
            <div className="skeleton mt-2 h-7 w-16" />
          ) : (
            <div className="mt-[5px] text-[24px] font-extrabold leading-[1.1] tracking-[-.04em] text-ink">{value}</div>
          )}
          {change && (
            <div className={`mt-2 inline-flex items-center gap-1 text-[10px] font-bold ${up ? 'text-ok' : 'text-danger'}`}>
              {up ? <TrendingUp size={12} /> : <TrendingDown size={12} />} {change}
            </div>
          )}
        </div>
        <div className="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px]" style={{ background: t.bg, color: t.fg }}>
          {icon}
        </div>
      </div>
      {children}
      {linkTo && (
        <Link to={linkTo} className="mt-3 inline-flex items-center gap-1 text-[11px] font-bold text-brand hover:underline">
          {linkLabel ?? 'View all'} <ArrowRight size={12} />
        </Link>
      )}
    </div>
  )
}

export function ProgressBar({ pct, color = '#2563eb' }: { pct: number; color?: string }) {
  return (
    <div className="h-[7px] w-full overflow-hidden rounded-full bg-line-soft">
      <div
        className="h-full rounded-full transition-all"
        style={{ width: `${Math.min(pct, 100)}%`, background: color }}
      />
    </div>
  )
}

export function ProgressRing({ pct, label, sub, color = '#0f9d6e', size = 88 }: {
  pct: number
  label: string
  sub?: string
  color?: string
  size?: number
}) {
  const r = size / 2 - 6
  const c = 2 * Math.PI * r
  return (
    <div className="flex flex-col items-center gap-1.5">
      <div className="relative" style={{ width: size, height: size }}>
        <svg viewBox={`0 0 ${size} ${size}`} className="h-full w-full -rotate-90">
          <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="#eef1f6" strokeWidth="6" />
          <circle
            cx={size / 2} cy={size / 2} r={r} fill="none" stroke={color} strokeWidth="6" strokeLinecap="round"
            strokeDasharray={c} strokeDashoffset={c - (c * Math.min(pct, 100)) / 100}
            style={{ transition: 'stroke-dashoffset .6s ease' }}
          />
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <div className="text-[15px] font-extrabold tracking-[-.03em] text-ink">{Math.round(pct)}%</div>
        </div>
      </div>
      <div className="text-center">
        <div className="text-[11px] font-bold text-ink">{label}</div>
        {sub && <div className="text-[10px] text-muted">{sub}</div>}
      </div>
    </div>
  )
}

export function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { cls: string; label: string }> = {
    ready: { cls: 'badge-ok', label: 'Online' },
    online: { cls: 'badge-ok', label: 'Online' },
    healthy: { cls: 'badge-ok', label: 'Healthy' },
    successful: { cls: 'badge-ok', label: 'Success' },
    active: { cls: 'badge-ok', label: 'Active' },
    available: { cls: 'badge-ok', label: 'Available' },
    resolved: { cls: 'badge-ok', label: 'Resolved' },
    provisioning: { cls: 'badge-warn', label: 'Provisioning' },
    creating: { cls: 'badge-warn', label: 'Creating' },
    installing: { cls: 'badge-warn', label: 'Installing' },
    pending: { cls: 'badge-warn', label: 'Pending' },
    warning: { cls: 'badge-warn', label: 'Warning' },
    deleting: { cls: 'badge-warn', label: 'Deleting' },
    running: { cls: 'badge-warn', label: 'Running' },
    failed: { cls: 'badge-off', label: 'Failed' },
    offline: { cls: 'badge-off', label: 'Offline' },
    suspended: { cls: 'badge-off', label: 'Suspended' },
    disabled: { cls: 'badge-off', label: 'Disabled' },
  }
  const m = map[status] ?? { cls: 'badge-neutral', label: status }
  return <span className={m.cls}><span className="h-1.5 w-1.5 rounded-full bg-current" />{m.label}</span>
}

export function EmptyState({ title, subtitle, action, icon }: {
  title: string
  subtitle: string
  action?: ReactNode
  icon?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center py-12 text-center">
      {icon && (
        <div className="mb-4 grid h-12 w-12 place-items-center rounded-[12px] border border-line bg-surface-2 text-muted">
          {icon}
        </div>
      )}
      <div className="text-[14px] font-bold tracking-[-.01em] text-ink">{title}</div>
      <div className="mt-1 max-w-xs text-[12px] text-muted">{subtitle}</div>
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

export function SkeletonRows({ rows = 3, height = 'h-16' }: { rows?: number; height?: string }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className={`skeleton ${height} w-full`} />
      ))}
    </div>
  )
}
