// Shared wire shapes + display maps for the Phase 11 unified backup engine
// (endpoints: /backup-targets, /backups2, /backup-schedules). Credentials are
// write-only: the API never returns the sealed blob, and nothing here renders
// one.

/** The verbatim master-doc backup types + the website_files split. */
export const BACKUP_TYPES = [
  'account',
  'database',
  'website',
  'minecraft_world',
  'discord_bot',
  'full_instance',
  'website_files',
] as const

export type BackupType = (typeof BACKUP_TYPES)[number]

export const TYPE_LABELS: Record<BackupType, string> = {
  account: 'Account',
  database: 'Database',
  website: 'Website',
  minecraft_world: 'Minecraft World',
  discord_bot: 'Discord Bot',
  full_instance: 'Full Instance',
  website_files: 'Website Files',
}

export const TYPE_ORDER: BackupType[] = [
  'website',
  'website_files',
  'account',
  'database',
  'minecraft_world',
  'discord_bot',
  'full_instance',
]

/** Which workload scope each type resolves against (mirrors the API's
 * resolveWorkload). The values are the query/body field names. */
export type WorkloadScope = 'website_id' | 'instance_id' | 'bot_id'

export function scopeForType(t: BackupType): WorkloadScope {
  if (t === 'minecraft_world') return 'instance_id'
  if (t === 'discord_bot' || t === 'full_instance') return 'bot_id'
  return 'website_id'
}

/** Chip tone per type (bg/text utility classes on the neutral chip base). */
export const TYPE_TONES: Record<BackupType, string> = {
  account: 'bg-purple-soft text-purple',
  database: 'bg-brand-soft text-brand',
  website: 'bg-brand-soft text-brand',
  minecraft_world: 'bg-ok-soft text-ok',
  discord_bot: 'bg-purple-soft text-purple',
  full_instance: 'bg-warn-soft text-warn',
  website_files: 'bg-surface-2 text-sub',
}

/** The verbatim master-doc target kinds. Note: the live API creates 'local'
 * and 'remote'; the object-storage kind has a backend seam (see the Phase 11
 * handoff) and is offered here so the UI keeps speaking the documented
 * contract. */
export const TARGET_KINDS = ['local', 'remote', 's3'] as const
export type TargetKind = (typeof TARGET_KINDS)[number]

export const TARGET_KIND_LABELS: Record<TargetKind, string> = {
  local: 'Local (node filesystem)',
  remote: 'Remote (ssh/rsync-style)',
  s3: 'Object Storage (S3-compatible)',
}

/** Unified backup row (backups table, Phase 11 projection). */
export interface BackupRow {
  id: string
  organization_id: string
  website_id?: string
  instance_id?: string
  bot_id?: string
  target_id?: string
  type: string
  status: string
  trigger_type: string
  size_bytes: number
  sha256?: string
  encrypted: boolean
  verification: string
  sink_kind: string
  sink_ref?: string
  stored_bytes?: number
  databases: string[]
  error?: string
  started_at?: string
  finished_at?: string
  verified_at?: string
  created_at: string
}

/** Backup sink target. The sealed creds blob is NEVER in this shape. */
export interface TargetRow {
  id: string
  organization_id: string
  name: string
  kind: string
  config: {
    kind?: string
    local_dir?: string
    s3?: { endpoint: string; region: string; bucket: string; prefix?: string; path_style?: boolean }
    remote?: { host: string; port?: number; user: string; path: string; transport?: string }
  }
  is_default: boolean
  created_at: string
}

/** Org-level cron schedule (website- or bot-scoped server-side). */
export interface ScheduleRow {
  id: string
  organization_id: string
  website_id?: string
  bot_id?: string
  type: string
  cron: string
  enabled: boolean
  last_run_at?: string
  next_run_at: string
  created_at: string
}

/** Minimal workload pickers for the create forms. */
export interface WorkloadOption {
  id: string
  name: string
  sub: string
  server_id?: string
}

export function cronLooksValid(v: string): boolean {
  const fields = v.trim().split(/\s+/)
  return fields.length === 5 && fields.every((f) => f.length > 0) && !v.includes('"') && !v.includes("'")
}

/** Brief per-status explanation shown as row subtitles (honest wording). */
export function verificationLabel(v: string): { text: string; cls: string } {
  switch (v) {
    case 'ok':
      return { text: 'Verified', cls: 'bg-ok-soft text-ok' }
    case 'failed':
      return { text: 'Unverified', cls: 'bg-danger-soft text-danger' }
    default:
      return { text: 'Not verified', cls: 'bg-surface-2 text-sub' }
  }
}
