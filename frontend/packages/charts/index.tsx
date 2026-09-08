import React from 'react'

/* ---------------------------------------------------------------
   Area chart — reference ApexCharts look: smooth gradient areas.
   Phase 5: live cards NEVER read the historical DB — this component
   is fed by the /metrics/history endpoints (historical store only).
---------------------------------------------------------------- */
export interface ChartSeries { name: string; data: number[] }

export const CHART_COLORS = ['#2563eb', '#0f9d6e', '#7c4dff', '#d88b00']

export function AreaChart({ categories, series, height = 260, format = (v: number) => `${Math.round(v)}%`, yMax }: {
  categories: string[]
  series: ChartSeries[]
  height?: number
  format?: (v: number) => string
  yMax?: number
}) {
  const W = 800
  const H = height
  const PAD = { l: 38, r: 8, t: 12, b: 22 }
  const all = series.flatMap((s) => s.data)
  const max = yMax ?? Math.max(10, ...all) * 1.15
  const n = categories.length
  const x = (i: number) => PAD.l + (i / Math.max(1, n - 1)) * (W - PAD.l - PAD.r)
  const y = (v: number) => H - PAD.b - (v / max) * (H - PAD.t - PAD.b)

  const [hover, setHover] = React.useState<number | null>(null)

  const smooth = (data: number[]) => {
    if (data.length < 2) return ''
    let d = `M ${x(0)} ${y(data[0])}`
    for (let i = 0; i < data.length - 1; i++) {
      const x0 = x(i), x1 = x(i + 1)
      const cx = (x0 + x1) / 2
      d += ` C ${cx} ${y(data[i])}, ${cx} ${y(data[i + 1])}, ${x1} ${y(data[i + 1])}`
    }
    return d
  }

  const ticks = 5

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="w-full"
        style={{ height }}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
          const px = ((e.clientX - rect.left) / rect.width) * W
          const i = Math.round(((px - PAD.l) / (W - PAD.l - PAD.r)) * (n - 1))
          setHover(Math.max(0, Math.min(n - 1, i)))
        }}
      >
        <defs>
          {series.map((_, si) => (
            <linearGradient key={si} id={`ag-${si}-${height}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={CHART_COLORS[si % CHART_COLORS.length]} stopOpacity="0.26" />
              <stop offset="100%" stopColor={CHART_COLORS[si % CHART_COLORS.length]} stopOpacity="0.04" />
            </linearGradient>
          ))}
        </defs>
        {Array.from({ length: ticks + 1 }).map((_, ti) => {
          const yy = PAD.t + (ti / ticks) * (H - PAD.t - PAD.b)
          const val = max - (ti / ticks) * max
          return (
            <g key={ti}>
              <line x1={PAD.l} x2={W - PAD.r} y1={yy} y2={yy} stroke="#e7ebf2" strokeDasharray="4 4" strokeWidth="1" />
              <text x={PAD.l - 6} y={yy + 3} textAnchor="end" fontSize="9" fill="#8590a2">{format(val)}</text>
            </g>
          )
        })}
        {hover != null && <line x1={x(hover)} x2={x(hover)} y1={PAD.t} y2={H - PAD.b} stroke="#c7d5ff" strokeWidth="1" />}
        {series.map((s, si) => (
          <g key={s.name}>
            <path d={`${smooth(s.data)} L ${x(n - 1)} ${H - PAD.b} L ${x(0)} ${H - PAD.b} Z`} fill={`url(#ag-${si}-${height})`} />
            <path d={smooth(s.data)} fill="none" stroke={CHART_COLORS[si % CHART_COLORS.length]} strokeWidth="2" strokeLinecap="round" />
            {hover != null && s.data[hover] != null && (
              <circle cx={x(hover)} cy={y(s.data[hover])} r="3" fill="#fff" stroke={CHART_COLORS[si % CHART_COLORS.length]} strokeWidth="2" />
            )}
          </g>
        ))}
        {categories.map((c, i) => (
          (n <= 8 || i % Math.ceil(n / 8) === 0) && (
            <text key={i} x={x(i)} y={H - 6} textAnchor="middle" fontSize="9" fill="#8590a2">{c}</text>
          )
        ))}
      </svg>
      {hover != null && (
        <div
          className="pointer-events-none absolute z-10 rounded-[8px] border border-line bg-white px-2.5 py-1.5 shadow-card"
          style={{ left: `${(x(hover) / W) * 100}%`, top: 0, transform: 'translateX(-50%)' }}
        >
          <div className="mb-0.5 text-[9px] font-bold text-muted">{categories[hover]}</div>
          {series.map((s, si) => (
            <div key={s.name} className="flex items-center gap-1.5 text-[9.5px] font-semibold text-ink">
              <span className="h-1.5 w-1.5 rounded-full" style={{ background: CHART_COLORS[si % CHART_COLORS.length] }} />
              {s.name}: {format(s.data[hover])}
            </div>
          ))}
        </div>
      )}
      <div className="mt-1 flex flex-wrap gap-3 pl-1">
        {series.map((s, si) => (
          <span key={s.name} className="inline-flex items-center gap-1.5 text-[9.5px] font-semibold text-muted">
            <span className="h-1.5 w-1.5 rounded-full" style={{ background: CHART_COLORS[si % CHART_COLORS.length] }} />
            {s.name}
          </span>
        ))}
      </div>
    </div>
  )
}

/* ---------------------------------------------------------------
   Range control for history charts
---------------------------------------------------------------- */
export function ChartControls({ options, value, onChange }: {
  options: { label: string; value: string }[]
  value: string
  onChange: (v: string) => void
}) {
  return (
    <div className="flex gap-1 rounded-[8px] border border-line bg-surface-2 p-[3px]">
      {options.map((o) => (
        <button
          key={o.value}
          onClick={() => onChange(o.value)}
          className={`rounded-[5px] px-2 py-1 text-[9.5px] font-bold transition ${
            value === o.value ? 'bg-white text-ink shadow-card' : 'text-muted hover:text-ink'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/* ---------------------------------------------------------------
   Sparkline — tiny live trend line for resource cards (WS-fed)
---------------------------------------------------------------- */
export function Sparkline({ data, color = '#2563eb', height = 30, width = 96 }: {
  data: number[]
  color?: string
  height?: number
  width?: number
}) {
  if (data.length < 2) return <div style={{ height }} />
  const max = Math.max(...data, 1)
  const min = Math.min(...data, 0)
  const span = Math.max(max - min, 1)
  const pts = data
    .map((v, i) => `${(i / (data.length - 1)) * width},${height - 2 - ((v - min) / span) * (height - 4)}`)
    .join(' ')
  return (
    <svg width={width} height={height} className="overflow-visible">
      <polyline points={pts} fill="none" stroke={color} strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
