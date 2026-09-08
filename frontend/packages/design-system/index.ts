// Design tokens (global contract #7): single source of truth for BOTH apps
// (customer cPanel + admin WHM). Values mirror tailwind.config.js + index.css
// primitives — keep them in sync; Phases 6/14 generate the Tailwind theme
// from this file so drift becomes impossible.
export const tokens = {
  color: {
    app: '#f5f7fb',
    surface: '#ffffff',
    surface2: '#f8fafc',
    sidebar: '#0c1526',
    sidebar2: '#111e32',
    sidebarText: '#aebbd0',
    border: '#e7ebf2',
    borderSoft: '#eef1f6',
    brand: '#2563eb',
    brandDark: '#1d4ed8',
    brandSoft: '#eef4ff',
    ok: '#0f9d6e',
    okSoft: '#eaf9f3',
    warn: '#d88b00',
    warnSoft: '#fff7e7',
    danger: '#dc3d4b',
    dangerSoft: '#fff0f2',
    purple: '#7c4dff',
    purpleSoft: '#f2edff',
    ink: '#172033',
    sub: '#596579',
    muted: '#667085',
    navy: '#132039',
  },
  font: 'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  radius: { sm: '9px', card: '14px' },
  shadow: {
    card: '0 2px 10px rgba(24, 39, 75, 0.05)',
    pop: '0 8px 30px rgba(24, 39, 75, 0.06)',
  },
} as const

/** Tone palette shared by cards, quick actions and icons. */
export const TONE: Record<'blue' | 'green' | 'amber' | 'purple' | 'red', { bg: string; fg: string }> = {
  blue: { bg: '#eef4ff', fg: '#2563eb' },
  green: { bg: '#eaf9f3', fg: '#0f9d6e' },
  amber: { bg: '#fff7e7', fg: '#d88b00' },
  purple: { bg: '#f2edff', fg: '#7c4dff' },
  red: { bg: '#fff0f2', fg: '#dc3d4b' },
}

/** Chart series colors (Phase 3 order: CPU, memory, disk, network). */
export const CHART_COLORS = ['#2563eb', '#0f9d6e', '#7c4dff', '#d88b00'] as const

// Freshness thresholds (Phase 3 contract, mirrors agentproto.LiveMaxAge /
// StaleMaxAge): LIVE <= 15s, STALE <= 120s, OFFLINE beyond that.
export const LIVE_MAX_MS = 15_000
export const STALE_MAX_MS = 120_000

/* ================================================================
   Phase 14 — verbatim ui-ref.html :root tokens (the canonical set).
   Extracted 1:1 from prompts/ui-ref.html lines 17-46. The generated
   CSS (tokensCss) is THE single source: both app stylesheets and the
   Tailwind theme reference these custom properties — never re-type a
   hex value in app code.
   ================================================================ */

/** ui-ref.html `:root`, key-for-key. Names match the reference exactly. */
export const UI_REF_TOKENS = {
  bg: '#f5f7fb',
  surface: '#ffffff',
  'surface-2': '#f8fafc',
  sidebar: '#0c1526',
  'sidebar-2': '#111e32',
  'sidebar-text': '#aebbd0',
  'sidebar-active': '#ffffff',
  text: '#172033',
  muted: '#667085',
  border: '#e7ebf2',
  primary: '#2563eb',
  'primary-2': '#1d4ed8',
  'primary-soft': '#eef4ff',
  success: '#0f9d6e',
  'success-soft': '#eaf9f3',
  warning: '#d88b00',
  'warning-soft': '#fff7e7',
  danger: '#dc3d4b',
  'danger-soft': '#fff0f2',
  purple: '#7c4dff',
  'purple-soft': '#f2edff',
  shadow: '0 8px 30px rgba(24, 39, 75, 0.06)',
  'shadow-sm': '0 2px 10px rgba(24, 39, 75, 0.05)',
  radius: '14px',
  'radius-sm': '10px',
  'header-h': '70px',
  'sidebar-w': '248px',
  transition: '180ms ease',
} as const

