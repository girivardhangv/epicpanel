import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Gamepad2, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { api, useAuth, timeAgo, fmtBytes } from '@epicpanel/core'
import type { FreshnessState } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, FormRow, ConfirmDialog, InfoNote } from '@epicpanel/forms'
import { FreshnessBadge } from '@epicpanel/ui'

/** Wire shape of a Minecraft instance (Phase 7 API). The RCON password
 * NEVER appears anywhere — it is write-only ciphertext on the server. */
export interface MCInstance {
  id: string
  organization_id: string
  server_id: string
  name: string
  provider: string
  version: string
  java_major: number
  status: string
  desired_state: string
  port: number
  xmx_mb: number
  extra_args?: string[]
  restart_policy: string
  max_restarts: number
  restart_count: number
  property_keys?: string[]
  last_error?: string
  unit_state?: string
  agent_seen_at?: string
  created_at: string
  updated_at: string
  metrics?: MCMetrics
}

export interface MCMetrics {
  kind: string
  status?: string
  cpu_percent?: number
  memory_bytes?: number
  net_rx_bps?: number
  net_tx_bps?: number
  disk_used_mb?: number
  uptime_s?: number
  players?: number
  tps?: number
  mspt?: number
  restart_count?: number
  tps_source?: 'rcon' | 'waiting' | 'unsupported' | 'unavailable'
  freshness?: { state?: string; age_ms?: number }
}

export interface MCProviderOffer {
  provider: string
  label: string
  versions: string[]
  default: string
  source: string
  supports_tps: boolean
}

export function freshnessOf(inst: MCInstance): { state: FreshnessState; ageMs: number } {
  const f = inst.metrics?.freshness as { state?: string; age_ms?: number } | undefined
  if (!f || !f.state) return { state: 'OFFLINE', ageMs: 0 }
  const state: FreshnessState = f.state === 'LIVE' ? 'LIVE' : f.state === 'STALE' ? 'STALE' : 'OFFLINE'
  return { state, ageMs: f.age_ms ?? 0 }
}

