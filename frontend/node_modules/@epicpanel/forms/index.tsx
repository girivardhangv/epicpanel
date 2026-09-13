// Form primitives (Phase 5): Field/Select/Validation shared by customer +
// admin apps. The modal primitives live here too (forms drive them).
// Phase 14 a11y pass: dialogs are role="dialog" + aria-modal, trap Tab
// focus while open, move focus in on open and restore it on close.
import { useEffect, useId, ReactNode, useRef } from 'react'
import { X } from 'lucide-react'

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

export function Modal({ open, onClose, title, subtitle, children, width = 'max-w-[500px]', footer }: {
  open: boolean
  onClose: () => void
  title: string
  subtitle?: string
  children: ReactNode
  width?: string
  footer?: ReactNode
}) {
  const titleId = useId()
  const panelRef = useRef<HTMLDivElement>(null)
  const restoreRef = useRef<HTMLElement | null>(null)

  useEffect(() => {
    if (!open) return
    restoreRef.current = document.activeElement as HTMLElement | null
    // Initial focus: first focusable inside the panel, else the panel itself.
    const t = window.setTimeout(() => {
      const first = panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)
      ;(first ?? panelRef.current)?.focus()
    }, 0)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      window.clearTimeout(t)
      document.body.style.overflow = prevOverflow
      restoreRef.current?.focus?.()
    }
  }, [open])

  // Escape closes; Tab is trapped inside the dialog while open.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
        return
      }
      if (e.key === 'Tab' && panelRef.current) {
        const nodes = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
          (el) => el.offsetParent !== null || el === document.activeElement,
        )
        if (nodes.length === 0) return
        const first = nodes[0]
        const last = nodes[nodes.length - 1]
        const active = document.activeElement as HTMLElement | null
        if (e.shiftKey) {
          if (active === first || !panelRef.current.contains(active)) {
            e.preventDefault()
            last.focus()
          }
        } else if (active === last || !panelRef.current.contains(active)) {
          e.preventDefault()
          first.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [open, onClose])

  if (!open) return null
  return (
    <div className="fixed inset-0 z-[100] grid place-items-center p-5">
      <div className="absolute inset-0 bg-[#0b1220]/40 backdrop-blur-[4px]" onClick={onClose} aria-hidden="true" />
      <div className={`fade-up relative z-10 w-full ${width}`}>
        <div
          ref={panelRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby={titleId}
          tabIndex={-1}
          className="dp-focusable overflow-hidden rounded-[15px] border border-line bg-white shadow-modal focus:outline-none"
        >
          <div className="flex items-center justify-between gap-2.5 border-b border-line px-4 py-[15px]">
            <div>
              <strong id={titleId} className="text-[13px] text-ink">{title}</strong>
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

export function FormRow({ children, cols = 2 }: { children: ReactNode; cols?: 1 | 2 | 3 }) {
  return (
    <div
      className={
        cols === 1 ? 'grid grid-cols-1 gap-x-3' : cols === 3 ? 'grid grid-cols-1 gap-x-3 sm:grid-cols-3' : 'grid grid-cols-1 gap-x-3 sm:grid-cols-2'
      }
    >
      {children}
    </div>
  )
}

export function ErrorNote({ message }: { message?: string }) {
  if (!message) return null
  return (
    <div role="alert" className="mb-3.5 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">
      {message}
    </div>
  )
}

export function InfoNote({ message, tone = 'ok' }: { message?: string; tone?: 'ok' | 'warn' }) {
  if (!message) return null
  const cls =
    tone === 'warn'
      ? 'border-[#ffe8b1] bg-warn-soft text-warn'
      : 'border-[#cef0e1] bg-ok-soft text-ok'
  return <div role="status" className={`mb-3.5 rounded-[9px] border px-3 py-2 text-[11px] font-semibold ${cls}`}>{message}</div>
}

export function Select({ value, onChange, options, placeholder, disabled }: {
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
  placeholder?: string
  disabled?: boolean
}) {
  return (
    <select className="input cursor-pointer" value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled}>
      {placeholder && <option value="">{placeholder}</option>}
      {options.map((o) => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
    </select>
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

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="mb-3 block">
      <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[10px] text-muted">{hint}</span>}
    </label>
  )
}
