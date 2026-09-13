/* Activity timeline (verbatim master-doc component list). One shared
 * implementation for the customer Activity screen, admin drill-downs and any
 * job/event feed. No emoji — Lucide icon or plain dot per entry.
 */
import { ReactNode } from 'react'

export type TimelineTone = 'blue' | 'green' | 'amber' | 'purple' | 'red' | 'neutral'

export interface TimelineItem {
  /** Stable key for React reconciliation. */
  id: string
  /** Primary line (what happened). */
  title: ReactNode
  /** Secondary line (why / detail). */
  description?: ReactNode
  /** Optional leading Lucide icon (size ~12-13). Falls back to a dot. */
  icon?: ReactNode
  tone?: TimelineTone
  /** Human timestamp label, e.g. "18.4s ago" or "2026-09-08 14:02". */
  timestamp?: ReactNode
  /** Right-aligned extra metadata (actor, job id, badge...). */
  meta?: ReactNode
}

const TONE_DOT: Record<TimelineTone, string> = {
  blue: 'bg-brand-soft text-brand',
  green: 'bg-ok-soft text-ok',
  amber: 'bg-warn-soft text-warn',
  purple: 'bg-purple-soft text-purple',
  red: 'bg-danger-soft text-danger',
  neutral: 'bg-surface-2 text-muted',
}

const TONE_LINE: Record<TimelineTone, string> = {
  blue: 'bg-[#c7d5ff]',
  green: 'bg-[#cef0e1]',
  amber: 'bg-[#ffe8b1]',
  purple: 'bg-[#e3d8ff]',
  red: 'bg-[#ffd0d7]',
  neutral: 'bg-line',
}

/**
 * Vertical activity timeline. Renders an accessible ordered list; the
 * connector line is decorative (aria-hidden). Compact by default to match
 * the ref density.
 */
export function Timeline({ items, ariaLabel = 'Activity timeline', className = '' }: {
  items: TimelineItem[]
  ariaLabel?: string
  className?: string
}) {
  if (items.length === 0) {
    return (
      <div className={`px-4 py-8 text-center text-[12px] text-muted ${className}`}>
        Nothing has happened yet.
      </div>
    )
  }
  return (
    <ol role="list" aria-label={ariaLabel} className={`relative ${className}`}>
      {items.map((it, i) => {
        const last = i === items.length - 1
        const tone = it.tone ?? 'neutral'
        return (
          <li key={it.id} className="relative flex gap-3 pb-4 last:pb-0">
            {/* Connector line between dots */}
            {!last && (
              <span
                aria-hidden="true"
                className={`absolute left-[13px] top-[30px] bottom-0 w-px ${TONE_LINE[tone]}`}
              />
            )}
            {/* Dot / icon node */}
            <span
              aria-hidden="true"
              className={`relative z-10 grid h-[27px] w-[27px] flex-none place-items-center rounded-full ${TONE_DOT[tone]}`}
            >
              {it.icon ?? <span className="h-[7px] w-[7px] rounded-full bg-current" />}
            </span>
            {/* Copy */}
            <div className="min-w-0 flex-1 pt-[3px]">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
                <strong className="min-w-0 text-[11.5px] font-bold leading-snug text-ink">{it.title}</strong>
                {it.timestamp && (
                  <span className="whitespace-nowrap text-[9.5px] font-semibold text-muted">{it.timestamp}</span>
                )}
              </div>
              {it.description && (
                <div className="mt-0.5 text-[10.5px] leading-relaxed text-muted">{it.description}</div>
              )}
              {it.meta && <div className="mt-1">{it.meta}</div>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
