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
