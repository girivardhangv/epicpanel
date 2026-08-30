import { get, post, del, request } from "../lib/api";
import type {
  AlertRuleView,
  AlertView,
  CurrentMetrics,
  CurrentUser,
  DatabaseView,
  DatabaseUser,
  DBEnginesView,
  DomainView,
  EditableSettings,
  FleetServer,
  HistoryView,
  InstallerStatus,
  JobView,
  LicenseInfo,
  LogPage,
  NetworkSeriesView,
  NotificationChannel,
  PermissionView,
  PHPVersionInfo,
  ProcessMetric,
  RegistrationTokenView,
  RequirementsReport,
  RoleView,
  ServerCapabilities,
  ServerView,
  ServiceHealth,
  UserView,
  WebsiteCertificate,
  WebsiteHealthView,
  WebsiteView,
} from "../types/api";

// --- bootstrap / installer ---------------------------------------------------

export const installerApi = {
  status: () => get<InstallerStatus>("/installer/status"),
  requirements: () => post<RequirementsReport>("/installer/requirements"),
  databaseTest: () =>
    post<{ reachable: boolean; message?: string }>("/installer/database/test"),
  databaseConfig: (dsn: string) =>
    post<{ restart_required: boolean }>("/installer/database/config", { dsn }),
  license: (licenseKey: string) =>
    post<LicenseInfo>("/installer/license", { license_key: licenseKey }),
  configuration: (siteName: string, timezone: string) =>
    post<{ site_name: string; timezone: string }>("/installer/configuration", {
      site_name: siteName,
      timezone,
    }),
  administrator: (input: {
    username: string;
    email?: string;
    display_name?: string;
    password: string;
    confirm_password: string;
  }) => post<{ created: boolean }>("/installer/administrator", input),
  security: (input: {
    min_password_length?: number;
    max_failed_logins?: number;
    lockout_minutes?: number;
    session_lifetime_minutes?: number;
  }) => post<{ configured: boolean }>("/installer/security", input),
  complete: () =>
    post<{ completed: boolean; instance_id: string }>("/installer/complete"),
};

// --- auth ---------------------------------------------------------------------

export const authApi = {
  login: (identifier: string, password: string) =>
    post<{ user: CurrentUser }>("/auth/login", { identifier, password }),
  logout: () => post<{ logged_out: boolean }>("/auth/logout"),
  me: () => get<{ user: CurrentUser }>("/auth/me"),
  refresh: () => post<{ refreshed: boolean; permissions?: string[] }>("/auth/refresh"),
  changePassword: (currentPassword: string, newPassword: string, confirmPassword: string) =>
    post<{ changed: boolean }>("/auth/change-password", {
      current_password: currentPassword,
      new_password: newPassword,
      confirm_password: confirmPassword,
    }),
};

// --- product data -------------------------------------------------------------

export const serversApi = {
  list: () => get<{ servers: ServerView[] }>("/servers"),
  get: (id: string) => get<ServerView>(`/servers/${encodeURIComponent(id)}`),
  revoke: (id: string) => del<{ revoked: boolean }>(`/servers/${encodeURIComponent(id)}`),
  // Registration tokens (Phase 2): expire, single-use, revocable.
  createToken: (label: string, expiresHours?: number) =>
    post<{ token: RegistrationTokenView; registration_token: string }>(
      "/servers/registration-tokens",
      { label, expires_hours: expiresHours },
    ),
  listTokens: () => get<{ tokens: RegistrationTokenView[] }>("/servers/registration-tokens"),
  revokeToken: (id: string) =>
    del<{ revoked: boolean }>(`/servers/registration-tokens/${encodeURIComponent(id)}`),
  capabilities: (id: string) =>
    get<ServerCapabilities>(`/servers/${encodeURIComponent(id)}/capabilities`),
  refreshCapabilities: (id: string) =>
    post<ServerCapabilities>(`/servers/${encodeURIComponent(id)}/capabilities`),
  phpVersions: (id: string) =>
    get<{ versions: PHPVersionInfo[]; source: "live" | "cached" }>(
      `/servers/${encodeURIComponent(id)}/php-versions`,
    ),
  // Explicit operator-requested runtime installs (never automatic).
  installNginx: (id: string) =>
    post<{ installed: boolean }>(`/servers/${encodeURIComponent(id)}/install/nginx`),
  installPHP: (id: string, version?: string) =>
    post<{ installed: boolean }>(`/servers/${encodeURIComponent(id)}/install/php`, {
      version: version ?? "",
    }),
  dbEngines: (id: string) =>
    get<DBEnginesView>(`/servers/${encodeURIComponent(id)}/db-engines`),
};

// --- Phase 6: managed databases ---------------------------------------------

