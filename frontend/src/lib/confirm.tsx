/* Promise-based confirmation dialog for the legacy root app.
 *
 * Replaces native window.confirm() with the shared ConfirmDialog
 * (@epicpanel/forms → a11y-hardened Modal: role=dialog, focus trap,
 * restore focus). Usage:
 *   if (!(await confirmAction({ title: 'Delete site?', message: '...' }))) return
 * One imperative host is mounted per call and unmounted after the answer,
 * so call sites stay one-liners (18 call sites, zero per-page state).
 */
import { createRoot, Root } from 'react-dom/client'
import { ConfirmDialog } from '@epicpanel/ui'

export interface ConfirmOptions {
  title: string
  message: string
  confirmLabel?: string
  danger?: boolean
}

export function confirmAction(opts: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root: Root = createRoot(host)
    const cleanup = (answer: boolean) => {
      root.unmount()
      host.remove()
      resolve(answer)
    }
    root.render(
      <ConfirmDialog
        open
        title={opts.title}
        message={opts.message}
        confirmLabel={opts.confirmLabel ?? 'Confirm'}
        danger={opts.danger ?? true}
        onClose={() => cleanup(false)}
        onConfirm={() => cleanup(true)}
      />,
    )
  })
}
