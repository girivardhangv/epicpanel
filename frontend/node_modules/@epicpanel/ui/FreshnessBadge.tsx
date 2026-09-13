import { formatAge } from '@epicpanel/core'
import type { FreshnessState } from '@epicpanel/core'

export function FreshnessBadge({ state, ageMs, label }: { state: FreshnessState; ageMs: number; label?: string }) {
  const cls = state === 'LIVE' ? 'status-live' : state === 'STALE' ? 'status-warning' : 'status-down'
  const detail = state === 'LIVE' ? `updated ${formatAge(ageMs)} ago` : state === 'STALE' ? `last update ${formatAge(ageMs)} ago` : null
  return (
    <span className={`status-chip ${cls}`}>
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {label ? `${label} · ` : ''}
      {state}
      {detail ? ` · ${detail}` : ''}
    </span>
  )
}
