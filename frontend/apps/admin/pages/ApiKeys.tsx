import { useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { api, timeAgo } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, PageTitle, StatCard, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { Modal, Field, ErrorNote } from '@epicpanel/forms'

/**
 * Platform admin API keys (epa_) — the machine counterpart of an admin
 * session: cross-org reach plus the /v1/admin* surface, always scope-gated.
 * Server-side rules mirrored here: listing needs admin:read; create/revoke
 * require THIS interactive admin session (a key can never mint or revoke
 * keys), so the create/revoke buttons are the only paths to those endpoints.
 */
interface AdminApiKey {
  id: string
  name: string
  scopes: string[]
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
  created_at: string
}

const SCOPE_GROUPS: { group: string; scopes: { label: string; value: string }[] }[] = [
  {
    group: 'Admin surface',
    scopes: [
      { label: 'Admin read (WHM views, jobs console, settings)', value: 'admin:read' },
      { label: 'Admin write (users, packages, billing admin, job retry…)', value: 'admin:write' },
    ],
  },
  {
    group: 'Organizations',
    scopes: [
      { label: 'Org read', value: 'org:read' },
      { label: 'Org write (create orgs, members, tokens)', value: 'org:write' },
      { label: 'Websites read / write (sites, files, cron, FTP, SSH, PHP, apps)', value: 'websites:write' },
      { label: 'Websites read only', value: 'websites:read' },
      { label: 'Databases read / write', value: 'databases:write' },
      { label: 'Databases read only', value: 'databases:read' },
      { label: 'Domains, DNS & SSL read / write', value: 'domains:write' },
      { label: 'Domains read only', value: 'domains:read' },
      { label: 'Servers & runtimes read / write', value: 'servers:write' },
      { label: 'Deployments read / write', value: 'deployments:write' },
      { label: 'Backups read / write', value: 'backups:write' },
      { label: 'Billing read / write (org side)', value: 'billing:write' },
      { label: 'Alerts read / write', value: 'alerts:write' },
      { label: 'Monitoring read', value: 'monitoring:read' },
      { label: 'Audit read', value: 'audit:read' },
    ],
  },
]

const READ_SCOPES = new Set(['websites:read', 'databases:read', 'domains:read', 'servers:read', 'deployments:read', 'backups:read', 'billing:read', 'monitoring:read', 'audit:read', 'org:read'])

export function ApiKeysPage() {
  const [keys, setKeys] = useState<AdminApiKey[] | null>(null)
  const [show, setShow] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState<{ name: string; full: boolean; picked: string[]; days: number }>({ name: '', full: false, picked: [], days: 0 })
  const [created, setCreated] = useState<{ name: string; raw: string } | null>(null)
  const [revoke, setRevoke] = useState<AdminApiKey | null>(null)

  const load = async () => {
    const r = await api.get<{ admin_api_keys: AdminApiKey[] }>('/v1/admin/api-keys')
    setKeys(r.admin_api_keys ?? [])
  }
  useEffect(() => {
    load().catch(() => setKeys([]))
  }, [])

  const active = (keys ?? []).filter((k) => !k.revoked_at)

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      const scopes = form.full ? ['*'] : form.picked
      const r = await api.post<{ admin_api_key: AdminApiKey; raw_token: string }>('/v1/admin/api-keys', {
        name: form.name,
        scopes,
        ...(form.days > 0 ? { expires_in_days: form.days } : {}),
      })
      setShow(false)
      setForm({ name: '', full: false, picked: [], days: 0 })
      setCreated({ name: r.admin_api_key.name, raw: r.raw_token })
      await load()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create key')
    } finally {
      setBusy(false)
    }
  }

  const doRevoke = async () => {
    if (!revoke) return
    setBusy(true)
    try {
      await api.del('/v1/admin/api-keys/' + revoke.id)
      pushToast('success', 'Key revoked')
      setRevoke(null)
      await load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Failed to revoke key')
    } finally {
      setBusy(false)
    }
  }

  const toggle = (s: string) =>
    setForm((f) => (f.picked.includes(s) ? { ...f, picked: f.picked.filter((x) => x !== s) } : { ...f, picked: [...f.picked, s] }))

  const columns: Column<AdminApiKey>[] = [
    {
      key: 'name', header: 'Key', render: (r) => (
        <div className="flex items-center gap-2.5">
          <span className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand"><KeyRound size={13} /></span>
          <div>
            <div className="table-primary">{r.name}</div>
            <div className="table-secondary font-mono text-[9px]">epa_••••{r.id.slice(0, 4)}</div>
          </div>
        </div>
      ),
      filter: (r) => r.name,
    },
    { key: 'scopes', header: 'Scopes', render: (r) => <span className="table-secondary">{r.scopes.length > 8 ? `Full control (${r.scopes.length} scopes)` : r.scopes.join(', ')}</span> },
    {
      key: 'used', header: 'Last used', render: (r) => (
        r.last_used_at ? <span className="text-muted">{timeAgo(r.last_used_at)}</span> : <span className="badge-off">Never</span>
      ),
    },
    {
      key: 'expires', header: 'Expires', render: (r) => (
        r.expires_at ? <span className="text-muted">{new Date(r.expires_at).toLocaleDateString()}</span> : <span className="badge-neutral">Never</span>
      ),
    },
    {
      key: 'status', header: 'Status', render: (r) => (
        r.revoked_at ? <span className="badge-off">Revoked</span> : <span className="badge-ok">Active</span>
      ),
    },
    {
      key: 'actions', header: '', render: (r) => (
        r.revoked_at ? null : (
          <button className="btn-ghost !text-danger" onClick={() => setRevoke(r)} aria-label={`Revoke ${r.name}`}>
            <Trash2 size={13} /> Revoke
          </button>
        )
      ),
    },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="API Keys"
        subtitle="Platform admin keys (epa_) for full-panel automation — any organization plus the admin surface."
        actions={<button className="btn-primary" onClick={() => setShow(true)}><Plus size={14} /> Create key</button>}
      />

      <div className="mb-4 grid grid-cols-2 gap-3.5 xl:grid-cols-4">
        <StatCard label="Active keys" value={keys ? active.length : ''} loading={!keys} icon={<KeyRound size={15} strokeWidth={1.8} />} />
        <StatCard label="Revoked" value={keys ? (keys.length - active.length) : ''} tone="amber" loading={!keys} icon={<KeyRound size={15} strokeWidth={1.8} />} />
      </div>

      <Card className="mb-4">
        <CardHeader title="How keys work" subtitle="Scope-gated machine principals" />
        <div className="divide-y divide-line text-[9.5px] leading-relaxed text-muted">
          <p className="py-2.5">An <code className="text-ink">epa_</code> key acts as the platform admin across all organizations and can reach <code className="text-ink">/v1/admin*</code> with <code className="text-ink">admin:read</code>/<code className="text-ink">admin:write</code>. Org routes still demand the matching resource scope (<code className="text-ink">websites:write</code>, <code className="text-ink">databases:write</code>, …).</p>
          <p className="py-2.5">Keys are shown once at creation, stored hashed, and revocable instantly. Creation and revocation require this interactive admin session — a leaked key can never mint or revoke other keys.</p>
          <p className="py-2.5">Send as <code className="text-ink">Authorization: Bearer epa_…</code>. Full guide: docs/api-reference.md · contract: <code className="text-ink">GET /v1/openapi.json</code>.</p>
        </div>
      </Card>

      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={keys}
          rowKey={(r) => r.id}
          searchText={(r) => r.name}
          minWidth={860}
          empty={<EmptyState icon={<KeyRound size={20} />} title="No API keys" subtitle="Create a key to automate provisioning from CI, Terraform or scripts." />}
        />
      </Card>

      <Modal open={show} onClose={() => setShow(false)} title="Create platform API key" subtitle="The raw key is shown exactly once after creation." width="max-w-[620px]">
        <ErrorNote message={err} />
        <Field label="Name"><input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="provisioning-bot" /></Field>
        <Field label="Scopes" hint={form.full ? 'Wildcard: every scope granted.' : 'Pick what this key may do. Reads only? Pick the :read entries.'}>
          <label className="mb-1.5 flex items-center gap-2 rounded-[9px] border border-line px-3 py-2 text-[11px] text-ink">
            <input type="checkbox" checked={form.full} onChange={(e) => setForm({ ...form, full: e.target.checked })} />
            Full control — all scopes (<code>*</code>)
          </label>
          {!form.full && (
            <div className="max-h-[240px] overflow-y-auto rounded-[9px] border border-line p-2">
              {SCOPE_GROUPS.map((g) => (
                <div key={g.group} className="mb-2 last:mb-0">
                  <div className="px-1.5 pb-1 text-[9px] font-bold uppercase tracking-wide text-muted">{g.group}</div>
                  {g.scopes.map((s) => (
                    <label key={s.value} className="flex items-center gap-2 rounded-[7px] px-1.5 py-1.5 text-[10.5px] text-ink hover:bg-[#f6f8fd]">
                      <input
                        type="checkbox"
                        checked={form.picked.includes(s.value)}
                        onChange={() => toggle(s.value)}
                        onClick={(e) => {
                          // Selecting a write scope implies its read scope.
                          if (!e.currentTarget.checked) return
                          const read = s.value.replace(':write', ':read')
                          if (!READ_SCOPES.has(read) || form.picked.includes(read)) return
                          setForm((f) => ({ ...f, picked: [...f.picked, read] }))
                        }}
                      />
                      {s.label}
                    </label>
                  ))}
                </div>
              ))}
            </div>
          )}
        </Field>
        <Field label="Expires in days" hint="0 = never expires.">
          <input className="input" type="number" min={0} value={form.days} onChange={(e) => setForm({ ...form, days: Math.max(0, Number(e.target.value) || 0) })} />
        </Field>
        <div className="mt-3 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShow(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name || (!form.full && form.picked.length === 0)}>
            {busy ? (<><Spinner size={13} /> Creating…</>) : 'Create key'}
          </button>
        </div>
      </Modal>

      <Modal open={!!created} onClose={() => setCreated(null)} title="API key created" subtitle="Copy it now — it is not retrievable later." width="max-w-[560px]">
        <p className="mb-2 text-[11px] text-ink">Key <strong>{created?.name}</strong>:</p>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-[9px] border border-line bg-[#f6f8fd] px-3 py-2 font-mono text-[10.5px] text-ink">{created?.raw}</code>
          <button className="btn-ghost flex-none" onClick={() => { if (created) { void navigator.clipboard.writeText(created.raw); pushToast('success', 'Copied to clipboard') } }}>Copy</button>
        </div>
        <div className="mt-3 flex justify-end">
          <button className="btn-brand" onClick={() => setCreated(null)}>Done</button>
        </div>
      </Modal>

      <Modal open={!!revoke} onClose={() => setRevoke(null)} title="Revoke API key" subtitle={revoke ? `"${revoke.name}" stops working immediately.` : ''}>
        <div className="mt-3 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setRevoke(null)}>Cancel</button>
          <button className="btn-danger" onClick={doRevoke} disabled={busy}>{busy ? (<><Spinner size={13} /> Revoking…</>) : 'Revoke key'}</button>
        </div>
      </Modal>
    </div>
  )
}