export const databasesApi = {
  list: (params?: { server_id?: string; website_id?: string }) => {
    const qs = new URLSearchParams();
    if (params?.server_id) qs.set("server_id", params.server_id);
    if (params?.website_id) qs.set("website_id", params.website_id);
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    return get<{ databases: DatabaseView[] }>(`/databases${suffix}`);
  },
  get: (id: string) => get<DatabaseView>(`/databases/${encodeURIComponent(id)}`),
  create: (input: {
    server_id: string;
    engine: string;
    name: string;
    website_id?: string | null;
  }) => post<{ database: DatabaseView; job: JobView }>("/databases", input),
  delete: (id: string) =>
    del<{ job: JobView }>(`/databases/${encodeURIComponent(id)}`),
  createUser: (id: string, username: string) =>
    post<{ user: DatabaseUser; password: string }>(
      `/databases/${encodeURIComponent(id)}/users`,
      { username },
    ),
  deleteUser: (id: string, userId: string) =>
    del<{ deleted: boolean }>(
      `/databases/${encodeURIComponent(id)}/users/${encodeURIComponent(userId)}`,
    ),
  rotatePassword: (id: string, userId: string) =>
    post<{ password: string }>(
      `/databases/${encodeURIComponent(id)}/users/${encodeURIComponent(userId)}/password`,
    ),
};

export const domainsApi = {
  list: (serverId?: string) =>
    get<{ domains: DomainView[] }>(
      serverId ? `/domains?server_id=${encodeURIComponent(serverId)}` : "/domains",
    ),
  get: (id: string) => get<DomainView>(`/domains/${encodeURIComponent(id)}`),
  create: (input: { server_id: string; domain: string; type?: string }) =>
    post<DomainView>("/domains", input),
  delete: (id: string) => del<{ deleted: boolean }>(`/domains/${encodeURIComponent(id)}`),
};

export const websitesApi = {
  list: (params?: { search?: string; status?: string; server_id?: string }) => {
    const qs = new URLSearchParams();
    if (params?.search) qs.set("search", params.search);
    if (params?.status) qs.set("status", params.status);
    if (params?.server_id) qs.set("server_id", params.server_id);
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    return get<{ websites: WebsiteView[] }>(`/websites${suffix}`);
  },
  get: (id: string) => get<WebsiteView>(`/websites/${encodeURIComponent(id)}`),
  create: (input: {
    server_id: string;
    domain_id: string;
    alias_domain_ids?: string[];
    php_version: string;
    document_root?: string;
  }) => post<{ website: WebsiteView; job: JobView }>("/websites", input),
  update: (
    id: string,
    input: Partial<{
      php_version: string;
      php_settings: Record<string, string>;
      alias_domain_ids: string[];
    }>,
  ) =>
    request<{ job: JobView }>("PATCH", `/websites/${encodeURIComponent(id)}`, { body: input }),
  delete: (id: string, deleteFiles: boolean) =>
    del<{ job: JobView }>(
      `/websites/${encodeURIComponent(id)}?delete_files=${deleteFiles ? "true" : "false"}`,
    ),
  enable: (id: string) =>
    post<{ status: string }>(`/websites/${encodeURIComponent(id)}/enable`),
  disable: (id: string) =>
    post<{ status: string }>(`/websites/${encodeURIComponent(id)}/disable`),
  reload: (id: string) => post<{ reloaded: boolean }>(`/websites/${encodeURIComponent(id)}/reload`),
  retry: (id: string) => post<{ job: JobView }>(`/websites/${encodeURIComponent(id)}/retry`),
  logs: (id: string, type: "access" | "error", maxBytes = 128 * 1024) =>
    get<LogPage>(
      `/websites/${encodeURIComponent(id)}/logs?type=${type}&max_bytes=${maxBytes}`,
    ),
  health: (id: string) =>
    get<WebsiteHealthView>(`/websites/${encodeURIComponent(id)}/health`),
  certificate: (id: string) =>
    get<{ certificate: WebsiteCertificate | null }>(
      `/websites/${encodeURIComponent(id)}/certificate`,
    ),
  requestCertificate: (id: string, autoRenew = true) =>
    post<{ job: JobView }>(`/websites/${encodeURIComponent(id)}/certificate`, { auto_renew: autoRenew }),
  removeCertificate: (id: string) =>
    del<{ removed: boolean }>(`/websites/${encodeURIComponent(id)}/certificate`),
};

export const jobsApi = {
  get: (id: string) => get<JobView>(`/jobs/${encodeURIComponent(id)}`),
};

// --- Phase 3: monitoring & alerts -------------------------------------------

export type MetricsRange = "1h" | "6h" | "24h" | "7d" | "30d";

