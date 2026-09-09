# EpicPanel Design Tokens

Single source of truth: `frontend/packages/design-system/index.ts`
(`UI_REF_TOKENS` — extracted 1:1 from `prompts/ui-ref.html` `:root`).
`frontend/tailwind.config.js` imports that object and derives its theme from
it; `frontend/packages/design-system/tokens.css` renders the `:root`
custom-property block (plus the shared `dp-*` motion/skeleton/focus classes)
and is side-effect-imported by `frontend/packages/ui/index.ts`, so every app
that pulls in shared components gets the tokens automatically. Never re-type a
hex value in app code — reference the token.

## Tokens (ui-ref `:root`)

| CSS custom property | Value | Tailwind mapping | Usage |
| --- | --- | --- | --- |
| `--bg` | `#f5f7fb` | `bg-app` | Page background (both apps) |
| `--surface` | `#ffffff` | `bg-white` / `surface.DEFAULT` | Cards, modals, header |
| `--surface-2` | `#f8fafc` | `surface.2` (`bg-surface-2`) | Toolbars, soft fills, kbd chips |
| `--sidebar` | `#0c1526` | `sidebar.DEFAULT` | Sidebar gradient base (`#0c1526 → #09111e` + radial brand glow) |
| `--sidebar-2` | `#111e32` | `sidebar.2` | Sidebar secondary surface |
| `--sidebar-text` | `#aebbd0` | `sidebar.text` (`text-sidebar-text`) | Sidebar nav text (inactive) |
| `--sidebar-active` | `#ffffff` | `sidebar.active` | Sidebar active nav text |
| `--text` | `#172033` | `ink` | Body text |
| `--muted` | `#667085` | `muted` | Secondary text, hints |
| `--border` | `#e7ebf2` | `line.DEFAULT` (`border-line`) | Card/table/button borders |
| `--primary` | `#2563eb` | `brand.DEFAULT` | Primary buttons, links, active states, focus rings |
| `--primary-2` | `#1d4ed8` | `brand.dark` | Primary button hover |
| `--primary-soft` | `#eef4ff` | `brand.soft` | Selected rows, icon chips, palette highlight |
| `--success` | `#0f9d6e` | `ok.DEFAULT` | LIVE/ok badges, success toast icon |
| `--success-soft` | `#eaf9f3` | `ok.soft` | Success badge background |
| `--warning` | `#d88b00` | `warn.DEFAULT` | STALE/warn badges |
| `--warning-soft` | `#fff7e7` | `warn.soft` | Warning badge background |
| `--danger` | `#dc3d4b` | `danger.DEFAULT` | OFFLINE/destructive badges + buttons |
| `--danger-soft` | `#fff0f2` | `danger.soft` | Danger badge/button background |
| `--purple` | `#7c4dff` | `purple.DEFAULT` | Chart series 3, accent chips |
| `--purple-soft` | `#f2edff` | `purple.soft` | Purple chip background |
| `--shadow` | `0 8px 30px rgba(24,39,75,.06)` | `shadow-pop` | Popovers, hover lift |
| `--shadow-sm` | `0 2px 10px rgba(24,39,75,.05)` | `shadow-card` | Cards |
| `--radius` | `14px` | `rounded-card` | Cards, modals, palette |
| `--radius-sm` | `10px` | `rounded-sm` | Inner panels, chips |
| `--header-h` | `70px` | — (layout) | Sticky header height |
| `--sidebar-w` | `248px` | — (layout) | Sidebar width (collapsed: 68px) |
| `--transition` | `180ms ease` | `transitionDuration.DEFAULT` | The only transition duration |

### App-level aliases (legacy names, kept in sync)

| Token | Value | Tailwind | Notes |
| --- | --- | --- | --- |
| `sub` | `#596579` | `sub` | Stronger secondary text (between ink and muted) |
| `navy` | `#132039` | `navy` | Overlay scrims |
| `line.soft` | `#eef1f6` | `border-line-soft` | Hairline dividers, skeleton base |
| radius default | `9px` | `rounded` | Buttons, inputs, table rows |
| `shadow-toast` | `0 20px 40px rgba(13,24,43,.22)` | `shadow-toast` | Toasts, bulk bar |
| `shadow-modal` | `0 30px 80px rgba(15,23,42,.25)` | `shadow-modal` | Modals, command palette |
| Font | Inter, ui-sans-serif, system-ui, … | `font-sans` | Loaded via index.html |

Derived constants in `design-system/index.ts`: `TONE` (chip fg/bg pairs),
`CHART_COLORS` (`#2563eb`, `#0f9d6e`, `#7c4dff`, `#d88b00` — Phase 3 series
order CPU/memory/disk/network), `LIVE_MAX_MS` 15s / `STALE_MAX_MS` 120s
(freshness thresholds), `MOTION_MS` 180.

