/** @type {import('tailwindcss').Config} */
import { UI_REF_TOKENS } from './packages/design-system/index.ts'

// Phase 14 single-source rule: the Tailwind theme is DERIVED from
// packages/design-system (UI_REF_TOKENS == ui-ref.html :root). The values
// below are token references, not literals, so a token change propagates
// through every app on the next build.
const c = UI_REF_TOKENS

export default {
  content: [
    './index.html',
    './src/**/*.{ts,tsx}',
    './packages/**/*.{ts,tsx}',
    './apps/**/*.{ts,tsx,html}',
  ],
  theme: {
    extend: {
      colors: {
        app: c.bg,
        surface: { DEFAULT: c.surface, 2: c['surface-2'] },
        sidebar: {
          DEFAULT: c.sidebar,
          2: c['sidebar-2'],
          hover: 'rgba(52, 106, 255, .2)',
          text: c['sidebar-text'],
          active: c['sidebar-active'],
        },
        line: { DEFAULT: c.border, soft: '#eef1f6' },
        brand: { DEFAULT: c.primary, dark: c['primary-2'], soft: c['primary-soft'] },
        accent: { DEFAULT: c.primary, dark: c['primary-2'] },
        ok: { DEFAULT: c.success, soft: c['success-soft'] },
        warn: { DEFAULT: c.warning, soft: c['warning-soft'] },
        danger: { DEFAULT: c.danger, soft: c['danger-soft'] },
        purple: { DEFAULT: c.purple, soft: c['purple-soft'] },
        ink: c.text,
        sub: '#596579',
        muted: c.muted,
        navy: '#132039',
      },
      boxShadow: {
        card: c['shadow-sm'],
        pop: c.shadow,
        toast: '0 20px 40px rgba(13, 24, 43, 0.22)',
        modal: '0 30px 80px rgba(15, 23, 42, 0.25)',
      },
      borderRadius: {
        DEFAULT: '9px',
        card: c.radius,
        sm: c['radius-sm'],
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', '-apple-system', 'BlinkMacSystemFont', 'Segoe UI', 'sans-serif'],
      },
      transitionDuration: {
        DEFAULT: '180ms',
      },
    },
  },
  plugins: [],
}
