// BandwidthCard — shared component for the bandwidth accounting + lifecycle
// feature. Rendered by both site detail pages; a pure client of the
// bandwidth/quota API surface, so the numbers shown are always the
// control plane's authoritative accounting (never frontend-derived).
//
// LIVE updates: the control plane publishes website.suspended /
// website.resumed / website.terminated on the /v1/ws bus; the card
// refetches on those (no polling) so a quota suspension flips the UI the
// moment it happens.
import { useEffect, useState } from 'react'
import { bandwidthApi, fmtBytes, ws } from './api'
import type { BandwidthSummary, Website } from './api'

export const suspensionReasonLabel: Record<string, string> = {
  manual: 'Suspended by an administrator',
  bandwidth_exhausted: 'Suspended — bandwidth limit reached',
  abuse: 'Suspended — abuse investigation',
  payment: 'Suspended — billing issue',
  admin: 'Suspended — administrative action',
  system: 'Suspended — system action',
  attack: 'Suspended — attack defense',
}

const fmtBps = (bps: number): string => {
  if (!bps || bps <= 0) return '0 bit/s'
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(2)} Mbit/s`
  if (bps >= 1_000) return `${(bps / 1_000).toFixed(1)} kbit/s`
  return `${Math.round(bps)} bit/s`
}

export function BandwidthCard({
  orgId,
  website,
  onChanged,
}: {
  orgId: string
  website: Pick<Website, 'id' | 'status' | 'suspension_reason'>
  onChanged?: () => void
}) {
  const [bw, setBw] = useState<BandwidthSummary | null>(null)

  useEffect(() => {
    let alive = true
    bandwidthApi.summary(orgId, website.id).then((b) => alive && setBw(b)).catch(() => alive && setBw(null))
    return () => {
      alive = false
    }
  }, [orgId, website.id])

  useEffect(() => {
    const off = ws.subscribe((msg: any) => {
      if (msg?.resource_id !== website.id) return
      if (msg?.type === 'website.suspended' || msg?.type === 'website.resumed' || msg?.type === 'website.terminated') {
        bandwidthApi.summary(orgId, website.id).then(setBw).catch(() => {})
        onChanged?.()
      }
    })
    return off
  }, [orgId, website.id, onChanged])

  if (!bw) return null

  const unlimited = !bw.quota.limit_bytes
  const pct = bw.quota.percentage ?? 0
  const suspended = bw.status === 'suspended' || website.status === 'suspended'
  const terminated = website.status === 'terminated'
  const reason = (bw.suspension?.reason as string) || website.suspension_reason || ''
  const tone = terminated ? 'red' : suspended ? 'amber' : pct >= 90 ? 'amber' : pct >= 75 ? 'purple' : 'green'

  return (
    <div className="card p-[14px]">
      <div className="flex items-start justify-between gap-2.5">
        <div className="flex items-center gap-2">
          <span className={`inline-flex h-7 w-7 items-center justify-center rounded-[8px] ${tone === 'red' ? 'bg-danger-soft text-danger' : tone === 'amber' ? 'bg-warn-soft text-warn' : 'bg-ok-soft text-ok'}`}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
            </svg>
          </span>
          <div>
            <div className="text-[11px] font-bold uppercase tracking-[.06em] text-muted">Bandwidth</div>
            <div className="text-[13px] font-semibold text-ink">
              {terminated
                ? 'Terminated'
                : suspended
                  ? (suspensionReasonLabel[reason] ?? 'Suspended')
                  : `${fmtBytes(bw.quota.used_bytes)} of ${unlimited ? 'unlimited' : fmtBytes(bw.quota.limit_bytes)}`}
            </div>
          </div>
        </div>
        <div className="text-right">
          <div className="text-[11px] font-semibold text-muted">{fmtBps(bw.rate.tx_bps)} out</div>
          <div className="text-[10.5px] text-muted">resets {new Date(bw.quota.resets_at).toLocaleDateString()}</div>
        </div>
      </div>
      {!unlimited && !terminated && (
        <div className="mt-2.5 h-1.5 overflow-hidden rounded-full bg-black/10 dark:bg-white/10">
          <div
            className={`h-full rounded-full transition-[width] duration-500 ${pct >= 90 ? 'bg-warn' : 'bg-ok'}`}
            style={{ width: `${Math.min(pct, 100)}%` }}
          />
        </div>
      )}
      {!unlimited && !terminated && (
        <div className="mt-1.5 flex justify-between text-[10.5px] text-muted">
          <span>{pct.toFixed(1)}% used</span>
          <span>{bw.quota.remaining_bytes != null ? `${fmtBytes(bw.quota.remaining_bytes)} left` : 'unlimited'}</span>
        </div>
      )}
      {terminated && (
        <div className="mt-2 text-[11px] text-muted">
          This site has been terminated. It serves a 410 page and its services are stopped; removal requires a purge.
        </div>
      )}
      {suspended && !terminated && (
        <div className="mt-2 text-[11px] text-muted">
          {bw.quota.used_bytes > 0 && bw.quota.limit_bytes > 0 && reason === 'bandwidth_exhausted'
            ? `Used ${fmtBytes(bw.quota.used_bytes)} of ${fmtBytes(bw.quota.limit_bytes)} this period. Usage resets ${new Date(bw.quota.resets_at).toLocaleDateString()}.`
            : 'The site serves a suspension page until it is resumed.'}
        </div>
      )}
    </div>
  )
}
