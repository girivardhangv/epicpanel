import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Archive, ArchiveRestore, Lock, Play, Plus, RefreshCw, ShieldCheck } from 'lucide-react'
import { api, fmtBytes, timeAgo, useAuth } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, PageTitle, RowActions, StatusBadge, SkeletonRows, pushToast } from '@epicpanel/ui'
import { ConfirmDialog, ErrorNote, Field, FormRow, Modal, Select } from '@epicpanel/forms'
import {
  BACKUP_TYPES,
  TYPE_LABELS,
  TYPE_ORDER,
  TYPE_TONES,
  TARGET_KIND_LABELS,
  verificationLabel,
  scopeForType,
} from './model'
import type { BackupRow, BackupType, TargetRow, WorkloadOption } from './model'

/** Phase 11 unified backups. The workload picker mirrors the API's
 * resolveWorkload: website-scoped types (account/database/website/
 * website_files), minecraft_world → instance, discord_bot + full_instance →
 * bot. Restore/verify are org-admin server-side; the buttons are hidden for
 * lower roles instead of inviting 403s. */

function TypeChip({ type }: { type: string }) {
  const tone = TYPE_TONES[type as BackupType] ?? 'bg-surface-2 text-sub'
  return (
    <span className={`inline-flex items-center gap-1 rounded-full px-2 py-[3px] text-[10px] font-bold ${tone}`}>
      {TYPE_LABELS[type as BackupType] ?? type}
    </span>
  )
}

function workloadLabel(type: string, b: BackupRow, sites: WorkloadOption[], instances: WorkloadOption[], bots: WorkloadOption[]): string {
  const scope = scopeForType((type as BackupType) ?? 'website')
  const list = scope === 'bot_id' ? bots : scope === 'instance_id' ? instances : sites
  const id = scope === 'bot_id' ? b.bot_id : scope === 'instance_id' ? b.instance_id : b.website_id
  const hit = list.find((w) => w.id === id)
  if (hit) return hit.name
  if (id) return id.slice(0, 8)
  return 'Org-wide'
}

