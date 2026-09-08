import { useCallback, useEffect, useState } from 'react'
import { History, Plus, RefreshCw, HardDriveDownload, Clock, Database as DatabaseIcon } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, CardHeader, StatusBadge, EmptyState } from '@/components/cards'
import { PageTitle, MiniItem } from '@/components/ref'
import { Modal, Field, ErrorNote, Select } from '@/components/ui'
import { fmtBytes, timeAgo } from '@/lib/types'
import type { Website } from '@/lib/types'

interface Backup {
  id: string
  website_id: string
  kind: string
  status: string
  size_bytes?: number
  databases?: string[]
  error_message?: string
  created_at: string
}

const SCHEDULES = [
  { value: 'off', label: 'Off' },
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
]

export function BackupsPage() {
  const { org, user } = useAuth()
  const [sites, setSites] = useState<Website[]>([])
  const [items, setItems] = useState<Record<string, Backup[]>>({})
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [showCfg, setShowCfg] = useState<Website | null>(null)
  const [cfg, setCfg] = useState({ schedule: 'off', retention: 7 })
  const isAdmin = !!user?.is_platform_admin

  const load = useCallback(async () => {
    if (!org) return
    try {
      const ws = await api.get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
      const list = ws.websites ?? []
      setSites(list)
      const map: Record<string, Backup[]> = {}
      await Promise.all(
        list.map(async (s) => {
          const r = await api
            .get<{ backups: Backup[] }>(`/v1/organizations/${org.id}/websites/${s.id}/backups`)
            .catch(() => ({ backups: [] as Backup[] }))
          map[s.id] = (r.backups ?? []).slice(0, 20)
        }),
      )
      setItems(map)
    } catch (ex: any) {
      setErr(ex.message)
    }
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  // Poll while any backup is running so status flips live.
  const busyNow = Object.values(items).flat().some((b) => b.status === 'pending' || b.status === 'running' || b.status === 'creating')
  useEffect(() => {
    if (!busyNow) return
    const t = setInterval(() => void load(), 4000)
    return () => clearInterval(t)
  }, [busyNow, load])

  const create = async (site: Website) => {
    if (!org) return
    setErr('')
    setBusy(site.id)
    try {
      await api.post(`/v1/organizations/${org.id}/websites/${site.id}/backups`)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy('')
    }
  }

  const restore = async (site: Website, b: Backup) => {
    if (!org || !confirm(`Restore "${site.name}" from this backup? Current files and databases are overwritten.`)) return
    setErr('')
    setBusy(b.id)
    try {
      await api.post(`/v1/organizations/${org.id}/backups/${b.id}/restore`)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy('')
    }
  }

  const openConfig = async (site: Website) => {
    setShowCfg(site)
    try {
      const r = await api.get<{ schedule?: string; retention_days?: number }>(`/v1/organizations/${org?.id}/websites/${site.id}/backup-config`)
      setCfg({ schedule: r.schedule ?? 'off', retention: r.retention_days ?? 7 })
    } catch {
      setCfg({ schedule: 'off', retention: 7 })
    }
  }

  const saveConfig = async () => {
    if (!org || !showCfg) return
    setErr('')
    try {
      await api.patch(`/v1/organizations/${org.id}/websites/${showCfg.id}/backup-config`, {
        schedule: cfg.schedule, retention_days: Number(cfg.retention),
      })
      setShowCfg(null)
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  const all = Object.values(items).flat()
  const scheduled = sites.filter((s) => s.backup_schedule && s.backup_schedule !== 'off').length
  const totalSize = all.reduce((a, b) => a + (b.size_bytes ?? 0), 0)

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Backups"
        subtitle="Files + attached databases per site, on demand or on a schedule."
        actions={
          <>
            <button className="btn-ghost" onClick={() => void load()}><RefreshCw size={14} /> Refresh</button>
            {isAdmin && sites[0] && (
              <button className="btn-primary" onClick={() => void create(sites[0])} disabled={busy === sites[0].id || sites[0].status !== 'ready'}>
                <Plus size={13} /> {busy === sites[0].id ? 'Starting…' : 'Run backup now'}
              </button>
            )}
          </>
        }
      />

      <ErrorNote message={err} />

      {/* Summary */}
      {sites.length > 0 && (
        <div className="mb-4 grid grid-cols-1 gap-3.5 xl:grid-cols-[minmax(0,1.55fr)_minmax(300px,.85fr)]">
          <Card>
            <CardHeader className="mx-4 mt-4 !mb-0" title="Backup health" subtitle="Snapshot coverage across your sites" />
            <div className="space-y-3.5">
              <ResBar label="Sites with backups" value={`${new Set(all.map((b) => b.website_id)).size} / ${sites.length}`} pct={sites.length ? (new Set(all.map((b) => b.website_id)).size / sites.length) * 100 : 0} color="#0f9d6e" />
              <ResBar label="Sites on a schedule" value={`${scheduled} / ${sites.length}`} pct={sites.length ? (scheduled / sites.length) * 100 : 0} color="#7c4dff" />
            </div>
          </Card>
          <Card>
            <CardHeader className="mx-4 mt-4 !mb-0" title="Snapshot totals" subtitle="Across every site" />
            <div className="space-y-3">
              <MiniItem tone="blue" icon={<History size={14} strokeWidth={1.8} />} title={`${all.length} restore points`} sub="Last 20 per site are kept in this view" />
              <MiniItem tone="purple" icon={<DatabaseIcon size={14} strokeWidth={1.8} />} title={totalSize ? fmtBytes(totalSize) : '—'} sub="Total size of listed snapshots" />
              <MiniItem tone="green" icon={<Clock size={14} strokeWidth={1.8} />} title={busyNow ? 'A backup is running' : 'All jobs idle'} sub={busyNow ? 'Statuses refresh every 4s' : 'Nothing is queued right now'} />
            </div>
          </Card>
        </div>
      )}

      {sites.length === 0 ? (
        <Card><EmptyState icon={<History size={22} />} title="No sites yet" subtitle="Create a site first — backups run per site." /></Card>
      ) : (
        <div className="space-y-3.5">
          {sites.map((s) => {
            const list = items[s.id] ?? []
            const scheduleOn = s.backup_schedule && s.backup_schedule !== 'off'
            return (
              <Card key={s.id} className="overflow-hidden !p-0">
                <div className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-3.5">
                  <div className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-brand-soft text-brand">
                    <History size={14} strokeWidth={1.8} />
                  </div>
                  <div className="min-w-0">
                    <div className="truncate text-[11.5px] font-bold text-[#243047]">{s.primary_domain || s.name}</div>
                    <div className="text-[9.5px] text-muted">
                      {scheduleOn ? `${s.backup_schedule} · keep ${s.backup_retention}d` : 'No schedule configured'}
                    </div>
                  </div>
                  <div className="ml-auto flex gap-2">
                    <button className="btn-ghost !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={() => void openConfig(s)}>
                      <Clock size={12} /> Schedule
                    </button>
                    <button
                      className="btn-primary !min-h-[30px] !px-2.5 !text-[10.5px]"
                      onClick={() => void create(s)}
                      disabled={busy === s.id || s.status !== 'ready'}
                    >
                      <Plus size={12} /> {busy === s.id ? 'Starting…' : 'Back Up Now'}
                    </button>
                  </div>
                </div>

                {list.length === 0 ? (
                  <div className="px-4 py-6 text-center text-[11px] text-muted">
                    No backups yet — take the first one now.
                  </div>
                ) : (
                  <div className="divide-y divide-line">
                    {list.map((b) => (
                      <div key={b.id} className="flex items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="text-[11px] font-bold capitalize text-ink">{b.kind || 'manual'}</span>
                            <StatusBadge status={b.status} />
                            {b.size_bytes ? <span className="text-[10px] text-muted">{fmtBytes(b.size_bytes)}</span> : null}
                            {(b.databases ?? []).length > 0 && <span className="text-[10px] text-muted">· {b.databases!.length} db</span>}
                          </div>
                          {b.error_message && <div className="mt-0.5 truncate text-[10px] text-danger">{b.error_message}</div>}
                        </div>
                        <span className="shrink-0 text-[10px] text-muted">{timeAgo(b.created_at)}</span>
                        {b.status === 'available' && (
                          <button
                            className="btn-ghost !min-h-[28px] !px-2 !text-[10px]"
                            onClick={() => void restore(s, b)}
                            disabled={busy === b.id}
                            title="Restore files + databases from this backup"
                          >
                            <HardDriveDownload size={12} /> Restore
                          </button>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </Card>
            )
          })}
        </div>
      )}

      <Modal open={!!showCfg} onClose={() => setShowCfg(null)} title="Backup schedule" subtitle={showCfg?.primary_domain ?? undefined}>
        <ErrorNote message={err} />
        <Field label="Schedule">
          <Select value={cfg.schedule} onChange={(v) => setCfg({ ...cfg, schedule: v })} options={SCHEDULES} />
        </Field>
        <Field label="Keep how many backups" hint="Older backups beyond this count are pruned automatically.">
          <input className="input" type="number" min={1} max={30} value={cfg.retention} onChange={(e) => setCfg({ ...cfg, retention: Number(e.target.value) })} />
        </Field>
        <div className="mt-2 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCfg(null)}>Cancel</button>
          <button className="btn-brand" onClick={saveConfig}>Save Schedule</button>
        </div>
      </Modal>
    </div>
  )
}

function ResBar({ label, value, pct, color }: { label: string; value: string; pct: number; color: string }) {
  return (
    <div>
      <div className="mb-[7px] flex items-baseline justify-between gap-3">
        <strong className="text-[11px] text-ink">{label}</strong>
        <span className="text-[10px] font-bold text-muted">{value}</span>
      </div>
      <div className="h-[7px] w-full overflow-hidden rounded-full bg-line-soft">
        <div className="h-full rounded-full transition-all" style={{ width: `${Math.max(2, Math.min(100, pct))}%`, background: color }} />
      </div>
    </div>
  )
}
