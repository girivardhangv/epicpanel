# Phase 14 Handoff — UI/UX Polish (P14-UI)

## Session Handoff (from phases/phase-14-ui-ux.md)

### Component list audit (verbatim contract) — ALL PRESENT
| Component | Where | Used by |
| --- | --- | --- |
| responsive sidebar | `packages/ui/AppSidebar.tsx` (collapse 248↔68px, mobile drawer, Ctrl+F tool search, persisted via `eh.sidebar.collapsed`) | root app, apps/customer, apps/admin |
| command/search palette | `packages/ui/CommandPalette.tsx` (results-provider prop, RBAC-filtered by callers, ARIA combobox) | root app + **apps/customer (this session)** |
| global notifications | `packages/ui/kit.tsx` (`pushToast`/`Toaster`) | root app (Toaster mount added this session), customer, admin |
| breadcrumbs | `packages/ui/kit.tsx` (`Breadcrumbs`) | SiteDetail (root + customer) |
| resource cards | `packages/ui/cards.tsx`, `kit.tsx` (Card/StatCard/UsageCard/QuickAction/FeatureTile) | both apps |
| live charts | `packages/charts` (AreaChart/Sparkline; WS-streamed frames only, Phase 3 rule) | both apps |
| tables with filtering | `packages/tables` (DataTable/TableSearch) | both apps |
| bulk actions | `packages/ui/BulkBar.tsx` (BulkBar/useBulkSelection/BulkCheckbox, aria-mixed) | Activity (root), Sites |
| confirmation dialogs | `packages/forms` ConfirmDialog + `frontend/src/lib/confirm.tsx` promise host | both apps |
| activity timeline | `packages/ui/Timeline.tsx` | Activity (root) |
| status badges | `StatusBadge` + `FreshnessBadge` (LIVE/STALE/OFFLINE + age) | both apps |
| contextual actions | `RowActions` (kit.tsx) + DataTable row slots | both apps |

### This session's changes
1. **Token stylesheet actually reachable (was the critical leftover):** `packages/ui/index.ts` now side-effect-imports `../design-system/tokens.css`. Before this, `dp-fade-up`/`dp-skeleton`/`dp-focusable` were dead classes in every app (palette motion, skeleton shimmer, forms Modal focus ring all inert) and the `:root` vars never loaded. Relative import is deliberate: the vite aliases map the bare specifier to `index.ts`, which cannot express the css subpath. Root + customer bundles verified to contain the classes. **apps/admin gets the tokens automatically too** via `@epicpanel/ui` — no admin edit needed.
2. **Focus rings:** `tokens.css` gained a global `:where(a, button, summary, [role=…], [tabindex]):focus-visible` ring (2px `--primary`, 2px offset, zero specificity so component focus styles win). WCAG 2.4.7 for every app.
3. **Command palette mounted in apps/customer** (single careful edit to `App.tsx`): `useCommandPaletteHotkey` (Cmd/Ctrl+K) + `CommandPalette` in the Shell with a static navigate provider (Dashboard, Websites, Domains, DNS, Files, FTP, Databases, PHP, Crons, Backups, Metrics, SSL, Security, Account) + RBAC-filtered entries (Minecraft, Bots, Billing, Invoices) gated at `ROLE_RANK >= billing` / platform admin — mirroring the server-side `organizations.RoleBilling` gate on those GET routes (phase7/phase10 handlers). Server enforcement remains the law; the palette never elevates.
4. **Toaster mounted in root app Shell** — `pushToast` had no renderer in the root monolith.
5. **alert() → pushToast()**: `Databases.tsx` (×2), `Software.tsx` (×1), `Packages.tsx` (×3 incl. one success toast).
6. **Animation audit:** last two `duration-500` progress bars (Setup.tsx, Software.tsx) → default 180ms (`transitionDuration.DEFAULT` = token). Emoji final sweep: zero true emoji (remaining `→ ↑ ↓ ↵` are typographic keyboard glyphs, same as ui-ref).
7. **docs/design-tokens.md**: full token table (name/value/Tailwind mapping/usage) + WCAG 2.1 audit + motion/keyboard maps. Contrast ratios computed with `contrastRatio()` from the design-system package.

### WCAG contrast results (all computed, not estimated)
- AAA: text/bg 15.17, text/surface 16.27, text/surface-2 15.55, sidebar-text/sidebar 9.40, sidebar-active/sidebar 18.25.
- AA-only: muted/surface 4.97, muted/surface-2 4.75, white-on-primary 5.17, primary-on-white 5.17.
- Soft-variant chips (3:1 large/bold + non-text, documented rule): success 3.19 pass, danger 3.95 pass, purple 4.20 pass.
- **Known fail (documented honestly):** `--warning` on `--warning-soft` = **2.59** (fails even 3:1). Value comes verbatim from ui-ref (parity contract); used only in small status badges where the dot + label text carry the meaning (WCAG 1.4.1). Suggested remediation if strict AA ever required: `#9a6700` ≈ 4.6:1, one-line change in `UI_REF_TOKENS` propagating via Tailwind.

### A11y status
Fixed this session: global focus-visible ring; palette focus trap/restore verified in code (input focus on open, restore on close, aria-activedescendant listbox); Toaster mount; dp-* classes made live (Modal `dp-focusable` now effective).
Already in place (verified, not re-touched): forms Modal/ConfirmDialog focus trap + Escape + restore; sidebar = real NavLinks (tab-able) with Ctrl+F search; `prefers-reduced-motion` guards in all three stylesheets + tokens.css; skeleton loaders with `role="status"`/aria-live; BulkCheckbox `role="checkbox"` + `aria-checked="mixed"`.
Deferred: admin palette mount (owner's call — component is shared and ready); warning-soft contrast (see above); inputs rely on border+ring focus treatment rather than outline (visible, intentional).

### Parity gaps knowingly left
- apps/admin does not mount the palette yet (apps/admin/** off-limits this session; agent owning the monitoring page active). Snippet: copy the ~20-line Shell block from `apps/customer/App.tsx` (`canBilling` → admin rank gate instead, `useCommandPaletteHotkey`, providers, `<CommandPalette/>`).
- Timeline/BulkBar not yet adopted in apps/customer pages (other agents own those pages); components are shared and ready.

### Deviations
- `apps/customer/App.tsx` also carries the pre-existing routes.minecraft/bots/billing mount from the earlier crashed run (verified working, left intact).
- Tokens.css import is relative, not via package specifier (alias constraint above); documented in code comment.

### Verification evidence
- `cd frontend && npx tsc -b --force` → exit 0 (clean; one transient failure mid-session was another agent's in-flight `backups2/Targets.tsx`, fixed by them before final run).
- `npm run build:customer` → ✓ built in 1.57s (458.71 kB JS / 52.28 kB CSS; dp-classes + focus-visible confirmed in emitted CSS).
- `npx vite build` (root) → ✓ built in 1.69s (pre-existing >500 kB chunk warning only).
- Zero new npm packages; no backend/, apps/admin/**, apps/customer/pages/**, root package.json, or vite config changes.
