# PHASE 14 — UI/UX Polish

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `prompts/epicpanel-docs` lines 1144–1174 (Phase 14 — component list + principles are the contract)
> 3. `prompts/ui-ref.html` — full prototype (admin + customer screens, `:root` design tokens)
> 4. `phases/README.md` + Phase 5/6 handoffs (what's already shared)
> 5. Code: `packages/*` (or wherever shared UI landed), `frontend/src`
>
> Depends on: Phases 3–13 stable — verbatim: **"Only after the underlying architecture is stable should you polish the UI heavily."** If architecture is NOT stable, stop and report back instead of polishing.

## Mission
Unify + polish both experiences on ONE design system to ui-ref parity.

## Principles — VERBATIM from master doc
- Customer UI: **"I shouldn't need to know Linux."**
- Admin UI: **"I should be able to understand the entire infrastructure in seconds."**

## Component list — VERBATIM from master doc ("Use:")
```
responsive sidebar | command/search palette | global notifications | breadcrumbs
resource cards | live charts | tables with filtering | bulk actions
confirmation dialogs | activity timeline | status badges | contextual actions
```
Avoid (verbatim): "Avoid unnecessary animations. Avoid emoji. Use a proper icon library."

## Work Items
- [ ] Design tokens: extract `ui-ref.html` `:root` (bg `#f5f7fb`, sidebar `#0c1526`, primary `#2563eb`, success/warning/danger/purple + soft variants, radius 14/10px, header 70px, sidebar 248px, Inter) into `packages/design-system` as CSS variables + Tailwind theme — single source
- [ ] Component audit: every item in the verbatim list exists in shared packages and is used by BOTH apps; replace any local one-off copies
- [ ] Command/search palette (⌘K): navigate + act (find account, node, run action) — RBAC-filtered results
- [ ] Live charts: historical series for graphs; live cards from WS — never mix sources (Phase 3 rule)
- [ ] Freshness UI consistency sweep: LIVE/STALE/OFFLINE badge everywhere a live value renders; age formatting (`240ms ago` / `18.4s ago`)
- [ ] Tables: filtering + bulk actions + contextual actions across all list screens
- [ ] Empty/loading/error states per screen; skeleton loaders (no spinners-as-content)
- [ ] Accessibility: keyboard nav through sidebar/palette/dialogs/tables, focus rings, ARIA on menus+dialogs, contrast check on tokens, reduced-motion respected
- [ ] Responsive: sidebar collapse, table→card layouts at breakpoints
- [ ] Emoji sweep + animation audit (only functional transitions, ~180ms per ui-ref)

## Deliverables
Token package, component parity, palette, a11y pass, both apps at ui-ref visual parity.

## Definition of Done
Side-by-side with `ui-ref.html` screens: parity; keyboard-only walkthrough passes; zero emoji; both apps import identical components.

## Session Handoff — FILL BEFORE ENDING SESSION
- Parity gaps knowingly left: (fill)
- A11y issues fixed vs deferred: (fill)
- Update `phases/README.md` status row for Phase 14 → DONE
