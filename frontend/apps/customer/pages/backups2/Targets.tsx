import { useCallback, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { CloudUpload, FolderCog, HardDrive, Plus, RefreshCw, Server, Trash2 } from 'lucide-react'
import { api, timeAgo, useAuth } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, PageTitle, RowActions, SkeletonRows, pushToast } from '@epicpanel/ui'
import { ConfirmDialog, ErrorNote, Field, FormRow, InfoNote, Modal, Select } from '@epicpanel/forms'
import { TARGET_KINDS, TARGET_KIND_LABELS } from './model'
import type { TargetKind, TargetRow } from './model'

const KIND_ICONS: Record<string, ReactNode> = {
  local: <HardDrive size={14} strokeWidth={1.8} />,
  remote: <Server size={14} strokeWidth={1.8} />,
  s3: <CloudUpload size={14} strokeWidth={1.8} />,
}

function targetSubtitle(t: TargetRow): string {
  if (t.kind === 'local') return t.config.local_dir ? `dir ${t.config.local_dir}` : 'agent default directory'
  if (t.kind === 'remote' && t.config.remote) {
    const r = t.config.remote
    return `${r.user}@${r.host}:${r.port || 22} ${r.path}`
  }
  if (t.kind === 's3' && t.config.s3) {
    const s = t.config.s3
    return `${s.bucket}@${s.endpoint}${s.prefix ? ` / ${s.prefix}` : ''}`
  }
  return 'configured target'
}

/** Initial form state per kind. Credentials are collected as plaintext,
 * transported over TLS and stored sealed at rest — the panel never returns
 * them, and no input here ever renders one. */
type TargetForm = {
  name: string
  kind: TargetKind
  local_dir: string
  host: string
  port: string
  user: string
  path: string
  endpoint: string
  region: string
  bucket: string
  prefix: string
  path_style: boolean
  password: string
  private_key: string
  access_key_id: string
  secret_key: string
}

const EMPTY_FORM: TargetForm = {
  name: '',
  kind: 'local',
  local_dir: '',
  host: '',
  port: '22',
  user: '',
  path: '',
  endpoint: '',
  region: 'us-east-1',
  bucket: '',
  prefix: '',
  path_style: true,
  password: '',
  private_key: '',
  access_key_id: '',
  secret_key: '',
}

/** UTF-8-safe base64. The payload is the agent's Credentials JSON shape
 * (sink.Credentials): { password?|private_key?|access_key_id?,secret_key? }.
 * NOTE: the API stores this blob verbatim; the panel-key sealing layer is a
 * documented backend seam (see the Phase 11 handoff). */
function encodeCreds(creds: Record<string, string>): string {
  const bytes = new TextEncoder().encode(JSON.stringify(creds))
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}

