import { useCallback, useEffect, useState } from 'react'
import { Globe2, Ban, PlayCircle, ShieldAlert, CheckSquare, Square } from 'lucide-react'
import { api, fmtBytes } from '@epicpanel/core'
import { Card, EmptyState, StatusBadge, PageTitle, Initials, RowActions, pushToast } from '@epicpanel/ui'
import { ConfirmDialog } from '@epicpanel/forms'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { adminview } from '../adminview'
import type { AdminAccount } from '../adminview'

/**
 * Accounts: cross-org hosting accounts (Phase 6 work item — "filter accounts
 * across nodes"). Suspend/resume enqueue the same Phase 4 agent jobs the
 * customer flow uses; server-side authz + audit live on the adminview API.
 */
export function AccountsPage() {
  const [rows, setRows] = useState<AdminAccount[] | null>(null)
  const rowsAll = rows
  const [orgs, setOrgs] = useState<{ id: string; name: string }[]>([])
  const [org, setOrg] = useState('')
  const [status, setStatus] = useState('')
  const [server, setServer] = useState('')
  const [servers, setServers] = useState<{ id: string; name: string }[]>([])
  const [confirm, setConfirm] = useState<{ rows: AdminAccount[]; suspend: boolean } | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    const q: Record<string, string> = {}
    if (org) q.org_id = org
    if (status) q.status = status
    if (server) q.server_id = server
    const r = await adminview.accounts(q)
    setRows(r.accounts ?? [])
  }, [org, status, server])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    Promise.all([adminview.organizations(), adminview.servers()])
      .then(([o, s]) => {
        setOrgs((o.organizations ?? []).map((x) => ({ id: x.id, name: x.name })))
        setServers((s.servers ?? []).map((x) => ({ id: x.id, name: x.name })))
      })
      .catch(() => undefined)
  }, [])

  const act = async () => {
    if (!confirm) return
    setBusy(true)
    try {
      let ok = 0
      for (const row of confirm.rows) {
        try {
          if (confirm.suspend) await adminview.suspend(row.id)
          else await adminview.resume(row.id)
          ok++
        } catch {
          // per-row failures surface via the toast count; keep going (audited)
        }
      }
      pushToast(ok === confirm.rows.length ? 'success' : 'error',
        `${confirm.suspend ? 'Suspend' : 'Resume'} queued for ${ok}/${confirm.rows.length} account(s)`)
      setSelected(new Set())
      setConfirm(null)
      await load()
    } finally {
      setBusy(false)
    }
  }

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const bulk = (suspend: boolean) => {
    const rows = (rowsAll ?? []).filter((r) => selected.has(r.id))
    if (rows.length === 0) return
    setConfirm({ rows, suspend })
  }

  const columns: Column<AdminAccount>[] = [
    {
      key: 'sel',
      header: '',
      render: (r) => {
        const eligible = r.status === 'ready' || r.status === 'failed' || r.status === 'suspended'
        if (!eligible) return null
        const on = selected.has(r.id)
        return (
          <button className="icon-btn" title={on ? 'Unselect' : 'Select'} onClick={() => toggle(r.id)}>
            {on ? <CheckSquare size={12} /> : <Square size={12} />}
          </button>
        )
      },
    },
    {
      key: 'account',
      header: 'Account',
      render: (r) => (
        <div className="flex items-center gap-2.5">
          <Initials text={r.name} />
          <div>
            <div className="table-primary">{r.name}</div>
            <div className="table-secondary">{r.primary_domain || 'no domain'}</div>
          </div>
        </div>
      ),
      filter: (r) => `${r.name} ${r.primary_domain}`,
    },
    { key: 'org', header: 'Customer', render: (r) => <span className="text-sub">{r.org_name}</span>, filter: (r) => r.org_name },
    { key: 'plan', header: 'Plan', render: (r) => r.plan ?? 'Unassigned' },
    {
      key: 'server', header: 'Node', render: (r) => (
        <div>
          <div className="table-primary">{r.server_name}</div>
          <div className="table-secondary">:{r.backend_port || '—'}</div>
        </div>
      ),
      filter: (r) => r.server_name,
    },
    {
      key: 'usage', header: 'Disk', render: (r) => (
        <span className="text-sub">{r.usage_disk_mb ? fmtBytes(r.usage_disk_mb * 1024 * 1024) : '—'}</span>
      ),
    },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    {
      key: 'actions', header: '', render: (r) => (
        <RowActions>
          {r.status === 'ready' || r.status === 'failed' ? (
            <button className="icon-btn" title="Suspend account" onClick={() => setConfirm({ rows: [r], suspend: true })}>
              <Ban size={13} />
            </button>
          ) : r.status === 'suspended' ? (
            <button className="icon-btn" title="Resume account" onClick={() => setConfirm({ rows: [r], suspend: false })}>
              <PlayCircle size={13} />
            </button>
          ) : null}
        </RowActions>
      ),
    },
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Accounts" subtitle="Hosting accounts across all customers and nodes." />
      {selected.size > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 rounded-[11px] border border-[#cddaff] bg-brand-soft px-4 py-2.5">
          <span className="text-[11px] font-bold text-brand">{selected.size} selected</span>
          <span className="flex-1" />
          <button className="btn-ghost !min-h-[30px] !text-[10.5px]" onClick={() => bulk(true)}><Ban size={12} /> Suspend selected</button>
          <button className="btn-ghost !min-h-[30px] !text-[10.5px]" onClick={() => bulk(false)}><PlayCircle size={12} /> Resume selected</button>
          <button className="btn-ghost !min-h-[30px] !text-[10.5px]" onClick={() => setSelected(new Set())}>Clear</button>
        </div>
      )}
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.name} ${r.primary_domain} ${r.org_name} ${r.server_name} ${r.plan ?? ''}`}
          filterSlot={
            <>
              <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={org} onChange={(e) => setOrg(e.target.value)}>
                <option value="">All customers</option>
                {orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
              </select>
              <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={server} onChange={(e) => setServer(e.target.value)}>
                <option value="">All nodes</option>
                {servers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
              <select className="input h-[34px] w-auto cursor-pointer text-[11px]" value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">All statuses</option>
                <option value="ready">Ready</option>
                <option value="suspended">Suspended</option>
                <option value="failed">Failed</option>
                <option value="provisioning">Provisioning</option>
                <option value="pending">Pending</option>
              </select>
            </>
          }
          minWidth={880}
          empty={<EmptyState icon={<Globe2 size={20} />} title="No accounts" subtitle="No hosting accounts match the current filters." />}
        />
      </Card>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={act}
        busy={busy}
        title={confirm?.suspend ? 'Suspend account' : 'Resume account'}
        message={
          confirm
            ? confirm.suspend
              ? `${confirm.rows.length} account(s) will serve a static "account suspended" page until resumed. A suspend_website job is queued per account on its node. Actions are audited.`
              : `${confirm.rows.length} account(s) will be restored to their previous serving configuration. A resume_website job is queued per account on its node. Actions are audited.`
            : ''
        }
        confirmLabel={confirm ? (confirm.suspend ? `Suspend ${confirm.rows.length}` : `Resume ${confirm.rows.length}`) : 'Confirm'}
        danger={!!confirm?.suspend}
      />
    </div>
  )
}

// Re-exported so the customers page can share the org fetch without cycles.
export function useOrgOptions(): { orgs: { id: string; name: string }[]; error: string } {
  const [orgs, setOrgs] = useState<{ id: string; name: string }[]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    adminview.organizations()
      .then((r) => setOrgs(r.organizations ?? []))
      .catch((ex) => setError(ex.message ?? 'Failed to load customers'))
  }, [])
  return { orgs, error }
}

export function OrgSwitchHint() {
  return (
    <div className="flex items-center gap-2 text-[10px] text-muted">
      <ShieldAlert size={12} /> Cross-org actions are audited.
    </div>
  )
}

// Used by Customers.tsx to trigger org-scoped package assignment via the
// existing admin packages API (Phase 2 route, admin-gated).
export async function assignPackage(orgId: string, packageId: string) {
  return api.post(`/v1/admin/organizations/${orgId}/package`, { package_id: packageId })
}
