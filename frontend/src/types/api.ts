// Shared API payload shapes mirroring backend JSON contracts.

export interface CurrentUser {
  id: string;
  username: string;
  email?: string | null;
  display_name?: string;
  permissions: string[];
}

export interface InstallerStatus {
  installed: boolean;
  version: string;
  steps: Record<string, boolean>;
}

export interface RequirementCheck {
  name: string;
  severity: "ok" | "warn" | "error";
  passed: boolean;
  message: string;
  value?: string;
}

export interface RequirementsReport {
  version: string;
  os: string;
  arch: string;
  checks: RequirementCheck[];
}

export interface LicenseInfo {
  status: "inactive" | "active" | "grace" | "expired" | "suspended" | "invalid";
  plan?: string;
  seats?: number | null;
  key_hint?: string;
  external_id?: string;
  features?: string[];
  activated_at?: string | null;
  last_validated_at?: string | null;
  expires_at?: string | null;
  fingerprint?: string;
}

export interface ServerView {
  id: string;
  label: string;
  hostname: string;
  os: string;
  os_version: string;
  arch: string;
  agent_version: string;
  status: string;
  registered_ip: string;
  registered_at: string;
  last_seen_at: string | null;
  agent_url: string;
  manageable: boolean;
  capabilities?: ServerCapabilities;
  specs?: {
    cpu?: { cores_logical?: number; model?: string };
    memory?: { total_mb?: number; available_mb?: number };
    disks?: Array<{ mountpoint: string; filesystem?: string; total_mb?: number; free_mb?: number }>;
  };
  online: boolean;
}

export interface PHPVersionInfo {
  version: string;
  binary_path: string;
  config_path?: string;
  handler_type: "fpm" | "fastcgi";
  status: string;
}

export interface NginxStatusInfo {
  installed: boolean;
  version?: string;
  config_path?: string;
  running: boolean;
  style?: string;
}

export interface ServerCapabilities {
  probed_at?: string;
  reachable: boolean;
  error?: string;
  nginx?: NginxStatusInfo;
  php?: PHPVersionInfo[];
  provisioning: boolean;
  log_access: boolean;
}

export interface RegistrationTokenView {
  id: string;
  label: string;
  created_by?: string | null;
  expires_at: string;
  used_at?: string | null;
  used_by_server?: string | null;
  revoked_at?: string | null;
  created_at: string;
}

export interface DomainView {
  id: string;
  server_id: string;
  domain: string;
  type: "primary" | "alias" | "subdomain";
  status: string;
  website_id?: string | null;
  website_name?: string | null;
  created_at: string;
  updated_at: string;
}

export type WebsiteStatus = "provisioning" | "active" | "disabled" | "error" | "deleting";

export interface JobView {
  id: string;
  type: string;
  status: "queued" | "running" | "completed" | "failed" | "cancelled";
  server_id?: string | null;
  website_id?: string | null;
  progress: number;
  message: string;
  error: string;
  created_at: string;
  started_at?: string | null;
  completed_at?: string | null;
}

export interface WebsiteView {
  id: string;
  server_id: string;
  server_name: string;
  server_os: string;
  server_online: boolean;
  domain_id: string;
  domain: string;
  aliases: string[];
  name: string;
  document_root: string;
  status: WebsiteStatus;
  php_version: string;
  web_server: string;
  os_user?: string;
  cpu_limit_pct?: number;
  memory_limit_mb?: number;
  php_settings?: Record<string, string>;
  active_job_status?: string;
  created_at: string;
  updated_at: string;
}

export interface LogPage {
  path: string;
  size_bytes: number;
  truncated: boolean;
  content: string;
}

// ---------------------------------------------------------------------------
// Phase 3 — monitoring, telemetry & server health
// ---------------------------------------------------------------------------

export interface DiskMetric {
  mount: string;
  fs?: string;
  total_bytes: number;
  used_bytes: number;
  free_bytes: number;
  usage_percent: number;
}