export function MinecraftPage() {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [instances, setInstances] = useState<MCInstance[] | null>(null)
  const [offers, setOffers] = useState<MCProviderOffer[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [confirm, setConfirm] = useState<{ inst: MCInstance } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', provider: 'paper', version: '', eula: false, motd: '' })

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ instances: MCInstance[] }>(`/v1/organizations/${org.id}/minecraft`)
      .then((r) => setInstances(r.instances ?? []))
      .catch((e) => {
        setInstances([])
        setErr(e.message ?? 'Failed to load instances')
      })
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    if (!org) return
    api
      .get<{ providers: MCProviderOffer[] }>(`/v1/organizations/${org.id}/minecraft/provider-offers`)
      .then((r) => setOffers(r.providers ?? []))
      .catch(() => setOffers([]))
  }, [load, org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name: form.name,
        provider: form.provider,
        accept_eula: form.eula,
      }
      if (form.version) body.version = form.version
      if (form.motd) body.properties = { motd: form.motd }
      const r = await api.post<MCInstance>(`/v1/organizations/${org.id}/minecraft`, body)
      pushToast('success', 'Instance created — install queued')
      setShowCreate(false)
      setForm({ name: '', provider: form.provider, version: '', eula: false, motd: '' })
      navigate(`/minecraft/${r.id}`)
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create instance')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!org || !confirm) return
    setConfirmBusy(true)
    try {
      await api.del(`/v1/organizations/${org.id}/minecraft/${confirm.inst.id}`)
      pushToast('success', 'Deletion queued')
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Delete failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  const offer = offers.find((o) => o.provider === form.provider)
  const versionOptions = (offer?.versions ?? []).map((v) => ({ value: v, label: v }))
  if (versionOptions.length > 0) versionOptions.unshift({ value: '', label: `default (${offer?.default ?? ''})` })

  return (
    <div className="fade-up">
      <PageTitle
        title="Minecraft"
        subtitle="Minecraft server instances — isolated workloads with console, backups and live metrics"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-brand" onClick={() => setShowCreate(true)}>
              <Plus size={16} /> New Instance
            </button>
          </>
        }
      />

      <Card>
        <CardHeader title="Instances" subtitle="Each instance runs in an isolated workload with its own user, port and limits" />
        {instances === null ? (
          <SkeletonRows rows={2} />
        ) : instances.length === 0 ? (
          <EmptyState
            icon={<Gamepad2 size={22} strokeWidth={1.7} />}
            title="No Minecraft instances yet"
            subtitle="Create an instance, pick a server type and version, then start it."
            action={<button className="btn-brand" onClick={() => setShowCreate(true)}>Create your first instance</button>}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Name</th>
                  <th className="px-4 py-2.5 font-semibold">Server</th>
                  <th className="px-4 py-2.5 font-semibold">Status</th>
                  <th className="px-4 py-2.5 font-semibold">Players</th>
                  <th className="px-4 py-2.5 font-semibold">TPS</th>
                  <th className="px-4 py-2.5 font-semibold">CPU</th>
                  <th className="px-4 py-2.5 font-semibold">Memory</th>
                  <th className="px-4 py-2.5 font-semibold">Freshness</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {instances.map((m) => (
                  <tr key={m.id} className="cursor-pointer border-b border-line/60 transition hover:bg-surface-2" onClick={() => navigate(`/minecraft/${m.id}`)}>
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{m.name}</strong>
                      <span className="block text-[10.5px] text-muted">:{m.port} · updated {timeAgo(m.updated_at)}</span>
                    </td>
                    <td className="px-4 py-3 text-sub">{m.provider} {m.version}</td>
                    <td className="px-4 py-3"><StatusBadge status={m.status} /></td>
                    <td className="px-4 py-3 text-sub">{m.metrics?.players ?? '—'}</td>
                    <td className="px-4 py-3 text-sub">{formatTPS(m.metrics)}</td>
                    <td className="px-4 py-3 text-sub">{m.metrics?.cpu_percent != null ? `${m.metrics.cpu_percent.toFixed(1)}%` : '—'}</td>
                    <td className="px-4 py-3 text-sub">{m.metrics?.memory_bytes ? fmtBytes(m.metrics.memory_bytes) : '—'}</td>
                    <td className="px-4 py-3">
                      {(() => {
                        const f = freshnessOf(m)
                        return <FreshnessBadge state={f.state} ageMs={f.ageMs} label="Metrics" />
                      })()}
                    </td>
                    <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}>
                      <RowActions>
                        <button className="btn-ghost" onClick={() => navigate(`/minecraft/${m.id}`)}>Open</button>
                        <button className="icon-btn" aria-label="Delete instance" onClick={() => setConfirm({ inst: m })}>
                          <Trash2 size={15} />
                        </button>
                      </RowActions>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="Create Minecraft Instance" subtitle="The server JAR is fetched from the provider at install time — versions follow the live manifest.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Name" hint="lowercase letters, digits, - and _">
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="survival-1" autoFocus />
          </Field>
          <Field label="Server type">
            <Select
              value={form.provider}
              onChange={(v) => setForm({ ...form, provider: v, version: '' })}
              options={offers.map((o) => ({ value: o.provider, label: o.label }))}
              placeholder="pick server type"
            />
          </Field>
        </FormRow>
        <FormRow cols={2}>
          <Field label="Version" hint={offer ? `source: ${offer.source}` : undefined}>
            <Select
              value={form.version}
              onChange={(v) => setForm({ ...form, version: v })}
              options={versionOptions}
              placeholder="default"
              disabled={!offer || offer.versions.length === 0}
            />
          </Field>
          <Field label="MOTD (optional)">
            <input className="input" value={form.motd} onChange={(e) => setForm({ ...form, motd: e.target.value })} placeholder="Welcome!" />
          </Field>
        </FormRow>
        <label className="mt-1 flex cursor-pointer items-start gap-2 text-[12px] text-sub">
          <input type="checkbox" checked={form.eula} onChange={(e) => setForm({ ...form, eula: e.target.checked })} className="mt-0.5" />
          <span>
            I accept the Minecraft <span className="text-ink">EULA</span> on behalf of this server (required to boot).
          </span>
        </label>
        <InfoNote message="Ports are allocated from the plan automatically; the instance boots with the plan's CPU/RAM limits enforced by the node." />
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name || !form.eula}>
            {busy ? 'Creating…' : 'Create Instance'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={remove}
        title="Delete instance"
        message={`"${confirm?.inst.name ?? ''}" will be stopped, its files (including the world) removed. This cannot be undone.`}
        busy={confirmBusy}
      />
    </div>
  )
}

export function formatTPS(m?: MCMetrics): string {
  if (!m) return '—'
  if (m.tps_source === 'unsupported') return 'n/a'
  if (m.tps == null || m.tps <= 0) return '—'
  return m.tps.toFixed(2)
}
