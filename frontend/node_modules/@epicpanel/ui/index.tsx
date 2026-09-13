// Phase 14 dedupe: this file used to carry its own Modal/ConfirmDialog
// copies — the ONE implementations live in @epicpanel/forms (imported here
// so both entry points surface identical, a11y-hardened components).
export { Modal, ConfirmDialog } from '@epicpanel/forms'
