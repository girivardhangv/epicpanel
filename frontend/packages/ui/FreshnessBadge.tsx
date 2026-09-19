import type { FreshnessState } from '@epicpanel/core'

export function FreshnessBadge(_props: { state?: FreshnessState; ageMs?: number; label?: string }) {
  // Live metrics freshness badges (e.g. "Metrics · STALE 49.7 Sec ago") removed per user request.
  return null
}

