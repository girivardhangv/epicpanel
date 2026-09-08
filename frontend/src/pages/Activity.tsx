import { useEffect, useMemo, useState } from 'react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Timeline } from '@epicpanel/ui'
import { Card, EmptyState, SkeletonRows } from '@/components/cards'
import { PageTitle, Toolbar, ToolbarSearch } from '@/components/ref'
import { timeAgo } from '@/lib/types'
import type { AuditEntry } from '@/lib/types'

// System-generated job chatter that end users should never see.
const NOISE_PREFIXES = ['job.', 'server.metrics', 'domain.dns_check_requested']

// Human phrasing for every user-facing audit action.
const ACTION_LABELS: Record<string, string> = {
  'website.created': 'created a website',
  'website.updated': 'updated a website',
  'website.delete_requested': 'requested website deletion',
  'website.deleted': 'deleted a website',
  'website.rewrite_rules_updated': 'updated rewrite rules',
  'wordpress.install_requested': 'requested a WordPress install',
  'domain.added': 'added a domain',
  'domain.removed': 'removed a domain',
  'domain.ssl_mode_set': 'changed SSL mode',
  'database.credentials_revealed': 'viewed database credentials',
  'database.pma_sso_opened': 'opened phpMyAdmin (SSO)',
  'runtime.install_requested': 'requested a software install',
  'runtime.remove_requested': 'requested software removal',
  'php.extension_install_requested': 'requested a PHP extension',
  'php.extension_remove_requested': 'requested PHP extension removal',
  'server.detect_software_requested': 'ran a software inventory',
  'backup.created': 'started a backup',
  'backup.restore_requested': 'requested a backup restore',
  'backup.config_updated': 'updated backup schedule',
  'cron.created': 'created a cron job',
  'cron.updated': 'updated a cron job',
  'cron.deleted': 'deleted a cron job',
  'sshkey.added': 'added an SSH key',
  'sshkey.removed': 'removed an SSH key',
  'member.added': 'added a team member',
  'member.updated': 'updated a team member',
  'member.removed': 'removed a team member',
  'package.assigned': 'assigned a hosting package',
  'staging.created': 'created a staging environment',
  'staging.promoted': 'promoted staging to production',
  'app.deploy_requested': 'triggered a deployment',
  'app.rollback_requested': 'rolled back a release',
}

function humanAction(action: string): string {
  if (ACTION_LABELS[action]) return ACTION_LABELS[action]
  return action
    .split(/[._]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

type Filter = 'all' | 'sites' | 'databases' | 'security' | 'team'

const FILTER_MAP: Record<Filter, string[] | null> = {
  all: null,
  sites: ['website', 'domain', 'cron', 'app'],
  databases: ['database', 'runtime', 'php'],
  security: ['backup', 'sshkey', 'member', 'package', 'staging'],
  team: ['member', 'package'],
}

export function ActivityPage() {
  const { org } = useAuth()
  const [logs, setLogs] = useState<AuditEntry[] | null>(null)
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')

  useEffect(() => {
    if (!org) return
    api.get<{ logs: AuditEntry[] }>(`/v1/audit-logs?organization_id=${org.id}&limit=200`)
      .then((r) => setLogs(r.logs ?? []))
      .catch(() => setLogs([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const visible = useMemo(() => {
    const kinds = FILTER_MAP[filter]
    const q = query.toLowerCase().trim()
    return (logs ?? []).filter((a) => {
      if (NOISE_PREFIXES.some((p) => a.action.startsWith(p))) return false
      if (kinds && !kinds.some((k) => a.resource_type.startsWith(k))) return false
      if (q && !`${a.actor_email ?? ''} ${humanAction(a.action)} ${a.resource_id ?? ''}`.toLowerCase().includes(q)) return false
      return true
    })
  }, [logs, filter, query])

  const filters: { key: Filter; label: string }[] = [
    { key: 'all', label: 'All' },
    { key: 'sites', label: 'Sites & domains' },
    { key: 'databases', label: 'Databases & software' },
    { key: 'security', label: 'Backups & security' },
    { key: 'team', label: 'Team' },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Activity Log" subtitle="A clear audit trail for important administrative actions." />

      <Card className="overflow-hidden !p-0">
        <Toolbar>
          <ToolbarSearch value={query} onChange={setQuery} placeholder="Search actor, action or resource..." />
          <select
            className="input h-[34px] w-auto cursor-pointer text-[11px]"
            value={filter}
            onChange={(e) => setFilter(e.target.value as Filter)}
          >
            {filters.map((f) => <option key={f.key} value={f.key}>{f.label}</option>)}
          </select>
        </Toolbar>

        {logs === null ? (
          <div className="p-5"><SkeletonRows rows={6} height="h-12" /></div>
        ) : visible.length === 0 ? (
          <EmptyState title="Nothing yet" subtitle="Actions will appear here as your team works." />
        ) : (
          <div className="px-5 py-4">
            <Timeline
              ariaLabel="Activity trail"
              items={visible.slice(0, 100).map((a) => ({
                id: String(a.id),
                title: (
                  <span className="text-[11.5px]">
                    <span className="font-bold text-brand">{a.actor_email ?? 'System'}</span>
                    {' '}{humanAction(a.action)}
                  </span>
                ),
                description: `${a.resource_type}${a.resource_id ? ` · ${a.resource_id.slice(0, 8)}` : ''}`,
                tone: a.result === 'failure' ? 'red' : 'blue',
                timestamp: timeAgo(a.created_at),
                meta: a.result === 'failure' ? <span className="status-chip status-down">Failed</span> : undefined,
              }))}
            />
          </div>
        )}
      </Card>
    </div>
  )
}
