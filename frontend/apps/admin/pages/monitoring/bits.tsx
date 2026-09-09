// Shared monitoring UI bits: state chips, section shell, the verbatim
// overview tree, and the light poll hook that backs the observability
// endpoints (live VALUES always come from the WS stream, never the poll).
import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { formatAge, timeAgo } from '@epicpanel/core'
import type { FreshnessState } from '@epicpanel/core'
import { Breadcrumbs } from '@epicpanel/ui'

export type UIState = FreshnessState

export function StateChip({ state, ageMs, label }: { state: UIState; ageMs?: number; label?: string }) {
  const cls = state === 'LIVE' ? 'status-live' : state === 'STALE' ? 'status-warning' : 'status-down'
  const detail =
    state === 'OFFLINE' ? '' : ageMs !== undefined && Number.isFinite(ageMs) && ageMs > 0 ? ` · ${formatAge(ageMs)}` : ''
  return (
    <span className={`status-chip ${cls}`}>
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {label ? `${label} · ` : ''}
      {state}
      {detail}
    </span>
  )
}

export function SeverityChip({ severity }: { severity: string }) {
  if (severity === 'critical') return <span className="status-chip status-down">critical</span>
  if (severity === 'warning') return <span className="status-chip status-warning">warning</span>
  return <span className="badge-neutral">info</span>
}

/**
 * usePolled — fetch helper for the observability snapshots. These endpoints
 * are LiveStore projections (not the WS stream itself), so a slow refresh
 * keeps the row list current while per-value freshness still comes from the
 * WS frames joined client-side. Returns null while loading, [] when empty or
 * when the request fails (the UI says which).
 */
export function usePolled<T>(fetcher: () => Promise<T>, deps: unknown[], intervalMs = 15000): { data: T | null; failed: boolean; reload: () => void } {
  const [data, setData] = useState<T | null>(null)
  const [failed, setFailed] = useState(false)
  const reload = useCallback(() => {
    fetcher()
      .then((r) => {
        setData(r)
        setFailed(false)
      })
      .catch(() => setFailed(true))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
  useEffect(() => {
    reload()
    if (intervalMs <= 0) return
    const t = window.setInterval(reload, intervalMs)
    return () => window.clearInterval(t)
  }, [reload, intervalMs])
  return { data, failed, reload }
}

export interface SectionShellProps {
  crumb: string
  title: string
  subtitle: string
  actions?: React.ReactNode
  children: React.ReactNode
}

export function SectionShell({ crumb, title, subtitle, actions, children }: SectionShellProps) {
  return (
    <div className="fade-up">
      <Breadcrumbs crumbs={[{ label: 'Monitoring', to: '/monitoring' }, { label: crumb }]} />
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">{title}</h1>
          <p className="mt-[5px] text-[12px] text-muted">{subtitle}</p>
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  )
}

export function FailedNote({ what, onRetry }: { what: string; onRetry: () => void }) {
  return (
    <div className="mb-3 flex items-center justify-between gap-3 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">
      <span>{what} unavailable — the request failed.</span>
      <button className="btn-ghost !min-h-[28px] !px-2 text-[10px]" onClick={onRetry}>Retry</button>
    </div>
  )
}

/** "Raised 4 min ago · 3 occurrences" secondary line shared by feed rows. */
export function AlertMetaLine({ created, occurrences, lastSeen }: { created: string; occurrences: number; lastSeen?: string | null }) {
  return (
    <span className="table-secondary">
      raised {timeAgo(created)}
      {occurrences > 1 ? ` · ${occurrences} occurrences` : ''}
      {lastSeen ? ` · last seen ${timeAgo(lastSeen)}` : ''}
    </span>
  )
}
