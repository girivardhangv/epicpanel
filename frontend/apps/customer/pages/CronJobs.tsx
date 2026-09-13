import { useCallback, useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { useParams } from 'react-router-dom'
import { Clock, Plus, Trash2, Play, Pause } from 'lucide-react'
import { api, useAuth, timeAgo } from '@epicpanel/core'
import type { Website } from '@epicpanel/core'
import { Card, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast, ConfirmDialog } from '@epicpanel/ui'
import { Modal, Field, ErrorNote } from '@epicpanel/forms'

interface Cron {
  id: string
  website_id: string
  schedule: string
  command: string
  status: string
  last_run_at?: string
}

const PRESETS = [
  { label: 'Every minute', value: '* * * * *' },
  { label: 'Every 5 minutes', value: '*/5 * * * *' },
  { label: 'Every 15 minutes', value: '*/15 * * * *' },
  { label: 'Hourly', value: '0 * * * *' },
  { label: 'Daily at midnight', value: '0 0 * * *' },
  { label: 'Daily at 3 AM', value: '0 3 * * *' },
  { label: 'Weekly (Sunday)', value: '0 0 * * 0' },
]

export function CronJobsPage() {
  const { org } = useAuth()
  const { website_id: routeWebsiteId = '' } = useParams()
  const [sites, setSites] = useState<Website[]>([])
  const [websiteId, setWebsiteId] = useState('')
  const [crons, setCrons] = useState<Cron[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ schedule: '*/15 * * * *', command: '' })
  const [confirm, setConfirm] = useState<{ title: string; message: string; action: () => Promise<void> } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const activeId = routeWebsiteId || websiteId

  useEffect(() => {
    if (!org) return
    api
      .get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) => {
        const list = (r.websites ?? []).filter((w) => w.status !== 'deleted' && w.status !== 'deleting')
        setSites(list)
        if (!routeWebsiteId && list.length > 0) setWebsiteId((prev) => prev || list[0].id)
      })
      .catch(() => setSites([]))
  }, [org?.id, routeWebsiteId]) // eslint-disable-line react-hooks/exhaustive-deps

  const load = useCallback(async () => {
    if (!org || !activeId) {
      setCrons([])
      return
    }
    try {
      const r = await api.get<{ crons: Cron[] }>(`/v1/organizations/${org.id}/websites/${activeId}/crons`)
      setCrons(r.crons ?? [])
    } catch {
      setCrons([])
    }
  }, [org?.id, activeId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    if (!org || !activeId) return
    setErr('')
    setBusy(true)
    try {
      await api.post(`/v1/organizations/${org.id}/websites/${activeId}/crons`, form)
      setShow(false)
      setForm({ schedule: '*/15 * * * *', command: '' })
      pushToast('success', 'Cron job added.')
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (c: Cron) => {
    if (!org) return
    await api.patch(`/v1/organizations/${org.id}/crons/${c.id}`)
    await load()
  }

  const remove = (c: Cron) => {
    if (!org) return
    setConfirm({
      title: 'Delete cron job',
      message: `Delete the job running "${c.command}"?`,
      action: async () => {
        await api.del(`/v1/organizations/${org.id}/crons/${c.id}`)
        pushToast('success', 'Cron job deleted.')
        await load()
      },
    })
  }

  const runConfirm = async () => {
    if (!confirm) return
    setConfirmBusy(true)
    try {
      await confirm.action()
      setConfirm(null)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Action failed')
      setConfirm(null)
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="Cron Jobs"
        subtitle="Scheduled commands running as this site's user."
        actions={<button className="btn-primary" onClick={() => setShow(true)} disabled={!activeId}><Plus size={14} /> Add cron</button>}
      />

      {sites.length > 0 && !routeWebsiteId && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <span className="text-[10px] font-extrabold uppercase tracking-[.06em] text-muted">Website</span>
          <select className="input w-[260px] cursor-pointer" value={activeId} onChange={(e) => setWebsiteId(e.target.value)}>
            {sites.map((s) => (
              <option key={s.id} value={s.id}>{s.primary_domain || s.name}</option>
            ))}
          </select>
        </div>
      )}

      <Card className="overflow-hidden !p-0">
        {crons === null ? (
          <div className="p-5"><SkeletonRows rows={2} /></div>
        ) : crons.length === 0 ? (
          <EmptyState
            icon={<Clock size={22} />}
            title="No cron jobs"
            subtitle="Schedule commands like database backups or cache cleanup."
            action={activeId && <button className="btn-brand" onClick={() => setShow(true)}><Plus size={16} /> Add Cron</button>}
          />
        ) : (
          <div className="divide-y divide-line">
            {crons.map((c) => (
              <div key={c.id} className="flex flex-wrap items-center gap-3 px-4 py-3.5 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <Clock size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="rounded-[6px] bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] font-bold text-ink">{c.schedule}</code>
                    <StatusBadge status={c.status === 'active' ? 'ready' : 'offline'} />
                    {c.last_run_at && <span className="text-[9.5px] text-muted">last run {timeAgo(c.last_run_at)}</span>}
                  </div>
                  <div className="mt-1 truncate font-mono text-[10.5px] text-muted">{c.command}</div>
                </div>
                <RowActions>
                  <button className="icon-btn" onClick={() => void toggle(c)} title={c.status === 'active' ? 'Pause' : 'Resume'} aria-label={c.status === 'active' ? 'Pause' : 'Resume'}>
                    {c.status === 'active' ? <Pause size={13} /> : <Play size={13} />}
                  </button>
                  <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => remove(c)} title="Delete" aria-label="Delete">
                    <Trash2 size={13} />
                  </button>
                </RowActions>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Add Cron Job" subtitle="Runs as the site's system user.">
        <ErrorNote message={err} />
        <Field label="Schedule" hint="Standard 5-field cron syntax.">
          <input className="input font-mono" value={form.schedule} onChange={(e) => setForm({ ...form, schedule: e.target.value })} />
        </Field>
        <div className="mb-4 flex flex-wrap gap-1.5">
          {PRESETS.map((p) => (
            <button key={p.value} className="rounded-full border border-line px-2.5 py-1 text-[11.5px] font-medium text-sub transition hover:border-brand/40 hover:text-brand"
              onClick={() => setForm({ ...form, schedule: p.value })}>
              {p.label}
            </button>
          ))}
        </div>
        <Field label="Command" hint="Single command, no chaining/pipes. Runs as the site's system user.">
          <input className="input font-mono" value={form.command} onChange={(e) => setForm({ ...form, command: e.target.value })} placeholder="php /home/site/cron.php" />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={create} disabled={busy || !form.command}>
          {busy ? (<><Spinner size={13} /> Adding…</>) : 'Add Cron Job'}
        </button>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={runConfirm}
        title={confirm?.title ?? ''}
        message={confirm?.message ?? ''}
        confirmLabel="Delete"
        busy={confirmBusy}
      />
    </div>
  )
}
