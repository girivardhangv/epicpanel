import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Bot, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { api, useAuth, timeAgo, fmtBytes } from '@epicpanel/core'
import type { FreshnessState } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, RowActions, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote, Select, FormRow, ConfirmDialog } from '@epicpanel/forms'
import { FreshnessBadge } from '@epicpanel/ui'

/** Wire shape of a bot instance (Phase 8 API). Values of env vars NEVER
 * appear anywhere — the API exposes keys only. */
export interface BotInstance {
  id: string
  organization_id: string
  server_id: string
  name: string
  runtime: string
  runtime_version: string
  status: string
  desired_state: string
  startup_file: string
  startup_command: string
  restart_policy: string
  max_restarts: number
  restart_count: number
  git_repo_url: string
  git_branch: string
  env_keys: string[]
  last_error?: string
  unit_state?: string
  agent_seen_at?: string
  created_at: string
  updated_at: string
  metrics?: BotMetrics
}

export interface BotMetrics {
  kind: string
  status?: string
  cpu_percent?: number
  memory_bytes?: number
  net_rx_bps?: number
  net_tx_bps?: number
  disk_used_mb?: number
  uptime_s?: number
  restart_count?: number
  freshness?: { state?: string; age_ms?: number }
}

export interface RuntimeOffer {
  runtime: string
  label: string
  versions: string[]
  default: string
}