export function Backups2Page() {
  const { org, myRole } = useAuth()
  const params = useParams()
  const navigate = useNavigate()
  const rawType = params.workload_type
  const urlType = rawType && BACKUP_TYPES.includes(rawType as BackupType) ? (rawType as BackupType) : null
  // An explicit but unknown /backups2/:workload_type gets an honest guard
  // (targets/schedules are static sub-pages, not types).
  const unknownType = !!rawType && !urlType && rawType !== 'targets' && rawType !== 'schedules'

  const [backups, setBackups] = useState<BackupRow[] | null>(null)
  const [targets, setTargets] = useState<TargetRow[]>([])
  const [sites, setSites] = useState<WorkloadOption[]>([])
  const [instances, setInstances] = useState<WorkloadOption[]>([])
  const [bots, setBots] = useState<WorkloadOption[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [confirm, setConfirm] = useState<{ kind: 'restore' | 'verify'; row: BackupRow } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ type: 'website' as BackupType, workload_id: '', target_id: '', encrypt: true, verify: true })

  // Mutations (create/restore/verify) are org-admin+ server-side.
  const canMutate = myRole === 'owner' || myRole === 'admin'

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ backups: BackupRow[] }>(`/v1/organizations/${org.id}/backups2?limit=100`)
      .then((r) => {
        setBackups(r.backups ?? [])
        setLoadErr('')
      })
      .catch((e) => {
        setBackups([])
        setLoadErr(e.message ?? 'Failed to load backups')
      })
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
  }, [load])

  // Workloads + targets (for the picker and row labels).
  useEffect(() => {
    if (!org) return
    api
      .get<{ websites: { id: string; name: string; primary_domain: string; server_id: string }[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) =>
        setSites((r.websites ?? []).map((w) => ({ id: w.id, name: w.primary_domain || w.name, sub: w.name, server_id: w.server_id }))),
      )
      .catch(() => setSites([]))
    api
      .get<{ instances: { id: string; name: string; server_id: string }[] }>(`/v1/organizations/${org.id}/minecraft`)
      .then((r) => setInstances((r.instances ?? []).map((i) => ({ id: i.id, name: i.name, sub: 'Minecraft instance', server_id: i.server_id }))))
      .catch(() => setInstances([]))
    api
      .get<{ bots: { id: string; name: string; server_id: string }[] }>(`/v1/organizations/${org.id}/bots`)
      .then((r) => setBots((r.bots ?? []).map((b) => ({ id: b.id, name: b.name, sub: 'Discord bot', server_id: b.server_id }))))
      .catch(() => setBots([]))
    api
      .get<{ targets: TargetRow[] }>(`/v1/organizations/${org.id}/backup-targets`)
      .then((r) => setTargets(r.targets ?? []))
      .catch(() => setTargets([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // Poll while any backup is pending/running so job outcomes surface live.
  const jobsInFlight = (backups ?? []).some((b) => b.status === 'pending' || b.status === 'running')
  useEffect(() => {
    if (!jobsInFlight) return
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [jobsInFlight, load])

  const shown = useMemo(() => {
    const list = backups ?? []
    return urlType ? list.filter((b) => b.type === urlType) : list
  }, [backups, urlType])

  const scope = scopeForType(form.type)
  const workloadList = scope === 'bot_id' ? bots : scope === 'instance_id' ? instances : sites
  const canCreate = canMutate && workloadList.length > 0

  const create = async () => {
    if (!org) return
    setFormErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        type: form.type,
        encrypt: form.encrypt,
        verify: form.verify,
      }
      if (scope === 'bot_id') body.bot_id = form.workload_id
      else if (scope === 'instance_id') body.instance_id = form.workload_id
      else body.website_id = form.workload_id
      if (form.target_id) body.target_id = form.target_id
      await api.post(`/v1/organizations/${org.id}/backups2`, body)
      pushToast('success', `${TYPE_LABELS[form.type]} backup queued`)
      setShowCreate(false)
      setForm({ type: form.type, workload_id: '', target_id: '', encrypt: form.encrypt, verify: true })
      load()
    } catch (ex: any) {
      setFormErr(ex.message ?? 'Failed to create backup')
    } finally {
      setBusy(false)
    }
  }

  const runConfirm = async () => {
    if (!org || !confirm) return
    setConfirmBusy(true)
    try {
      if (confirm.kind === 'restore') {
        await api.post(`/v1/organizations/${org.id}/backups2/${confirm.row.id}/restore`)
        pushToast('success', 'Restore job queued — the workload is overwritten once it runs')
      } else {
        await api.post(`/v1/organizations/${org.id}/backups2/${confirm.row.id}/verify`)
        pushToast('success', 'Verification job queued (restore-to-scratch + checksum)')
      }
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Request failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Backups"
        subtitle="Every workload type — account, database, website, Minecraft world, Discord bot, full instance — with encrypted targets, verification and restore"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            {canMutate && (
              <button className="btn-brand" onClick={() => setShowCreate(true)} disabled={!canCreate}>
                <Plus size={16} /> New Backup
              </button>
            )}          </>
        }
      />

      <div className="mb-3.5 flex flex-wrap items-center gap-1.5">
        <button
          className={`rounded-full px-3 py-1.5 text-[11px] font-bold transition ${!urlType ? 'bg-brand text-white' : 'border border-line bg-white text-sub hover:bg-surface-2'}`}
          onClick={() => navigate('/backups2')}
        >
          All
        </button>
        {TYPE_ORDER.map((t) => {
          const active = urlType === t
          const count = (backups ?? []).filter((b) => b.type === t).length
          return (
            <button
              key={t}
              className={`rounded-full px-3 py-1.5 text-[11px] font-bold transition ${active ? 'bg-brand text-white' : 'border border-line bg-white text-sub hover:bg-surface-2'}`}
              onClick={() => navigate(`/backups2/${t}`)}
            >
              {TYPE_LABELS[t]}
              {count > 0 ? <span className={active ? 'ml-1.5 text-white/80' : 'ml-1.5 text-muted'}>{count}</span> : null}
            </button>
          )
        })}
      </div>

      <Card>
        <CardHeader
          title="Restore points"
          subtitle={`Unified backup engine — local, remote and object-storage targets${jobsInFlight ? ' · jobs in flight, refreshing every 4s' : ''}`}
        />
        <ErrorNote message={loadErr} />
        {unknownType ? (
          <EmptyState
            icon={<Archive size={22} strokeWidth={1.7} />}
            title="Unknown backup type"
            subtitle="That workload type does not exist. Use the type chips above to pick one of the six verbatim types."
          />
        ) : backups === null ? (
          <SkeletonRows rows={2} />
        ) : shown.length === 0 ? (
          <EmptyState
            icon={<Archive size={22} strokeWidth={1.7} />}
            title={urlType ? `No ${TYPE_LABELS[urlType].toLowerCase()} backups yet` : 'No backups yet'}
            subtitle={
              canMutate
                ? 'Create a backup of any workload, or set a schedule below so restore points accumulate on their own.'
                : 'Backups created by org admins or schedules will appear here.'
            }
            action={
              canMutate && canCreate ? (
                <button className="btn-brand" onClick={() => setShowCreate(true)}>
                  Create your first backup
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
                  <th className="px-4 py-2.5 font-semibold">Type</th>
                  <th className="px-4 py-2.5 font-semibold">Status</th>
                  <th className="px-4 py-2.5 font-semibold">Size</th>
                  <th className="px-4 py-2.5 font-semibold">Protection</th>
                  <th className="px-4 py-2.5 font-semibold">Target</th>
                  <th className="px-4 py-2.5 font-semibold">Created</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {shown.map((b) => {
                  const ver = verificationLabel(b.verification)
                  return (
                    <tr key={b.id} className="border-b border-line/60">
                      <td className="px-4 py-3">
                        <strong className="text-[13px] text-ink">{workloadLabel(b.type, b, sites, instances, bots)}</strong>
                        <span className="block text-[10.5px] text-muted">
                          {b.trigger_type === 'manual' ? 'manual' : b.trigger_type}
                          {b.error ? ` · ${b.error}` : ''}
                        </span>
                      </td>
                      <td className="px-4 py-3">
                        <TypeChip type={b.type} />
                      </td>
                      <td className="px-4 py-3">
                        <StatusBadge status={b.status} />
                      </td>
                      <td className="px-4 py-3 text-sub">{b.status === 'successful' && b.size_bytes > 0 ? fmtBytes(b.size_bytes) : '—'}</td>
                      <td className="px-4 py-3">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {b.encrypted ? (
                            <span className="inline-flex items-center gap-1 rounded-full bg-brand-soft px-2 py-[3px] text-[10px] font-bold text-brand">
                              <Lock size={10} /> Encrypted
                            </span>
                          ) : (
                            <span className="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-[3px] text-[10px] font-bold text-sub">
                              Plaintext
                            </span>
                          )}
                          <span className={`inline-flex items-center gap-1 rounded-full px-2 py-[3px] text-[10px] font-bold ${ver.cls}`}>
                            <ShieldCheck size={10} /> {ver.text}
                          </span>
                        </div>
                      </td>
                      <td className="px-4 py-3 text-sub">{TARGET_KIND_LABELS[b.sink_kind as keyof typeof TARGET_KIND_LABELS] ?? b.sink_kind}</td>
                      <td className="px-4 py-3 text-sub">{timeAgo(b.created_at)}</td>
                      <td className="px-4 py-3">
                        {canMutate && b.status === 'successful' && (
                          <RowActions>
                            <button className="btn-ghost" onClick={() => setConfirm({ kind: 'verify', row: b })}>
                              <ShieldCheck size={13} /> Verify
                            </button>
                            <button className="btn-ghost" onClick={() => setConfirm({ kind: 'restore', row: b })}>
                              <ArchiveRestore size={13} /> Restore
                            </button>
                          </RowActions>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="New Backup"
        subtitle="The job runs on the workload's node, is checksummed and (optionally) encrypted before leaving it."
      >
        <ErrorNote message={formErr} />
        <FormRow cols={2}>
          <Field label="Backup type">
            <Select
              value={form.type}
              onChange={(v) => setForm({ ...form, type: v as BackupType, workload_id: '' })}
              options={BACKUP_TYPES.map((t) => ({ value: t, label: TYPE_LABELS[t] }))}
            />
          </Field>
          <Field label="Workload" hint={workloadList.length === 0 ? `no ${scope === 'bot_id' ? 'bots' : scope === 'instance_id' ? 'Minecraft instances' : 'websites'} in this organization yet` : undefined}>
            <Select
              value={form.workload_id}
              onChange={(v) => setForm({ ...form, workload_id: v })}
              options={workloadList.map((w) => ({ value: w.id, label: w.name }))}
              placeholder={workloadList.length === 0 ? 'nothing to back up' : 'pick a workload'}
              disabled={workloadList.length === 0}
            />
          </Field>
        </FormRow>
        <Field label="Target" hint="Where the archive lands. Empty = the node's local backup directory.">
          <Select
            value={form.target_id}
            onChange={(v) => setForm({ ...form, target_id: v })}
            options={[
              { value: '', label: 'Node-local default' },
              ...targets.map((t) => ({ value: t.id, label: `${t.name} (${TARGET_KIND_LABELS[t.kind as keyof typeof TARGET_KIND_LABELS] ?? t.kind})` })),
            ]}
          />
        </Field>
        <div className="space-y-2">
          <label className="flex cursor-pointer items-center gap-2 text-[12px] font-semibold text-sub">
            <input type="checkbox" className="h-3.5 w-3.5 accent-[#2563eb]" checked={form.encrypt} onChange={(e) => setForm({ ...form, encrypt: e.target.checked })} />
            <Lock size={13} className="text-brand" /> Encrypt at rest (per-backup data key, wrapped — encrypted before leaving the node)
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-[12px] font-semibold text-sub">
            <input type="checkbox" className="h-3.5 w-3.5 accent-[#2563eb]" checked={form.verify} onChange={(e) => setForm({ ...form, verify: e.target.checked })} />
            <ShieldCheck size={13} className="text-ok" /> Verify after write (checksum + artifact stat)
          </label>
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.workload_id}>
            <Play size={13} /> {busy ? 'Queueing…' : 'Queue backup'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={runConfirm}
        title={confirm?.kind === 'restore' ? 'Restore from backup' : 'Verify backup'}
        confirmLabel={confirm?.kind === 'restore' ? 'Restore now' : 'Run verification'}
        busy={confirmBusy}
        danger={confirm?.kind === 'restore'}
        message={
          confirm?.kind === 'restore'
            ? `"${workloadLabel(confirm.row.type, confirm.row, sites, instances, bots)}" will be overwritten from the ${TYPE_LABELS[confirm.row.type as BackupType] ?? confirm.row.type} backup taken ${timeAgo(confirm.row.created_at)}. Current data on the workload is replaced. This is audited.`
            : `The stored artifact will be pulled to a scratch area on the node and its SHA-256 checksum re-verified. Unverified backups are flagged so they can be re-run before you ever need them.`
        }
      />
    </div>
  )
}
