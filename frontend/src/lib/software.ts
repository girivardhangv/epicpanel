/**
 * Software Installer API client (aaPanel-style catalog) — typed mirror of the
 * contract in docs/research/aapanel-software-installer.md §4.4.
 *
 * The control-plane endpoints are NOT implemented yet (backend phase P1+);
 * every call here hits the real path cleanly and surfaces the resulting
 * ApiError (404/501) so the UI renders an honest empty/error state — never
 * fake data. Paths follow the app-wide convention (`/v1/...`, which the
 * backend also serves under `/api/v1/...` via the /api prefix shim; the Vite
 * dev proxy only forwards `/v1`).
 */
import { api } from './api'

const base = (orgID: string, serverID: string) =>
  `/v1/organizations/${orgID}/servers/${serverID}/software`

// ---------------------------------------------------------------- wire types

/** Install-method kinds from the catalog `methods` chain (ordered: first
 * success wins). 'auto' lets the server walk the chain. */
export type SoftwareMethod = 'auto' | 'apt' | 'ppa' | 'apt_repo' | 'tarball' | 'source' | string

export interface InstalledEntry {
  version: string
  method?: SoftwareMethod
  status?: string
  healthy?: boolean
}

export interface AvailableVersion {
  version: string
  methods?: SoftwareMethod[]
  eol?: boolean
}

export type SoftwareCategory =
  | 'webserver'
  | 'database'
  | 'language'
  | 'cache'
  | 'tooling'
  | 'cms'
  | string

export interface SoftwareItem {
  name: string
  display_name: string
  category: SoftwareCategory
  description?: string
  singleton?: boolean
  installed: InstalledEntry[]
  available_versions: AvailableVersion[]
  requires?: { name: string; version?: string }[]
  conflicts?: string[]
  default_version?: string
}

export interface SoftwareDetail {
  name: string
  installed_version?: string | null
  versions: { version: string; methods?: { kind: SoftwareMethod; [k: string]: unknown }[] }[]
}

export interface TaskRef {
  task_id: string
  status: string
  steps_expected?: number
}

export type TaskState = 'queued' | 'pending' | 'running' | 'success' | 'failed' | string

export interface TaskStep {
  name: string
  status: TaskState
  percent?: number
}

export interface SoftwareTask {
  id: string
  type: string
  status: TaskState
  name?: string
  version?: string
  percent?: number
  step?: string
  steps?: TaskStep[]
  error?: string
  created_at?: string
  started_at?: string | null
  finished_at?: string | null
  log_cursor?: number
}

export interface TaskLogs {
  cursor: number
  done: boolean
  lines: string[]
}

export interface InstallRequest {
  name: string
  version: string
  method?: SoftwareMethod
}

export interface RemoveRequest {
  name: string
  version?: string
  force?: boolean
}

export interface UpgradeRequest {
  name: string
  from: string
  to: string
}

export interface StackRequest {
  stack: 'lnmp' | 'lamp' | string
  components?: { web?: string; php?: string; db?: string; cache?: string }
}

export interface ExtensionRequest {
  version: string
  extension: string
  action: 'install' | 'remove'
}

// ---------------------------------------------------------------- endpoints

export const software = {
  /** GET catalog for a server. */
  list: (orgID: string, serverID: string, params?: { category?: string; q?: string }) => {
    const qs = new URLSearchParams()
    if (params?.category) qs.set('category', params.category)
    if (params?.q) qs.set('q', params.q)
    const suffix = qs.toString() ? `?${qs.toString()}` : ''
    return api.get<{ data: SoftwareItem[] }>(`${base(orgID, serverID)}${suffix}`)
  },

  /** GET one catalog entry with its resolved method chains. */
  get: (orgID: string, serverID: string, name: string) =>
    api.get<SoftwareDetail>(`${base(orgID, serverID)}/${encodeURIComponent(name)}`),

  /** POST install → 202 {task_id}. */
  install: (orgID: string, serverID: string, req: InstallRequest) =>
    api.post<TaskRef>(`${base(orgID, serverID)}/install`, { method: 'auto', ...req }),

  /** POST remove → 202 {task_id}; 409 while dependencies exist. */
  remove: (orgID: string, serverID: string, req: RemoveRequest) =>
    api.post<TaskRef>(`${base(orgID, serverID)}/remove`, req),

  /** POST upgrade → 202 {task_id}. */
  upgrade: (orgID: string, serverID: string, req: UpgradeRequest) =>
    api.post<TaskRef>(`${base(orgID, serverID)}/upgrade`, req),

  /** POST one-click stack (LNMP/LAMP) → 202 {task_id, steps_expected}. */
  stack: (orgID: string, serverID: string, req: StackRequest) =>
    api.post<TaskRef>(`${base(orgID, serverID)}/stack`, req),

  /** POST per-version PHP extension install/remove → 202 {task_id}. */
  extension: (orgID: string, serverID: string, name: string, req: ExtensionRequest) =>
    api.post<TaskRef>(`${base(orgID, serverID)}/${encodeURIComponent(name)}/extensions`, req),

  /** GET task status (stepped progress + log_cursor). */
  task: (orgID: string, serverID: string, taskID: string) =>
    api.get<SoftwareTask>(`${base(orgID, serverID)}/tasks/${encodeURIComponent(taskID)}`),

  /** GET task log delta from a cursor (poll ~2s while running). */
  taskLogs: (orgID: string, serverID: string, taskID: string, cursor = 0, max = 400) =>
    api.get<TaskLogs>(
      `${base(orgID, serverID)}/tasks/${encodeURIComponent(taskID)}/logs?cursor=${cursor}&max=${max}`,
    ),
}

/** Known PHP extensions for the version-scoped extensions sub-view. The
 * catalog install/remove plumbing is backend-side (distro package → pecl
 * ladder); this static list is the UI vocabulary, matching the existing
 * extension_ops allow-list. */
export const PHP_EXTENSIONS: { name: string; label: string; note?: string }[] = [
  { name: 'redis', label: 'redis', note: 'Object cache / session store' },
  { name: 'opcache', label: 'opcache', note: 'Bytecode cache (usually built-in)' },
  { name: 'curl', label: 'curl', note: 'HTTP client' },
  { name: 'mbstring', label: 'mbstring', note: 'Multibyte strings' },
  { name: 'mysqli', label: 'mysqli', note: 'MySQL driver' },
  { name: 'pdo_mysql', label: 'pdo_mysql', note: 'PDO MySQL driver' },
  { name: 'gd', label: 'gd', note: 'Image processing' },
  { name: 'intl', label: 'intl', note: 'Internationalization' },
  { name: 'zip', label: 'zip', note: 'ZIP archives' },
  { name: 'imagick', label: 'imagick', note: 'ImageMagick bindings' },
  { name: 'xdebug', label: 'xdebug', note: 'Debugger / profiler (dev only)' },
]