export const monitoringApi = {
  current: (serverId: string) =>
    get<CurrentMetrics>(`/servers/${encodeURIComponent(serverId)}/metrics/current`),
  history: (serverId: string, range: MetricsRange) =>
    get<HistoryView>(
      `/servers/${encodeURIComponent(serverId)}/metrics/history?range=${range}`,
    ),
  network: (serverId: string, range: MetricsRange) =>
    get<NetworkSeriesView>(
      `/servers/${encodeURIComponent(serverId)}/metrics/network?range=${range}`,
    ),
  disk: (serverId: string, range: MetricsRange) =>
    get<{ current: import("../types/api").DiskMetric[]; history: HistoryView }>(
      `/servers/${encodeURIComponent(serverId)}/metrics/disk?range=${range}`,
    ),
  services: (serverId: string) =>
    get<{ has_data: boolean; observed_at?: string; services: ServiceHealth[] }>(
      `/servers/${encodeURIComponent(serverId)}/metrics/services`,
    ),
  processes: (serverId: string) =>
    get<{ has_data: boolean; observed_at?: string; processes: ProcessMetric[] }>(
      `/servers/${encodeURIComponent(serverId)}/metrics/processes`,
    ),
  fleet: () => get<{ servers: FleetServer[] }>("/monitoring/fleet"),
};

export const alertsApi = {
  list: (status?: string, serverId?: string) => {
    const qs = new URLSearchParams();
    if (status) qs.set("status", status);
    if (serverId) qs.set("server_id", serverId);
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    return get<{ alerts: AlertView[] }>(`/alerts${suffix}`);
  },
  rules: () => get<{ rules: AlertRuleView[] }>("/alerts/rules"),
  updateRule: (
    id: string,
    input: Partial<{ threshold: number; duration_seconds: number; enabled: boolean }>,
  ) =>
    request<AlertRuleView>("PATCH", `/alerts/rules/${encodeURIComponent(id)}`, {
      body: input,
    }),
  acknowledge: (id: string) =>
    post<{ acknowledged: boolean }>(`/alerts/${encodeURIComponent(id)}/acknowledge`),
};

export const systemApi = {
  info: () => get<{ product: string; version: string; environment: string }>("/system/info"),
  internalMetrics: () => get<Record<string, unknown>>("/system/internal-metrics"),
};

// --- Phase 5: notification channels + operator settings ----------------------

export const notificationsApi = {
  list: () => get<{ channels: NotificationChannel[] }>("/notifications/channels"),
  create: (input: {
    name: string;
    type: NotificationChannel["type"];
    config: Record<string, unknown>;
    severity: string;
    enabled?: boolean;
  }) => post<{ channel: NotificationChannel }>("/notifications/channels", input),
  update: (
    id: string,
    input: Partial<{
      name: string;
      config: Record<string, unknown>;
      severity: string;
      enabled: boolean;
    }>,
  ) =>
    request<{ channel: NotificationChannel }>(
      "PATCH",
      `/notifications/channels/${encodeURIComponent(id)}`,
      { body: input },
    ),
  remove: (id: string) =>
    del<{ deleted: boolean }>(`/notifications/channels/${encodeURIComponent(id)}`),
  test: (id: string) =>
    post<{ sent: boolean }>(`/notifications/channels/${encodeURIComponent(id)}/test`),
};

export const settingsApi = {
  get: () => get<{ settings: EditableSettings }>("/settings"),
  patch: (settings: Partial<EditableSettings>) =>
    request<{ settings: EditableSettings }>("PATCH", "/settings", { body: { settings } }),
};

export const licenseApi = {
  status: () => get<LicenseInfo>("/license/status"),
  refresh: () => post<{ license?: LicenseInfo; error_message?: string }>("/license/refresh"),
  deactivate: () => post<{ deactivated: boolean }>("/license/deactivate"),
};

export const usersApi = {
  list: () => get<{ users: UserView[] }>("/users"),
  get: (id: string) => get<UserView>(`/users/${encodeURIComponent(id)}`),
  create: (input: {
    username: string;
    email?: string;
    display_name?: string;
    password: string;
    roles?: string[];
  }) => post<UserView>("/users", input),
  update: (
    id: string,
    input: Partial<{
      display_name?: string;
      email?: string;
      is_active?: boolean;
      roles?: string[];
      new_password?: string;
    }>,
  ) => request<UserView>("PATCH", `/users/${encodeURIComponent(id)}`, { body: input }),
  delete: (id: string) => del<{ deleted: boolean }>(`/users/${encodeURIComponent(id)}`),
};

export const rolesApi = {
  listDetail: () => get<{ roles: RoleView[] }>("/roles/detail"),
  permissions: () => get<{ permissions: PermissionView[] }>("/permissions"),
  create: (input: { name: string; description?: string; permissions?: string[] }) =>
    post<RoleView>("/roles", input),
  update: (id: string, input: { description?: string; permissions?: string[] }) =>
    request<RoleView>("PATCH", `/roles/${encodeURIComponent(id)}`, { body: input }),
  deleteRole: (id: string) => del<{ deleted: boolean }>(`/roles/${encodeURIComponent(id)}`),
};

export const dashboardApi = {
  summary: () =>
    get<{
      servers_total: number;
      servers_online: number;
      websites_total: number;
      websites_active: number;
      users_count: number;
      sessions_active: number;
      license: { status: LicenseInfo["status"]; plan?: string; expires_at?: string | null };
      recent_events: Array<{
        id: number;
        actor_type: string;
        actor_label: string;
        action: string;
        ip: string;
        created_at: string;
      }>;
    }>("/dashboard/summary"),
};