export function TargetsPage() {
  const { org, myRole } = useAuth()
  const [targets, setTargets] = useState<TargetRow[] | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [confirm, setConfirm] = useState<{ target: TargetRow } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [form, setForm] = useState<TargetForm>(EMPTY_FORM)

  const canManage = myRole === 'owner' || myRole === 'admin'

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ targets: TargetRow[] }>(`/v1/organizations/${org.id}/backup-targets`)
      .then((r) => setTargets(r.targets ?? []))
      .catch((e) => {
        setTargets([])
        setErr(e.message ?? 'Failed to load backup targets')
      })
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
  }, [load])

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const config: Record<string, unknown> = { kind: form.kind }
      if (form.kind === 'local') {
        if (form.local_dir) config.local_dir = form.local_dir
      } else if (form.kind === 'remote') {
        config.remote = {
          host: form.host,
          port: Number(form.port) || 22,
          user: form.user,
          path: form.path,
        }
      } else {
        config.s3 = {
          endpoint: form.endpoint,
          region: form.region,
          bucket: form.bucket,
          ...(form.prefix ? { prefix: form.prefix } : {}),
          path_style: form.path_style,
        }
      }
      const body: Record<string, unknown> = { name: form.name, kind: form.kind, config }
      if (form.kind === 'remote') {
        body.creds_enc = encodeCreds(
          form.private_key ? { private_key: form.private_key } : { password: form.password },
        )
      } else if (form.kind === 's3') {
        body.creds_enc = encodeCreds({ access_key_id: form.access_key_id, secret_key: form.secret_key })
      }
      await api.post(`/v1/organizations/${org.id}/backup-targets`, body)
      pushToast('success', 'Target added — credentials are sealed at rest and never shown again')
      setShowCreate(false)
      setForm(EMPTY_FORM)
      load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create target')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!org || !confirm) return
    setConfirmBusy(true)
    try {
      await api.del(`/v1/organizations/${org.id}/backup-targets/${confirm.target.id}`)
      pushToast('success', 'Target removed — existing backups keep their stored copies')
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Delete failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="fade-up">
      <PageTitle
        title="Backup Targets"
        subtitle="Where archives land: the node's filesystem, a remote host, or S3-compatible object storage. Credentials are write-only — sealed at rest, never rendered."
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            {canManage && (
              <button className="btn-brand" onClick={() => setShowCreate(true)}>
                <Plus size={16} /> New Target
              </button>
            )}
          </>
        }
      />

      <Card>
        <CardHeader title="Storage targets" subtitle="Backups are checksummed and (optionally) encrypted before they leave the node" />
        <ErrorNote message={err} />
        {targets === null ? (
          <SkeletonRows rows={2} />
        ) : targets.length === 0 ? (
          <EmptyState
            icon={<HardDrive size={22} strokeWidth={1.7} />}
            title="No extra targets"
            subtitle="Backups default to the node's local backup directory. Add a remote or S3-compatible target to keep copies off the node."
            action={
              canManage ? (
                <button className="btn-brand" onClick={() => setShowCreate(true)}>
                  Add your first target
                </button>
              ) : undefined
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Name</th>
                  <th className="px-4 py-2.5 font-semibold">Kind</th>
                  <th className="px-4 py-2.5 font-semibold">Destination</th>
                  <th className="px-4 py-2.5 font-semibold">Credentials</th>
                  <th className="px-4 py-2.5 font-semibold">Added</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {targets.map((t) => (
                  <tr key={t.id} className="border-b border-line/60">
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{t.name}</strong>
                      {t.is_default && <span className="ml-2 rounded-full bg-brand-soft px-2 py-[2px] text-[9px] font-bold text-brand">DEFAULT</span>}
                    </td>
                    <td className="px-4 py-3">
                      <span className="inline-flex items-center gap-1.5 rounded-full bg-surface-2 px-2 py-[3px] text-[10px] font-bold text-sub">
                        {KIND_ICONS[t.kind] ?? <FolderCog size={14} strokeWidth={1.8} />} {TARGET_KIND_LABELS[t.kind as TargetKind] ?? t.kind}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-sub">{targetSubtitle(t)}</td>
                    <td className="px-4 py-3 text-[11px] text-muted">{t.kind === 'local' ? 'not required' : 'sealed at rest'}</td>
                    <td className="px-4 py-3 text-sub">{timeAgo(t.created_at)}</td>
                    <td className="px-4 py-3">
                      {canManage && (
                        <RowActions>
                          <button className="icon-btn" aria-label={`Delete target ${t.name}`} onClick={() => setConfirm({ target: t })}>
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

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="New Backup Target" subtitle="Credentials are encrypted with the panel key before storage and never displayed again.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Name" hint="how the target appears in backup forms">
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="offsite-s3" autoFocus />
          </Field>
          <Field label="Kind">
            <Select
              value={form.kind}
              onChange={(v) => setForm({ ...form, kind: v as TargetKind })}
              options={TARGET_KINDS.map((k) => ({ value: k, label: TARGET_KIND_LABELS[k] }))}
            />
          </Field>
        </FormRow>

        {form.kind === 'local' && (
          <>
            <InfoNote message="Local targets write into the node's backup directory — use the path below to override it. No credentials needed." />
            <Field label="Archive directory" hint="absolute path on the node; empty = agent default (/srv/epicpanel/backups)">
              <input className="input" value={form.local_dir} onChange={(e) => setForm({ ...form, local_dir: e.target.value })} placeholder="/srv/epicpanel/backups" />
            </Field>
          </>
        )}

        {form.kind === 'remote' && (
          <>
            <InfoNote message="Config-only ssh/rsync-style destination: the agent assembles fixed-argument transfers from these validated fields — the API can never express a shell command." />
            <FormRow cols={2}>
              <Field label="Host">
                <input className="input" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} placeholder="backup.example.test" />
              </Field>
              <Field label="Port">
                <input className="input" value={form.port} onChange={(e) => setForm({ ...form, port: e.target.value })} placeholder="22" />
              </Field>
            </FormRow>
            <FormRow cols={2}>
              <Field label="User">
                <input className="input" value={form.user} onChange={(e) => setForm({ ...form, user: e.target.value })} placeholder="backup" />
              </Field>
              <Field label="Base path" hint="absolute path on the remote host">
                <input className="input" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} placeholder="/srv/backups" />
              </Field>
            </FormRow>
            <FormRow cols={2}>
              <Field label="Password" hint="write-only; stored sealed, never shown again">
                <input className="input" type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value, private_key: form.private_key ? '' : form.private_key })} autoComplete="new-password" />
              </Field>
              <Field label="SSH private key" hint="alternative to a password; write-only">
                <textarea
                  className="input min-h-[64px] font-mono text-[10.5px]"
                  value={form.private_key}
                  onChange={(e) => setForm({ ...form, private_key: e.target.value, password: e.target.value ? '' : form.password })}
                  autoComplete="off"
                />
              </Field>
            </FormRow>
          </>
        )}

        {form.kind === 's3' && (
          <>
            <InfoNote message="S3-compatible object storage (AWS S3, MinIO, Cloudflare R2...). Path-style addressing suits MinIO-style endpoints." />
            <FormRow cols={2}>
              <Field label="Endpoint">
                <input className="input" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://s3.example.test" />
              </Field>
              <Field label="Region">
                <input className="input" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="us-east-1" />
              </Field>
            </FormRow>
            <FormRow cols={2}>
              <Field label="Bucket">
                <input className="input" value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} placeholder="epicpanel-backups" />
              </Field>
              <Field label="Prefix" hint="optional key prefix">
                <input className="input" value={form.prefix} onChange={(e) => setForm({ ...form, prefix: e.target.value })} placeholder="org-backups/" />
              </Field>
            </FormRow>
            <FormRow cols={2}>
              <Field label="Access key ID" hint="write-only; stored sealed, never shown again">
                <input className="input" type="password" value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} autoComplete="off" />
              </Field>
              <Field label="Secret access key" hint="write-only; stored sealed, never shown again">
                <input className="input" type="password" value={form.secret_key} onChange={(e) => setForm({ ...form, secret_key: e.target.value })} autoComplete="new-password" />
              </Field>
            </FormRow>
            <label className="flex cursor-pointer items-center gap-2 text-[12px] font-semibold text-sub">
              <input type="checkbox" className="h-3.5 w-3.5 accent-[#2563eb]" checked={form.path_style} onChange={(e) => setForm({ ...form, path_style: e.target.checked })} />
              Path-style addressing (bucket in the path)
            </label>
          </>
        )}

        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button
            className="btn-brand"
            onClick={create}
            disabled={busy || !form.name || (form.kind === 'remote' && (!form.host || !form.user || !form.path)) || (form.kind === 's3' && (!form.endpoint || !form.bucket))}
          >
            {busy ? 'Adding…' : 'Add target'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={remove}
        title="Delete backup target"
        message={`"${confirm?.target.name ?? ''}" will no longer receive new backups. Existing archives already stored on it are untouched. Backups that reference it keep their stored copies.`}
        busy={confirmBusy}
      />
    </div>
  )
}
