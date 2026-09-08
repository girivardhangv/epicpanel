// Phase 6 admin data layer: typed adminview reads + action helpers on top
// of the shared @epicpanel/core api client (no second HTTP stack).
import { api } from '@epicpanel/core'

export interface AdminServer {
  id: string
  organization_id: string
  name: string
  hostname: string
  os_info: string
  agent_version: string
  status: string
  maintenance_mode: boolean
  enrolled_at?: string | null
  last_seen_at?: string | null
  created_at: string
  websites: number
  databases: number
  jobs_active: number
  runtimes: { type: string; version: string; status: string }[]
}

export interface AdminAccount {
  id: string
  organization_id: string
  org_name: string
  server_id: string
  server_name: string
  name: string
  primary_domain: string
  runtime: string
  web_server: string
  plan?: string | null
  status: string
  backend_port: number
  usage_cpu_percent: number
  usage_memory_bytes: number
  usage_disk_mb: number
  created_at: string
}

export interface AdminOrg {
  id: string
  name: string
  slug: string
  plan?: string | null
  websites: number
  databases: number
  domains: number
  owners: string
  created_at: string
}

export interface AdminDomain {
  id: string
  org_name: string
  website_id: string
  website_name: string
  server_name: string
  domain: string
  kind: string
  ssl_mode: string
  ssl_state: string
  ssl_expires_at?: string | null
  created_at: string
}

export interface AdminDatabase {
  id: string
  org_name: string
  server_name: string
  website_name?: string | null
  engine: string
  name: string
  db_user: string
  status: string
  created_at: string
}

export interface AdminBackup {
  id: string
  org_name: string
  website_name: string
  type: string
  status: string
  trigger_type: string
  size_bytes: number
  error: string
  created_at: string
  finished_at?: string | null
}

export interface AdminZone {
  id: string
  org_name: string
  website_id: string
  website_name: string
  domain: string
  ttl: number
  serial: number
  status: string
  records: number
  updated_at: string
}

export interface AdminAlert {
  id: string
  org_name: string
  type: string
  severity: string
  resource_type: string
  resource_id: string
  resource_name: string
  message: string
  created_at: string
  resolved_at?: string | null
}

export interface AdminUser {
  id: string
  email: string
  name: string
  is_platform_admin: boolean
  status: string
  mfa_enabled: boolean
  orgs: string
  created_at: string
}

export interface ConsoleJob {
  id: string
  server_id: string
  server: string
  website_id?: string
  website: string
  type: string
  status: 'pending' | 'running' | 'success' | 'failed'
  error: string
  attempts: number
  max_attempts: number
  created_at: string
  finished_at?: string
}

export interface Overview {
  accounts: { total: number; active: number; suspended: number; failed: number }
  customers: { orgs: number; users: number }
  domains: { total: number; ssl_active: number; ssl_expiring_30d: number }
  databases: number
  backups: { total: number; last_7d: number; failed_7d: number }
  jobs: { pending: number; running: number; failed: number; success_24h: number }
  alerts: { unresolved: number }
  nodes: { total: number; online: number; stale: number; offline: number; maintenance: number }
  fleet: {
    cpu_pct: number
    mem_used_bytes: number
    mem_total_bytes: number
    disk_used_bytes: number
    disk_total_bytes: number
    rx_bps: number
    tx_bps: number
    sampled_nodes: number
    freshness: { state: 'LIVE' | 'STALE' | 'OFFLINE'; age_ms: number | null }
  }
  distribution: { by_plan: { key: string; count: number }[]; by_status: { key: string; count: number }[]; by_node: { key: string; count: number }[] }
  ports: { apache?: PortPool; ols?: PortPool }
  services: { name: string; ok: number; down: number }[]
  failed_jobs: { id: string; type: string; server: string; website: string; error: string; created_at: string }[]
}

export interface PortPool {
  range: string
  total: number
  allocated: number
}

export const adminview = {
  overview: () => api.get<Overview>('/v1/adminview/overview'),
  servers: () => api.get<{ servers: AdminServer[] }>('/v1/adminview/servers'),
  accounts: (q: Record<string, string> = {}) =>
    api.get<{ accounts: AdminAccount[] }>('/v1/adminview/accounts?' + new URLSearchParams(q).toString()),
  organizations: () => api.get<{ organizations: AdminOrg[] }>('/v1/adminview/organizations'),
  domains: () => api.get<{ domains: AdminDomain[] }>('/v1/adminview/domains'),
  databases: () => api.get<{ databases: AdminDatabase[] }>('/v1/adminview/databases'),
  backups: () => api.get<{ backups: AdminBackup[] }>('/v1/adminview/backups'),
  zones: () => api.get<{ zones: AdminZone[] }>('/v1/adminview/dns-zones'),
  ports: () => api.get<{ apache: PortPool; ols: PortPool; allocations: { server: string; port: number; website: string; org: string; status: string }[]; nodes: { name: string; hostname: string; status: string }[]; note: string }>('/v1/adminview/ports'),
  alerts: (resolved = false) => api.get<{ alerts: AdminAlert[] }>(`/v1/adminview/alerts?resolved=${resolved}`),
  users: () => api.get<{ users: AdminUser[]; security: { service_accounts_active: number; api_tokens_active: number } }>('/v1/adminview/users'),
  jobs: (q: Record<string, string> = {}) =>
    api.get<{ jobs: ConsoleJob[] }>('/v1/adminview/jobs?' + new URLSearchParams(q).toString()),
  deadLetter: () => api.get<{ jobs: ConsoleJob[] }>('/v1/adminview/jobs/dead-letter'),
  live: () => api.get<{ frames: unknown[] }>('/v1/adminview/live'),
  suspend: (websiteId: string) => api.post<{ job_id: string }>(`/v1/adminview/accounts/${websiteId}/suspend`),
  resume: (websiteId: string) => api.post<{ job_id: string }>(`/v1/adminview/accounts/${websiteId}/resume`),
  retryJob: (jobId: string) => api.post<{ status?: string; job_id?: string }>(`/v1/adminview/jobs/${jobId}/retry`),
  cancelJob: (jobId: string) => api.del<null>(`/v1/adminview/jobs/${jobId}/cancel`),
}