export interface NetworkMetric {
  interface: string;
  rx_bytes: number;
  tx_bytes: number;
  rx_packets: number;
  tx_packets: number;
  errors: number;
  drops: number;
}

export interface ProcessMetric {
  name: string;
  pid: number;
  cpu_percent: number | null;
  memory_bytes: number;
  status: string;
}

export interface ServiceHealth {
  name: string;
  display_name: string;
  status: "Running" | "Stopped" | "Failed" | "Unknown" | "NotInstalled";
  running: boolean;
  enabled?: boolean | null;
  last_checked?: string;
}

export interface HealthPoint {
  component: string;
  value: number | null;
  state: string;
}

export interface HealthStateView {
  state: string;
  points: HealthPoint[];
  basis: number;
}

export interface ThresholdsView {
  cpu_warn: number;
  cpu_crit: number;
  mem_warn: number;
  mem_crit: number;
  disk_warn: number;
  disk_crit: number;
  offline_after_seconds: number;
}

export interface CurrentMetrics {
  server_id: string;
  has_data: boolean;
  latest?: {
    sequence: number;
    agent_timestamp: string | null;
    server_received_at: string;
    cpu_usage_percent: number | null;
    cpu_user_percent: number | null;
    cpu_system_percent: number | null;
    cpu_idle_percent: number | null;
    load_1m: number | null;
    load_5m: number | null;
    load_15m: number | null;
    memory_usage_percent: number | null;
    memory_total_bytes: number | null;
    memory_used_bytes: number | null;
    memory_available_bytes: number | null;
    swap_usage_percent: number | null;
    uptime_seconds: number | null;
    disk: DiskMetric[];
    network: NetworkMetric[];
    processes: ProcessMetric[];
    services: ServiceHealth[];
  };
  health: HealthStateView;
  thresholds: ThresholdsView;
  monitoring_capabilities?: Record<string, string>;
}

export interface HistoryPoint {
  t: string;
  cpu_usage: number | null;
  memory_usage: number | null;
  load_1m: number | null;
  load_5m: number | null;
  load_15m: number | null;
  swap_usage: number | null;
  max_disk_usage: number | null;
}

export interface HistoryView {
  range: string;
  interval_seconds: number;
  source: "raw" | "hourly" | "daily";
  points: HistoryPoint[];
}

export interface NetworkPoint {
  t: string;
  interface: string;
  rx_bytes: number;
  tx_bytes: number;
  rx_mbps: number | null;
  tx_mbps: number | null;
}

export interface NetworkSeriesView {
  range_seconds: number;
  interfaces: Record<string, { points: NetworkPoint[] }>;
}

export interface FleetServer {
  server_id: string;
  name: string;
  hostname: string;
  status: string;
  online: boolean;
  last_seen_at: string | null;
  cpu_usage: number | null;
  memory_usage: number | null;
  max_disk_usage: number | null;
  uptime_hours: number | null;
  health: string;
}

export interface AlertRuleView {
  id: string;
  name: string;
  rule_type: string;
  threshold: number | null;
  duration_seconds: number;
  severity: "warning" | "critical";
  enabled: boolean;
}

export interface AlertView {
  id: string;
  rule_id: string;
  rule_name: string;
  rule_type: string;
  server_id: string | null;
  server_name: string;
  status: "triggered" | "acknowledged" | "resolved";
  severity: "warning" | "critical";
  metric_value: number | null;
  threshold: number | null;
  message: string;
  triggered_at: string;
  acknowledged_at?: string | null;
  resolved_at?: string | null;
}

export interface WebsiteHealthView {
  has_data: boolean;
  website_status: string;
  nginx: { status: string; running: boolean };
  php: { status: string; running: boolean };
  php_version: string;
  configuration: { status: string };
  server: { status: string };
}

// ---------------------------------------------------------------------------
// Phase 4 — SSL certificates
// ---------------------------------------------------------------------------

