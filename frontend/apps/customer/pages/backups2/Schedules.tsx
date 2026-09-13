import { useCallback, useEffect, useState } from 'react'
import { CalendarClock, Plus, RefreshCw, Timer, Trash2 } from 'lucide-react'
import { api, timeAgo, useAuth } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, PageTitle, RowActions, SkeletonRows, pushToast } from '@epicpanel/ui'
import { ConfirmDialog, ErrorNote, Field, FormRow, Modal, Select } from '@epicpanel/forms'
import { TYPE_LABELS, cronLooksValid } from './model'
import type { BackupType, ScheduleRow, WorkloadOption } from './model'

/** Phase 11 schedules: 5-field cron, per workload type. The API scopes every
 * schedule to a website (server-side invariant: website_id required). */
export function SchedulesPage() {
  const { org, myRole } = useAuth()
  const [schedules, setSchedules] = useState<ScheduleRow[] | null>(null)
  const [sites, setSites] = useState<WorkloadOption[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [confirm, setConfirm] = useState<{ schedule: ScheduleRow } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [formErr, setFormErr] = useState('')
  const [form, setForm] = useState({ type: 'website' as BackupType, workload_id: '', cron: '0 3 * * *' })

  const canManage = myRole === 'owner' || myRole === 'admin'

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ schedules: ScheduleRow[] }>(`/v1/organizations/${org.id}/backup-schedules`)
      .then((r) => setSchedules(r.schedules ?? []))
      .catch((e) => {
        setSchedules([])
        setErr(e.message ?? 'Failed to load backup schedules')
      })
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
  }, [load])

  useEffect(() => {
    if (!org) return
    api
      .get<{ websites: { id: string; name: string; primary_domain: string }[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) => setSites((r.websites ?? []).map((w) => ({ id: w.id, name: w.primary_domain || w.name, sub: w.name }))))
      .catch(() => setSites([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const workloadList = sites

  const create = async () => {
    if (!org) return
    setFormErr('')
    if (!cronLooksValid(form.cron)) {
      setFormErr('Cron must be a 5-field expression, e.g. 0 3 * * *')
      return
    }
    setBusy(true)
    try {
      const body: Record<string, unknown> = { type: form.type, cron: form.cron.trim(), website_id: form.workload_id }
      await api.post(`/v1/organizations/${org.id}/backup-schedules`, body)
      pushToast('success', 'Schedule created — the control-plane scheduler fires it on the cron')
      setShowCreate(false)
      setForm({ type: form.type, workload_id: '', cron: form.cron })
      load()
    } catch (ex: any) {
      setFormErr(ex.message ?? 'Failed to create schedule')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!org || !confirm) return
    setConfirmBusy(true)
    try {
      await api.del(`/v1/organizations/${org.id}/backup-schedules/${confirm.schedule.id}`)
      pushToast('success', 'Schedule removed — existing backups are untouched')
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Delete failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  const workloadName = (s: ScheduleRow): string => {
    const hit = sites.find((w) => w.id === s.website_id)
    return hit?.name ?? (s.website_id ?? 'unknown').slice(0, 8)
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Backup Schedules"
        subtitle="Cron-driven automatic backups per workload. Retention prunes restore points beyond your plan's Backups allowance."
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            {canManage && (
              <button className="btn-brand" onClick={() => setShowCreate(true)}>
                <Plus size={16} /> New Schedule
              </button>
            )}
          </>
        }
      />

      <Card>
        <CardHeader title="Cron schedules" subtitle="5-field cron (minute hour day month weekday), server time" />
        <ErrorNote message={err} />
        {schedules === null ? (
          <SkeletonRows rows={2} />
        ) : schedules.length === 0 ? (
          <EmptyState
            icon={<CalendarClock size={22} strokeWidth={1.7} />}
            title="No schedules yet"
            subtitle="Without a schedule, backups exist only when created manually. Add one so every workload accumulates restore points automatically."
            action={
              canManage ? (
                <button className="btn-brand" onClick={() => setShowCreate(true)}>
                  Create your first schedule
                </button>
              ) : undefined
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Workload</th>
                  <th className="px-4 py-2.5 font-semibold">Backup type</th>
                  <th className="px-4 py-2.5 font-semibold">Cron</th>
                  <th className="px-4 py-2.5 font-semibold">State</th>
                  <th className="px-4 py-2.5 font-semibold">Last run</th>
                  <th className="px-4 py-2.5 font-semibold">Next run</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {schedules.map((s) => (
                  <tr key={s.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{workloadName(s)}</strong>
                      <span className="block text-[10.5px] text-muted">Website</span>
                    </td>
                    <td className="px-4 py-3 text-sub">{TYPE_LABELS[s.type as BackupType] ?? s.type}</td>
                    <td className="px-4 py-3">
                      <code className="rounded-[6px] bg-surface-2 px-1.5 py-[3px] text-[11px] text-ink">{s.cron}</code>
                    </td>
                    <td className="px-4 py-3">
                      <span className={`inline-flex items-center rounded-full px-2 py-[3px] text-[10px] font-bold ${s.enabled ? 'bg-ok-soft text-ok' : 'bg-surface-2 text-sub'}`}>
                        {s.enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-sub">{s.last_run_at ? timeAgo(s.last_run_at) : 'never'}</td>
                    <td className="px-4 py-3 text-sub">
                      <span className="inline-flex items-center gap-1">
                        <Timer size={12} className="text-muted" /> {timeAgo(s.next_run_at)}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      {canManage && (
                        <RowActions>
                          <button className="icon-btn" aria-label="Delete schedule" onClick={() => setConfirm({ schedule: s })}>
                            <Trash2 size={15} />
                          </button>
                        </RowActions>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card className="mt-3.5">
        <CardHeader title="Retention" subtitle="How long restore points are kept" />
        <div className="pb-1 text-[12px] leading-relaxed text-sub">
          <p>
            Retention is governed by your plan's <strong className="text-ink">Backups</strong> allowance (Phase 9 resource engine): when a workload exceeds its
            allowance, the oldest restore points are pruned automatically after each successful backup, and a periodic time-based prune removes expired
            archives from every target. The allowance is shown on your plan; ask the operator to raise it if you need deeper history.
          </p>
        </div>
      </Card>

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="New Backup Schedule" subtitle="The control-plane scheduler enqueues a backup job whenever the cron expression is due.">
        <ErrorNote message={formErr} />
        <FormRow cols={2}>
          <Field label="Backup type">
            <Select
              value={form.type}
              onChange={(v) => setForm({ ...form, type: v as BackupType, workload_id: '' })}
              options={[
                { value: 'website', label: TYPE_LABELS.website },
                { value: 'website_files', label: TYPE_LABELS.website_files },
                { value: 'account', label: TYPE_LABELS.account },
                { value: 'database', label: TYPE_LABELS.database },
              ]}
            />
          </Field>
          <Field label="Workload" hint={workloadList.length === 0 ? 'no websites in this organization yet' : undefined}>
            <Select
              value={form.workload_id}
              onChange={(v) => setForm({ ...form, workload_id: v })}
              options={workloadList.map((w) => ({ value: w.id, label: w.name }))}
              placeholder={workloadList.length === 0 ? 'nothing schedulable' : 'pick a workload'}
              disabled={workloadList.length === 0}
            />
          </Field>
        </FormRow>
        <Field label="Cron expression" hint="5 fields: minute hour day-of-month month day-of-week">
          <input className="input font-mono" value={form.cron} onChange={(e) => setForm({ ...form, cron: e.target.value })} placeholder="0 3 * * *" />
        </Field>
        <div className="mt-2 flex flex-wrap gap-1.5">
          {[
            { label: 'Daily 03:00', value: '0 3 * * *' },
            { label: 'Daily 04:30', value: '30 4 * * *' },
            { label: 'Sundays 05:00', value: '0 5 * * 0' },
            { label: 'Every 12 hours', value: '0 */12 * * *' },
          ].map((p) => (
            <button
              key={p.value}
              type="button"
              className="rounded-full border border-line bg-white px-2.5 py-1 text-[10.5px] font-bold text-sub transition hover:bg-surface-2"
              onClick={() => setForm({ ...form, cron: p.value })}
            >
              {p.label}
            </button>
          ))}
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.workload_id || !cronLooksValid(form.cron)}>
            {busy ? 'Creating…' : 'Create schedule'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={remove}
        title="Delete schedule"
        message={`Automatic backups for "${confirm ? workloadName(confirm.schedule) : ''}" will stop. Existing backups are kept.`}
        confirmLabel="Delete schedule"
        busy={confirmBusy}
      />
    </div>
  )
}
