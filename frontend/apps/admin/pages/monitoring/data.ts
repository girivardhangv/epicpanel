// Phase 13 monitoring data layer: typed wire shapes for the alerts API
// (GET/POST /v1/admin/alerts*, /v1/admin/alert-rules*) and the observability
// drill-downs (GET /v1/admin/observability/*) — field names are the verbatim
// JSON contract from backend/internal/api/phase13_alerts.go. The admin surface
// is platform-admin-session-only; authorization is enforced server-side.

/**
 * Observability endpoints snapshot the server LiveStore per request. The
 * nodes projection currently leaves disk_percent and the network counters at
 * zero (backend gap); the UI fills those columns from the WS frames when a
 * frame for the node exists (P6 fleet-freshness pattern) and shows "—" when
 * no source has a real value.
 */
export interface ObsNode {
  server_id: string
  name: string
  state: string
  cpu_percent: number
  ram_percent: number
  disk_percent: number
  net_rx_bps: number
  net_tx_bps: number
  age_seconds: number
}

export interface ObsService {
  server_id: string
  service: string
  up: boolean
  node_state: string
}

export interface ObsCustomer {
  website_id: string
  server_id: string
  state: string
  cpu_percent: number
  ram_percent: number
  disk_used_mb: number
  bandwidth_bps: number
}

export interface AlertRow {
  id: string
  organization_id?: string | null
  type: string
  severity: string
  resource_type: string
  resource_id: string
  resource_name: string
  message: string
  rule_id?: string | null
  occurrences: number
  last_seen_at?: string | null
  acknowledged_at?: string | null
  resolved_at?: string | null
  created_at: string
}

export type AlertState = 'active' | 'ack' | 'resolved' | 'all'

export type RuleClass = 'threshold' | 'state' | 'time'

export interface RuleRow {
  id: string
  name: string
  description: string
  rule_class: RuleClass
  metric: string
  scope: string
  scope_id: string
  comparison: string
  threshold: number
  duration_seconds: number
  recovery_margin: number
  window_days: number
  severity: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface RuleDraft {
  name: string
  description: string
  rule_class: RuleClass
  metric: string
  scope: string
  scope_id: string
  comparison: string
  threshold: string
  duration_seconds: string
  recovery_margin: string
  window_days: string
  severity: string
}

export const emptyRuleDraft: RuleDraft = {
  name: '',
  description: '',
  rule_class: 'threshold',
  metric: 'cpu',
  scope: 'fleet',
  scope_id: '',
  comparison: 'gt',
  threshold: '90',
  duration_seconds: '120',
  recovery_margin: '5',
  window_days: '30',
  severity: 'warning',
}

/** Verbatim metric catalog — must mirror validMetric() on the backend. */
export const METRICS: Record<RuleClass, { value: string; label: string; unit: string }[]> = {
  threshold: [
    { value: 'cpu', label: 'Node CPU', unit: '%' },
    { value: 'ram', label: 'Node RAM', unit: '%' },
    { value: 'disk', label: 'Node disk', unit: '%' },
  ],
  state: [
    { value: 'node_offline', label: 'Node offline', unit: '' },
    { value: 'service_down', label: 'Service down', unit: '' },
    { value: 'container_crashed', label: 'Container crashed', unit: '' },
    { value: 'backup_failed', label: 'Backup failed', unit: '' },
    { value: 'provisioning_failed', label: 'Provisioning failed', unit: '' },
  ],
  time: [{ value: 'ssl_expiration', label: 'SSL expiration', unit: 'days' }],
}

export const SCOPES = [
  { value: 'fleet', label: 'Fleet (everything)' },
  { value: 'node', label: 'Node (server id)' },
  { value: 'account', label: 'Account (website id)' },
  { value: 'plan', label: 'Plan (package name)' },
]

export const SEVERITIES = [
  { value: 'info', label: 'Info' },
  { value: 'warning', label: 'Warning' },
  { value: 'critical', label: 'Critical' },
]

export function metricLabel(metric: string): string {
  for (const cls of Object.keys(METRICS) as RuleClass[]) {
    const hit = METRICS[cls].find((m) => m.value === metric)
    if (hit) return hit.label
  }
  return metric
}

export function metricUnit(metric: string): string {
  for (const cls of Object.keys(METRICS) as RuleClass[]) {
    const hit = METRICS[cls].find((m) => m.value === metric)
    if (hit) return hit.unit
  }
  return ''
}

/** Human one-line description of what a rule fires on (hysteresis included). */
export function ruleTrigger(r: RuleRow): string {
  if (r.rule_class === 'time') {
    return `expires within ${r.window_days} day(s)`
  }
  if (r.rule_class === 'state') {
    return 'fires while the condition holds, auto-resolves on recovery'
  }
  const cmp = r.comparison === 'lt' ? '<' : '>'
  const unit = metricUnit(r.metric)
  const dur = r.duration_seconds > 0 ? ` for ${r.duration_seconds}s` : ''
  const rec = r.recovery_margin > 0 ? ` · clears at ${r.comparison === 'lt' ? '+' : '-'}${r.recovery_margin}${unit}` : ''
  return `${cmp} ${r.threshold}${unit === '%' ? '%' : ` ${unit}`.trimEnd()}${dur}${rec}`
}

/** ONLINE/STALE/OFFLINE (backend NodeState) -> LIVE/STALE/OFFLINE (UI). */
export function liveState(nodeState: string): 'LIVE' | 'STALE' | 'OFFLINE' {
  if (nodeState === 'ONLINE' || nodeState === 'LIVE') return 'LIVE'
  if (nodeState === 'STALE') return 'STALE'
  return 'OFFLINE'
}

export function shortId(id: string | null | undefined): string {
  if (!id) return '—'
  return id.length > 8 ? id.slice(0, 8) : id
}
