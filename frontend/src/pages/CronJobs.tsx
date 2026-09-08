import { useCallback, useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Clock, Plus, Trash2, Play, Pause } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows, StatusBadge } from '@/components/cards'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { timeAgo } from '@/lib/types'

interface Cron {
  id: string
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
  const { website_id: websiteId = '' } = useParams()
  const [crons, setCrons] = useState<Cron[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ schedule: '*/15 * * * *', command: '' })
  const base = `/v1/organizations/${org?.id}/websites/${websiteId}/crons`

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ crons: Cron[] }>(base)
      setCrons(r.crons ?? [])
    } catch {
      setCrons([])
    }
  }, [base])

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.post(base, form)
      setShow(false)
      setForm({ schedule: '*/15 * * * *', command: '' })
      await load()
    } catch (ex: any) {
      setErr(ex.message)
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (c: Cron) => {
    await api.patch(`/v1/organizations/${org?.id}/crons/${c.id}`)
    await load()
  }

  const remove = async (c: Cron) => {
    if (!confirm(`Delete cron "${c.command}"?`)) return
    await api.del(`/v1/organizations/${org?.id}/crons/${c.id}`)
    await load()
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <Link to={`/sites/${websiteId}`} className="icon-btn !h-[34px] !w-[34px]" title="Back"><ArrowLeft size={15} /></Link>
          <div>
            <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Cron Jobs</h1>
            <p className="mt-[5px] text-[12px] text-muted">Scheduled commands running as this site's user.</p>
          </div>
        </div>
        <button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Add cron</button>
      </div>

      <Card className="overflow-hidden !p-0">
        {crons === null ? (
          <SkeletonRows rows={2} />
        ) : crons.length === 0 ? (
          <EmptyState
            icon={<Clock size={22} />}
            title="No cron jobs"
            subtitle="Schedule commands like database backups or cache cleanup."
            action={<button className="btn-brand" onClick={() => setShow(true)}><Plus size={16} /> Add Cron</button>}
          />
        ) : (
          <div className="divide-y divide-line">
            {crons.map((c) => (
              <div key={c.id} className="flex items-center gap-3 px-4 py-3.5 transition hover:bg-surface-2">
                <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                  <Clock size={14} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="rounded-[6px] bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] font-bold text-ink">{c.schedule}</code>
                    <StatusBadge status={c.status === 'active' ? 'ready' : 'offline'} />
                  </div>
                  <div className="mt-1 truncate font-mono text-[10.5px] text-muted">{c.command}</div>
                </div>
                <button className="icon-btn" onClick={() => toggle(c)} title={c.status === 'active' ? 'Pause' : 'Resume'}>
                  {c.status === 'active' ? <Pause size={13} /> : <Play size={13} />}
                </button>
                <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" onClick={() => remove(c)} title="Delete">
                  <Trash2 size={13} />
                </button>
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
          {busy ? 'Adding...' : 'Add Cron Job'}
        </button>
      </Modal>
    </div>
  )
}