export interface WebsiteCertificate {
  id: string;
  website_id: string;
  provider: "acme" | "mock";
  domains: string[];
  status: string;
  cert_path: string;
  key_path: string;
  auto_renew: boolean;
  issued_at?: string | null;
  expires_at?: string | null;
  error?: string;
}

// ---------------------------------------------------------------------------
// Phase 5 — notification channels + settings
// ---------------------------------------------------------------------------

export interface NotificationChannel {
  id: string;
  name: string;
  type: "webhook" | "slack" | "discord" | "email";
  config: Record<string, unknown>;
  severity: "warning" | "critical";
  enabled: boolean;
  created_at: string;
}

export type EditableSettings = Record<string, number | string>;

// ---------------------------------------------------------------------------
// Phase 6 — managed databases
// ---------------------------------------------------------------------------

export type DatabaseEngine = "mysql" | "postgres";
export type DatabaseStatus = "provisioning" | "active" | "error" | "deleting";

export interface DatabaseUser {
  id: string;
  username: string;
  status: string;
  created_at: string;
}

export interface DatabaseView {
  id: string;
  server_id: string;
  server_name: string;
  website_id?: string | null;
  engine: DatabaseEngine;
  name: string;
  status: DatabaseStatus;
  error?: string;
  users: DatabaseUser[];
  created_at: string;
  updated_at: string;
}

export interface DBEngineStatus {
  configured: boolean;
  available: boolean;
  version?: string;
  error?: string;
}

export interface DBEnginesView {
  mysql: DBEngineStatus;
  postgres: DBEngineStatus;
}

// ---------------------------------------------------------------------------
// Phase 7 — software manager
// ---------------------------------------------------------------------------

export interface SoftwareComponent {
  name: string;
  display_name: string;
  category: string;
  installed: boolean;
  managed?: boolean;
  location?: string;
  version?: string;
  service?: string;
  running: boolean;
  supported: boolean;
  source?: string;
}

export interface SoftwareOS {
  Distro: string;
  Family: string;
  Arch: string;
  PackageManager: string;
}

export interface SoftwareListResult {
  os: SoftwareOS;
  components: SoftwareComponent[];
  dir?: string;
}

export interface AuditEvent {
  id: number;
  actor_type: string;
  actor_label: string;
  action: string;
  resource: string;
  resource_id: string;
  ip: string;
  created_at: string;
}

export interface UserView {
  id: string;
  username: string;
  email?: string | null;
  display_name?: string;
  is_active: boolean;
  roles: string[];
  last_login_at?: string | null;
  created_at: string;
}

export interface RoleView {
  id: string;
  name: string;
  description?: string;
  is_system: boolean;
  permissions: string[];
  user_count: number;
}

export interface PermissionView {
  code: string;
  description: string;
}

export interface DashboardSummary {
  servers_total: number;
  servers_online: number;
  users_count: number;
  sessions_active: number;
  license: { status: LicenseInfo["status"]; plan?: string; expires_at?: string | null };
  recent_events: AuditEvent[];
}

export type Permission =
  | "dashboard.view"
  | "server.view"
  | "server.manage"
  | "servers.create"
  | "servers.delete"
  | "users.view"
  | "users.create"
  | "users.edit"
  | "users.delete"
  | "roles.view"
  | "roles.create"
  | "roles.edit"
  | "roles.delete"
  | "domains.view"
  | "domains.create"
  | "domains.manage"
  | "domains.delete"
  | "websites.view"
  | "websites.create"
  | "websites.edit"
  | "websites.delete"
  | "websites.logs.view"
  | "websites.php.manage"
  | "websites.config.manage"
  | "monitoring.view"
  | "monitoring.server.view"
  | "monitoring.website.view"
  | "monitoring.processes.view"
  | "monitoring.services.view"
  | "databases.view"
  | "databases.create"
  | "databases.manage"
  | "databases.delete"
  | "databases.users.manage"
  | "settings.view"
  | "settings.manage"
  | "license.view"
  | "license.manage";