## WCAG 2.1 contrast audit

Ratios computed with `contrastRatio()` from `packages/design-system/index.ts`
(relative luminance per WCAG 2.1). Levels: AA normal text ≥ 4.5, AA large text
(≥ 18.66px bold / 24px) and non-text UI ≥ 3.0, AAA normal ≥ 7.0.

| Foreground / Background | Ratio | AA (4.5) | AAA (7) | Verdict |
| --- | --- | --- | --- | --- |
| `#172033` text on `#f5f7fb` bg (body text on page) | **15.17** | pass | pass | AAA |
| `#172033` text on `#ffffff` surface | **16.27** | pass | pass | AAA |
| `#172033` text on `#f8fafc` surface-2 | **15.55** | pass | pass | AAA |
| `#667085` muted on `#ffffff` surface | **4.97** | pass | fail | AA |
| `#667085` muted on `#f8fafc` surface-2 | **4.75** | pass | fail | AA |
| `#ffffff` on `#2563eb` primary (buttons) | **5.17** | pass | fail | AA |
| `#2563eb` primary links on `#ffffff` | **5.17** | pass | fail | AA |
| `#aebbd0` sidebar-text on `#0c1526` sidebar | **9.40** | pass | pass | AAA |
| `#ffffff` sidebar-active on `#0c1526` sidebar | **18.25** | pass | pass | AAA |
| `#0f9d6e` success on `#eaf9f3` success-soft | **3.19** | fail (< 4.5) | fail | **AA large/bold + UI only** |
| `#d88b00` warning on `#fff7e7` warning-soft | **2.59** | fail | fail | **FAIL — known gap** |
| `#dc3d4b` danger on `#fff0f2` danger-soft | **3.95** | fail (< 4.5) | fail | **AA large/bold + UI only** |
| `#7c4dff` purple on `#f2edff` purple-soft | **4.20** | fail (< 4.5) | fail | **AA large/bold + UI only** |

### Rules that keep the failing pairs honest

- Soft-variant text (`success`/`warning`/`danger`/`purple` on their `-soft`
  backgrounds) renders **only** as status chips/badges: 10px **bold**,
  paired with a colored status dot and a semantic border
  (`.badge-ok/.badge-warn/.badge-off`, `.status-live/...` in each stylesheet).
  The color is never the sole signal — the label text ("LIVE", "STALE",
  "OFFLINE") carries the meaning, satisfying WCAG 1.4.1 (use of color).
- These pairs meet the 3:1 non-text/large-text threshold except warning
  (2.59). **Known gap, documented deliberately:** `--warning` on
  `--warning-soft` comes verbatim from `ui-ref.html` (the parity contract);
  it is used exclusively in small decorative badges whose state is also
  conveyed by dot + text. If a strict pass is required, darken the
  foreground to ~`#9a6700` (≈ 4.6:1 on the soft bg) — a one-line change in
  `UI_REF_TOKENS` that propagates everywhere via the Tailwind theme.
- `#667085` muted and `#2563eb` primary meet AA but not AAA; muted is never
  used below 10px, primary text is always ≥ 11px semibold links/buttons.

## Motion & interaction budget

- One duration: **180ms ease** (`--transition`, Tailwind `duration-default`).
  Functional only: hover/focus color, dropdown/modal/palette enter
  (`.dp-fade-up`), sidebar slide. No decorative animation (ui-ref rule).
- `prefers-reduced-motion: reduce`: `.dp-fade-up` and `.dp-skeleton` stop
  animating; a global guard collapses every transition/animation to 0.01ms
  (`tokens.css`, plus the same guard in each app stylesheet).
- Focus: global `:focus-visible` ring `2px solid var(--primary)` +
  2px offset for links/buttons/menu items/options (zero-specificity
  `:where()` so component styles win), `.dp-focusable` for composite
  components. Visible for keyboard users, hidden for pointer/touch.

## Keyboard map (both apps)

| Surface | Keys |
| --- | --- |
| Command palette | `Cmd/Ctrl+K` toggle, `↑/↓` move, `Home/End` ends, `Enter` run, `Esc` close, `Tab` input↔list (ARIA 1.2 combobox + listbox) |
| Sidebar | `Ctrl/Cmd+F` tool search, `Tab` through nav links, mobile drawer closes on nav |
| Dialogs | Focus moves in on open, `Tab` trapped, `Esc` closes, focus restored on close (forms/Modal) |
| Tables | `Tab` to search/filters/row actions; `Space`/`Enter` on checkbox cells |