export type UiRefTokenName = keyof typeof UI_REF_TOKENS

/**
 * The `:root` custom-property block every app stylesheet imports. One string,
 * one place — apps embed it verbatim (see docs/design-tokens.md).
 */
export function tokensCss(indent = ''): string {
  const body = (Object.entries(UI_REF_TOKENS) as [UiRefTokenName, string][])
    .map(([k, v]) => `${indent}  --${k}: ${v};`)
    .join('\n')
  return `${indent}:root {\n${body}\n${indent}}`
}

/** Relative luminance (WCAG 2.1) of a #rrggbb color. */
export function relativeLuminance(hex: string): number {
  const h = hex.replace('#', '')
  const chan = [0, 2, 4].map((i) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  })
  return 0.2126 * chan[0] + 0.7152 * chan[1] + 0.0722 * chan[2]
}

/** WCAG 2.1 contrast ratio between two #rrggbb colors (1..21). */
export function contrastRatio(fg: string, bg: string): number {
  const a = relativeLuminance(fg)
  const b = relativeLuminance(bg)
  const hi = Math.max(a, b)
  const lo = Math.min(a, b)
  return Math.round(((hi + 0.05) / (lo + 0.05)) * 100) / 100
}

export interface ContrastPair {
  /** Human label: "what renders where". */
  use: string
  fg: string
  bg: string
  /** WCAG level the pair must meet ("AA text" = 4.5, "AA large/UI" = 3). */
  target: number
  ratio: number
  pass: boolean
}

function pair(use: string, fg: string, bg: string, target: number): ContrastPair {
  const ratio = contrastRatio(fg, bg)
  return { use, fg, bg, target, ratio, pass: ratio >= target }
}

/**
 * Audited foreground/background pairs (docs/design-tokens.md documents these
 * numbers). Tokens below 4.5 render ONLY as large/bold text or non-text UI
 * (chips, badges, dots) which the 3:1 UI requirement governs.
 */
export const CONTRAST_AUDIT: ContrastPair[] = [
  pair('Body text on page background', UI_REF_TOKENS.text, UI_REF_TOKENS.bg, 4.5),
  pair('Body text on surface', UI_REF_TOKENS.text, UI_REF_TOKENS.surface, 4.5),
  pair('Body text on surface-2', UI_REF_TOKENS.text, UI_REF_TOKENS['surface-2'], 4.5),
  pair('Secondary text (muted) on surface', UI_REF_TOKENS.muted, UI_REF_TOKENS.surface, 4.5),
  pair('Secondary text (muted) on surface-2', UI_REF_TOKENS.muted, UI_REF_TOKENS['surface-2'], 4.5),
  pair('Primary buttons: white on primary', '#ffffff', UI_REF_TOKENS.primary, 4.5),
  pair('Links / primary text on surface', UI_REF_TOKENS.primary, UI_REF_TOKENS.surface, 4.5),
  pair('Sidebar nav text on sidebar', UI_REF_TOKENS['sidebar-text'], UI_REF_TOKENS.sidebar, 4.5),
  pair('Sidebar active text on sidebar', UI_REF_TOKENS['sidebar-active'], UI_REF_TOKENS.sidebar, 4.5),
  pair('Success badge text on success-soft (large/bold chips)', UI_REF_TOKENS.success, UI_REF_TOKENS['success-soft'], 3),
  pair('Warning badge text on warning-soft (large/bold chips)', UI_REF_TOKENS.warning, UI_REF_TOKENS['warning-soft'], 3),
  pair('Danger badge text on danger-soft (large/bold chips)', UI_REF_TOKENS.danger, UI_REF_TOKENS['danger-soft'], 3),
  pair('Purple accent text on purple-soft (large/bold chips)', UI_REF_TOKENS.purple, UI_REF_TOKENS['purple-soft'], 3),
]

/** Motion budget: the ONLY functional transition duration allowed. */
export const MOTION_MS = 180
