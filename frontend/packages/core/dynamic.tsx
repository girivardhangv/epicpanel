// DynamicResourcesCard — shared component for the dynamic resources feature
// (traffic-adaptive allocation + bot defense + Free Perk overlay). Rendered
// by both site detail pages; a pure client of the dedicated API surface so
// the card only ever shows the backend's decisions. Lives in core (not ui)
// because it is data-coupled; ui components stay data-free.
import { useCallback, useEffect, useState } from 'react'
import { dynamicApi } from './api'
import type { DynamicStatus, FreePerkStatus, Website } from './api'

const tierLabel = (t: number) => (t <= 0 ? 'floor (minimum)' : t === 1 ? 'base (plan)' : `${[1, 1, 2, 4, 8][t] ?? t}x plan`)

const stateBadge: Record<string, { label: string; cls: string }> = {
  active: { label: 'Active', cls: 'bg-green-500/15 text-green-500' },
  busy: { label: 'Protected (busy)', cls: 'bg-amber-500/15 text-amber-500' },
  suspended_attack: { label: 'Suspended — attack detected', cls: 'bg-red-500/15 text-red-500' },
}

function Toggle({ on, onChange, disabled }: { on: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${on ? 'bg-green-600' : 'bg-gray-600'} ${disabled ? 'opacity-50' : 'cursor-pointer'}`}
    >
      <span className={`absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all ${on ? 'left-[18px]' : 'left-0.5'}`} />
    </button>
  )
}

export function DynamicResourcesCard({
  orgId,
  website,
  canManage,
  toast,
  onChanged,
}: {
  orgId: string
  website: Website
  canManage: boolean
  toast?: (kind: 'success' | 'error', message: string) => void
  onChanged?: () => void
}) {
  const [status, setStatus] = useState<DynamicStatus | null>(null)
  const [perk, setPerk] = useState<FreePerkStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(() => {
    dynamicApi.status(orgId, website.id).then(setStatus).catch(() => setStatus(null))
    dynamicApi.orgPerk(orgId).then(setPerk).catch(() => setPerk(null))
  }, [orgId, website.id])

  useEffect(() => {
    void load()
  }, [load])

  const act = async (fn: () => Promise<unknown>, okMsg: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      toast?.('success', okMsg)
      load()
      onChanged?.()
    } catch (ex: any) {
      const msg = ex?.message ?? 'Action failed'
      setError(msg)
      toast?.('error', msg)
    } finally {
      setBusy(false)
    }
  }

  const st = status?.state ?? website.dynamic_state ?? 'active'
  const badge = stateBadge[st] ?? stateBadge.active
  const verdict = status?.trend?.last_verdict

  return (
    <Card>
      <CardHeader
        className="mx-5 mt-4 !mb-0"
        title="Dynamic Resources"
        subtitle="Traffic-adaptive allocation with bot defense"
        right={<span className={`rounded px-2 py-0.5 text-[10px] font-semibold ${badge.cls}`}>{badge.label}</span>}
      />
      <div className="px-5 pb-4 pt-3">
        {!status ? (
          <p className="text-[11px] text-muted">{error || 'Dynamic resources status unavailable.'}</p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-[11px] text-muted">
              <label className="flex items-center gap-2">
                <Toggle
                  on={status.enabled}
                  disabled={!canManage || busy}
                  onChange={(v) =>
                    act(
                      () => dynamicApi.setEnabled(orgId, website.id, v),
                      v ? 'Dynamic resources enabled.' : 'Dynamic resources disabled.',
                    )
                  }
                />
                <span className={status.enabled ? 'text-ink' : ''}>Adaptive allocation {status.enabled ? 'on' : 'off'}</span>
              </label>
              {!status.global_enabled && <span className="rounded bg-amber-500/10 px-2 py-0.5 text-amber-500">Panel-wide toggle is OFF</span>}
              {status.free_perk && <span className="rounded bg-blue-500/15 px-2 py-0.5 text-blue-400">Free Perk applied</span>}
            </div>

            <div className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Tier" value={tierLabel(status.tier)} />
              <Stat label="Memory ceiling" value={status.effective_limits?.memory_mb ? `${status.effective_limits.memory_mb} MB` : 'plan default'} />
              <Stat
                label="CPU ceiling"
                value={status.effective_limits?.cpu_percent ? `${Math.round(status.effective_limits.cpu_percent)}%` : 'plan default'}
              />
              <Stat label="Last score" value={verdict ? `${verdict.score.toFixed(1)} (${verdict.class})` : '—'} />
            </div>

            {st === 'suspended_attack' && (
              <div className="mt-3 rounded border border-red-500/30 bg-red-500/10 p-3 text-[11px] text-red-400">
                This site was suspended automatically: repeated bot/attack traffic was detected. Restore it when the traffic is clean.
                {canManage && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => act(() => dynamicApi.restore(orgId, website.id), 'Site restore queued.')}
                    className="ml-3 rounded bg-red-500/20 px-2 py-1 font-semibold hover:bg-red-500/30"
                  >
                    Restore site
                  </button>
                )}
              </div>
            )}

            {!website.free_perk && canManage && perk && perk.remaining > 0 && (
              <button
                type="button"
                disabled={busy}
                onClick={() => act(() => dynamicApi.assignPerk(orgId, website.id), 'Free Perk assigned to this site.')}
                className="mt-3 rounded bg-blue-500/15 px-2 py-1 text-[11px] font-semibold text-blue-400 hover:bg-blue-500/25"
              >
                Apply Free Perk ({perk.used}/{perk.max_sites} used
                {perk.package ? ` — ${perk.package.memory_limit_mb}MB RAM, ${Math.round(perk.package.cpu_cores * 100)}% CPU` : ''})
              </button>
            )}
            {website.free_perk && canManage && (
              <button
                type="button"
                disabled={busy}
                onClick={() => act(() => dynamicApi.removePerk(orgId, website.id), 'Free Perk removed.')}
                className="mt-3 rounded bg-gray-500/15 px-2 py-1 text-[11px] font-semibold text-muted hover:bg-gray-500/25"
              >
                Remove Free Perk
              </button>
            )}

            {(status.recent_events?.length ?? 0) > 0 && (
              <div className="mt-4 border-t border-line pt-3">
                <p className="mb-1.5 text-[10px] font-bold uppercase tracking-wide text-muted">Recent decisions</p>
                <ul className="space-y-1 text-[11px] text-muted">
                  {status.recent_events!.slice(0, 6).map((e, i) => (
                    <li key={i} className="flex justify-between gap-3">
                      <span className="text-ink">{e.kind.replace(/_/g, ' ')}</span>
                      <span className="truncate" title={e.reason ?? ''}>
                        {e.reason ?? ''}
                      </span>
                      <span className="shrink-0">{new Date(e.created_at).toLocaleString()}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {error && <p className="mt-2 text-[11px] text-red-400">{error}</p>}
          </>
        )}
      </div>
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded bg-black/20 p-2.5">
      <p className="text-[10px] uppercase tracking-wide text-muted">{label}</p>
      <p className="mt-0.5 text-[13px] font-semibold text-ink">{value}</p>
    </div>
  )
}

// Local Card/Header primitives matching the design-system classes (kept here
// to avoid core -> ui imports; ui already imports core types).
function Card({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <div className={`card p-5 ${className}`}>{children}</div>
}

function CardHeader({ title, subtitle, right, className = '' }: { title: string; subtitle?: string; right?: React.ReactNode; className?: string }) {
  return (
    <div className={`mb-4 flex items-center justify-between gap-3 border-b border-line pb-3 ${className}`}>
      <div>
        <h3 className="text-[12px] font-bold tracking-[-.01em] text-ink">{title}</h3>
        {subtitle && <span className="mt-0.5 block text-[10px] text-muted">{subtitle}</span>}
      </div>
      {right}
    </div>
  )
}
