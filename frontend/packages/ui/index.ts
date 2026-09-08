// Shared UI primitives (Phase 5): ONE design system for the customer cPanel
// (apps/customer) and the admin WHM (apps/admin, Phase 6). Nothing here may
// import app code — components are pure presentation + props.
export * from './cards'
export * from './kit'
export * from './AppSidebar'
export * from './FreshnessBadge'
export { Modal, ConfirmDialog } from '@epicpanel/forms'

/** Legacy alias kept so the root monolith keeps compiling unchanged. */
export { StatCard as MetricCard } from './cards'
