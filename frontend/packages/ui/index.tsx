import { useEffect, ReactNode } from 'react'
import { X } from 'lucide-react'

export function Modal({ open, onClose, title, subtitle, children, width = 'max-w-[500px]', footer }: {
  open: boolean
  onClose: () => void
  title: string
  subtitle?: string
  children: ReactNode
  width?: string
  footer?: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    if (open) document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  return (
    <div className="fixed inset-0 z-[100] grid place-items-center p-5">
      <div className="absolute inset-0 bg-[#0b1220]/40 backdrop-blur-[4px]" onClick={onClose} />
      <div className={`fade-up relative z-10 w-full ${width}`}>
        <div className="overflow-hidden rounded-[15px] border border-line bg-white shadow-modal">
          <div className="flex items-center justify-between gap-2.5 border-b border-line px-4 py-[15px]">
            <div>
              <strong className="text-[13px] text-ink">{title}</strong>
              {subtitle && <p className="mt-0.5 text-[10px] text-muted">{subtitle}</p>}
            </div>
            <button onClick={onClose} className="icon-btn" aria-label="Close">
              <X size={15} />
            </button>
          </div>
          <div className="px-4 py-4">{children}</div>
          {footer && (
            <div className="flex justify-end gap-2 border-t border-line px-4 py-3">{footer}</div>
          )}
        </div>
      </div>
    </div>
  )
}

/** Destructive/confirm dialog: the UX-kit confirmation surface both apps use. */
export function ConfirmDialog({ open, onClose, onConfirm, title, message, confirmLabel = 'Confirm', busy, danger = true }: {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  message: string
  confirmLabel?: string
  busy?: boolean
  danger?: boolean
}) {
  return (
    <Modal open={open} onClose={onClose} title={title} width="max-w-[420px]"
      footer={
        <>
          <button className="btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
          <button className={danger ? 'btn-danger' : 'btn-brand'} onClick={onConfirm} disabled={busy}>
            {busy ? 'Working...' : confirmLabel}
          </button>
        </>
      }
    >
      <p className="text-[12px] leading-relaxed text-sub">{message}</p>
    </Modal>
  )
}
