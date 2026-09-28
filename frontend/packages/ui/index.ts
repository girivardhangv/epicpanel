// Shared UI primitives (Phase 5): ONE design system for the customer cPanel
// (apps/customer) and the admin WHM (apps/admin, Phase 6). Nothing here may
// import app code — components are pure presentation + props.
// Phase 14: side-effect import of the design-system token stylesheet so every
// app that pulls in shared components automatically gets the ui-ref :root
// custom properties, the dp-* motion/skeleton classes, the focus-visible ring
// and the reduced-motion guards. Relative on purpose — the vite aliases map
// the bare specifier to index.ts, which cannot express the css subpath.
import '../design-system/tokens.css'
export * from './cards'
export * from './kit'
export * from './AppSidebar'
export * from './FreshnessBadge'
export * from './Skeleton'
export * from './Timeline'
export * from './BulkBar'
export * from './ErrorBoundary'
export { WebServerConfigCard } from './WebServerConfigCard'
export type { SiteConfigDoc } from './WebServerConfigCard'

/* Phase 14 additions — command palette (⌘K), results-provider driven and
 * RBAC-filtered by the app. Never removed: contract is additive-only. */
export { CommandPalette, useCommandPaletteHotkey } from './CommandPalette'
export type { CommandResult, CommandResultsProvider } from './CommandPalette'

export { Modal, ConfirmDialog } from '@epicpanel/forms'

/** Legacy alias kept so the root monolith keeps compiling unchanged. */
export { StatCard as MetricCard } from './cards'
