// Pterodactyl-style server management shell: power bar + stat gauges +
// vertical tab rail + content pane. Shared by the Minecraft and Discord bot
// server pages so both workloads feel like the same product (Pterodactyl UX).
import { ReactNode, useEffect, useRef, useState } from 'react'
import { ArrowLeft, Play, RotateCw, Square, Zap } from 'lucide-react'
import { StatusBadge } from '@/components/cards'

export interface PteroStat {
  label: string
  value: string
  hint?: string
  /** 0..100 — renders a usage bar under the value when present. */
  percent?: number | null
  tone?: 'brand' | 'warn' | 'danger'
}

export interface PteroTab {
  id: string
  label: string
  icon: ReactNode
}

const toneText: Record<string, string> = {
  brand: 'text-brand',
  warn: 'text-amber-400',
  danger: 'text-danger',
}

export function PteroServerPage({
  onBack, name, tagline, status, power, stats, tabs, active, onTab, children,
}: {
  onBack: { to: () => void; label: string }
  name: ReactNode
  tagline?: ReactNode
  status: string
  /** Power buttons; pass disabled flags per workload state machine. */
  power: {
    onStart?: () => void
    onRestart?: () => void
    onStop?: () => void
    onKill?: () => void
    busy?: boolean
  }
  stats: PteroStat[]
  tabs: PteroTab[]
  active: string
  onTab: (id: string) => void
  children: ReactNode
}) {
  const kill = (
    <button className="btn-danger" disabled={power.busy} onClick={power.onKill}>
      <Zap size={13} /> Kill
    </button>
  )
  return (
    <div className="p-6">
      <button className="btn-ghost mb-3" onClick={onBack.to}>
        <ArrowLeft size={13} /> {onBack.label}
      </button>

      {/* Ptero top bar: identity + status + power cluster */}
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="truncate text-[22px] font-bold tracking-[-.02em] text-ink">{name}</h1>
            <StatusBadge status={status} />
          </div>
          {tagline && <div className="mt-0.5 text-[12px] text-muted">{tagline}</div>}
        </div>
        <div className="flex items-center gap-1.5">
          <button className="btn-brand" disabled={power.busy} onClick={power.onStart}><Play size={13} /> Start</button>
          <button className="btn-ghost" disabled={power.busy} onClick={power.onRestart}><RotateCw size={13} /> Restart</button>
          <button className="btn-ghost" disabled={power.busy} onClick={power.onStop}><Square size={13} /> Stop</button>
          {power.onKill && kill}
        </div>
      </div>

      {/* Resource gauges */}
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {stats.map((s) => (
          <div key={s.label} className="rounded-lg border border-line bg-card px-4 py-3">
            <div className="text-[10.5px] font-semibold uppercase tracking-wider text-muted">{s.label}</div>
            <div className={`mt-1 text-[19px] font-bold leading-none ${s.tone ? toneText[s.tone] : 'text-ink'}`}>{s.value}</div>
            {s.percent !== undefined && s.percent !== null && (
              <div className="mt-2 h-1 overflow-hidden rounded bg-line">
                <div className="h-full rounded bg-brand transition-all"
                  style={{ width: `${Math.min(100, Math.max(0, s.percent))}%` }} />
              </div>
            )}
            {s.hint && <div className="mt-1.5 text-[10.5px] text-muted">{s.hint}</div>}
          </div>
        ))}
      </div>

      {/* Left tab rail + content (Pterodactyl layout) */}
      <div className="flex flex-col gap-4 md:flex-row">
        <nav className="flex shrink-0 gap-1 overflow-x-auto md:w-48 md:flex-col">
          {tabs.map((t) => (
            <button key={t.id} onClick={() => onTab(t.id)}
              className={`flex items-center gap-2.5 whitespace-nowrap rounded-md px-3 py-2 text-left text-[12.5px] font-medium transition-colors ${
                active === t.id ? 'bg-brand/10 text-brand' : 'text-muted hover:bg-line/40 hover:text-ink'
              }`}>
              {t.icon} {t.label}
            </button>
          ))}
        </nav>
        <div className="min-w-0 flex-1">{children}</div>
      </div>
    </div>
  )
}

export interface ConsoleLine { seq: number; ts: string; text: string }

// PteroConsole — dark terminal box, autoscroll, optional allowlisted command
// input (Minecraft console; bot console is read-only by design).
export function PteroConsole({ lines, onSend, placeholder, sendLabel = 'Send', tone = 'green', busy }: {
  lines: ConsoleLine[]
  onSend?: (cmd: string) => void
  placeholder?: string
  sendLabel?: string
  tone?: 'green' | 'sky'
  busy?: boolean
}) {
  const boxRef = useRef<HTMLDivElement>(null)
  const [cmd, setCmd] = useState('')
  const textClass = tone === 'sky' ? 'text-sky-200' : 'text-green-200'

  useEffect(() => {
    boxRef.current?.scrollTo({ top: boxRef.current.scrollHeight })
  }, [lines])

  return (
    <div>
      <div ref={boxRef} className={`max-h-[460px] min-h-[320px] overflow-y-auto rounded-lg border border-line bg-ink/95 p-3 font-mono text-[11.5px] leading-5 ${textClass}`}>
        {lines.length === 0 && <div className="text-muted">no output yet</div>}
        {lines.map((l) => (
          <div key={l.seq} className="whitespace-pre-wrap break-all">{l.text}</div>
        ))}
      </div>
      {onSend && (
        <div className="mt-3 flex gap-2">
          <input className="input flex-1 font-mono" value={cmd} onChange={(e) => setCmd(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && cmd.trim() && !busy) { onSend(cmd.trim()); setCmd('') } }}
            placeholder={placeholder} />
          <button className="btn-brand" disabled={!cmd.trim() || busy}
            onClick={() => { if (cmd.trim()) { onSend(cmd.trim()); setCmd('') } }}>{sendLabel}</button>
        </div>
      )}
    </div>
  )
}