function freshnessOf(bot: BotInstance): { state: FreshnessState; ageMs: number } {
  const f = bot.metrics?.freshness as { state?: string; age_ms?: number } | undefined
  if (!f || !f.state) return { state: 'OFFLINE', ageMs: 0 }
  const state: FreshnessState = f.state === 'LIVE' ? 'LIVE' : f.state === 'STALE' ? 'STALE' : 'OFFLINE'
  return { state, ageMs: f.age_ms ?? 0 }
}
export function BotsPage() {
  const { org } = useAuth()
  const navigate = useNavigate()
  const [bots, setBots] = useState<BotInstance[] | null>(null)
  const [offers, setOffers] = useState<RuntimeOffer[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [confirm, setConfirm] = useState<{ bot: BotInstance } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ name: '', runtime: 'node', runtime_version: '', startup_file: '', git_repo_url: '', git_branch: 'main', git_token: '' })

  const load = useCallback(() => {
    if (!org) return
    api
      .get<{ bots: BotInstance[] }>(`/v1/organizations/${org.id}/bots`)
      .then((r) => setBots(r.bots ?? []))
      .catch((e) => {
        setBots([])
        setErr(e.message ?? 'Failed to load bots')
      })
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load()
    if (!org) return
    api
      .get<{ runtimes: RuntimeOffer[] }>(`/v1/organizations/${org.id}/bots/runtime-offers`)
      .then((r) => setOffers(r.runtimes ?? []))
      .catch(() => setOffers([]))
  }, [load, org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    if (!org) return
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name: form.name,
        runtime: form.runtime,
        startup_file: form.startup_file || undefined,
        git_repo_url: form.git_repo_url || undefined,
        git_branch: form.git_branch || undefined,
      }
      if (form.runtime_version) body.runtime_version = form.runtime_version
      if (form.git_token) body.git_token = form.git_token
      const r = await api.post<BotInstance>(`/v1/organizations/${org.id}/bots`, body)
      pushToast('success', 'Bot created — install queued')
      setShowCreate(false)
      setForm({ name: '', runtime: form.runtime, runtime_version: '', startup_file: '', git_repo_url: '', git_branch: 'main', git_token: '' })
      navigate(`/bots/${r.id}`)
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to create bot')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!org || !confirm) return
    setConfirmBusy(true)
    try {
      await api.del(`/v1/organizations/${org.id}/bots/${confirm.bot.id}`)
      pushToast('success', 'Deletion queued')
      setConfirm(null)
      load()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Delete failed')
    } finally {
      setConfirmBusy(false)
    }
  }

  const offer = offers.find((o) => o.runtime === form.runtime)
  const versionOptions = (offer?.versions ?? []).map((v) => ({ value: v, label: v }))
  if (versionOptions.length > 0) versionOptions.unshift({ value: '', label: `default (${offer?.default ?? ''})` })

  return (
    <div className="fade-up">
      <PageTitle
        title="Bots"
        subtitle="Discord bot instances — deploy, run and monitor as first-class workloads"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-brand" onClick={() => setShowCreate(true)}>
              <Plus size={16} /> New Bot
            </button>
          </>
        }
      />

      <Card>
        <CardHeader title="Bot Instances" subtitle="Each bot is an isolated unit with its own user, limits and console" />
        {bots === null ? (
          <SkeletonRows rows={2} />
        ) : bots.length === 0 ? (
          <EmptyState
            icon={<Bot size={22} strokeWidth={1.7} />}
            title="No bots yet"
            subtitle="Create a bot, upload files or point it at a git repo, then start it."
            action={<button className="btn-brand" onClick={() => setShowCreate(true)}>Create your first bot</button>}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="border-b border-line text-left text-[10.5px] uppercase tracking-wide text-muted">
                  <th className="px-4 py-2.5 font-semibold">Name</th>
                  <th className="px-4 py-2.5 font-semibold">Runtime</th>
                  <th className="px-4 py-2.5 font-semibold">Status</th>
                  <th className="px-4 py-2.5 font-semibold">CPU</th>
                  <th className="px-4 py-2.5 font-semibold">Memory</th>
                  <th className="px-4 py-2.5 font-semibold">Restarts</th>
                  <th className="px-4 py-2.5 font-semibold">Freshness</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {bots.map((b) => (
                  <tr key={b.id} className="cursor-pointer border-b border-line/60 transition hover:bg-surface-2" onClick={() => navigate(`/bots/${b.id}`)}>
                    <td className="px-4 py-3">
                      <strong className="text-[13px] text-ink">{b.name}</strong>
                      <span className="block text-[10.5px] text-muted">updated {timeAgo(b.updated_at)}</span>
                    </td>
                    <td className="px-4 py-3 text-sub">{b.runtime} {b.runtime_version}</td>
                    <td className="px-4 py-3"><StatusBadge status={b.status} /></td>
                    <td className="px-4 py-3 text-sub">{b.metrics?.cpu_percent != null ? `${b.metrics.cpu_percent.toFixed(1)}%` : '—'}</td>
                    <td className="px-4 py-3 text-sub">{b.metrics?.memory_bytes ? fmtBytes(b.metrics.memory_bytes) : '—'}</td>
                    <td className="px-4 py-3 text-sub">{b.metrics?.restart_count ?? b.restart_count}</td>
                    <td className="px-4 py-3">
                      {(() => {
                        const f = freshnessOf(b)
                        return <FreshnessBadge state={f.state} ageMs={f.ageMs} label="Metrics" />
                      })()}
                    </td>
                    <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}>
                      <RowActions>
                        <button className="btn-ghost" onClick={() => navigate(`/bots/${b.id}`)}>Open</button>
                        <button className="icon-btn" aria-label="Delete bot" onClick={() => setConfirm({ bot: b })}>
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

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="Create Bot" subtitle="The instance is created on the node with an isolated runtime environment.">
        <ErrorNote message={err} />
        <FormRow cols={2}>
          <Field label="Name" hint="lowercase letters, digits, - and _">
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="moderator-bot" autoFocus />
          </Field>
          <Field label="Runtime">
            <Select
              value={form.runtime}
              onChange={(v) => setForm({ ...form, runtime: v, runtime_version: '' })}
              options={offers.map((o) => ({ value: o.runtime, label: o.label }))}
              placeholder="pick runtime"
            />
          </Field>
        </FormRow>
        <FormRow cols={2}>
          <Field label="Version" hint={offer && offer.versions.length === 0 ? 'coming soon' : undefined}>
            <Select
              value={form.runtime_version}
              onChange={(v) => setForm({ ...form, runtime_version: v })}
              options={versionOptions}
              placeholder="default"
              disabled={!offer || offer.versions.length === 0}
            />
          </Field>
          <Field label="Startup file" hint="e.g. index.js / bot.py">
            <input className="input" value={form.startup_file} onChange={(e) => setForm({ ...form, startup_file: e.target.value })} placeholder="index.js" />
          </Field>
        </FormRow>
        <div className="section-title mt-2">Git deployment (optional)</div>
        <FormRow cols={2}>
          <Field label="Repository URL" hint="https:// or git@ remote">
            <input className="input" value={form.git_repo_url} onChange={(e) => setForm({ ...form, git_repo_url: e.target.value })} placeholder="https://github.com/you/bot" />
          </Field>
          <Field label="Branch">
            <input className="input" value={form.git_branch} onChange={(e) => setForm({ ...form, git_branch: e.target.value })} placeholder="main" />
          </Field>
        </FormRow>
        <Field label="Access token (optional)" hint="stored encrypted; never shown again">
          <input className="input" type="password" value={form.git_token} onChange={(e) => setForm({ ...form, git_token: e.target.value })} placeholder="ghp_…" />
        </Field>
        <div className="mt-4 flex justify-end gap-2">
          <button className="btn-ghost" onClick={() => setShowCreate(false)}>Cancel</button>
          <button className="btn-brand" onClick={create} disabled={busy || !form.name}>
            {busy ? 'Creating…' : 'Create Bot'}
          </button>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={remove}
        title="Delete bot"
        message={`"${confirm?.bot.name ?? ''}" will be stopped and its files removed. This cannot be undone.`}
        busy={confirmBusy}
      />
    </div>
  )
}
