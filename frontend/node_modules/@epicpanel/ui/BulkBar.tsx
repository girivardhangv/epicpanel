/* Bulk actions (verbatim master-doc component list: "bulk actions").
 *
 * `useBulkSelection` owns the id set; `BulkBar` renders the sticky action
 * bar that appears while rows are selected. Works for DataTable selections
 * AND card/grid selections — the bar never touches data, the app wires the
 * actions (RBAC-check them before listing, like the command palette).
 */
import { ReactNode, useCallback, useMemo, useState } from 'react'
import { X } from 'lucide-react'

export interface BulkSelection {
  /** Ordered list of selected ids (last-picked last). */
  selected: string[]
  count: number
  isSelected: (id: string) => boolean
  toggle: (id: string) => void
  /** Add/remove a batch (page select-all). */
  toggleMany: (ids: string[], on: boolean) => void
  /** Whether every id in `ids` is currently selected. */
  allSelected: (ids: string[]) => boolean
  clear: () => void
}

export function useBulkSelection(): BulkSelection {
  const [selected, setSelected] = useState<string[]>([])
  const toggle = useCallback((id: string) => {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]))
  }, [])
  const toggleMany = useCallback((ids: string[], on: boolean) => {
    setSelected((cur) => {
      if (on) {
        const set = new Set(cur)
        ids.forEach((id) => set.add(id))
        return Array.from(set)
      }
      const drop = new Set(ids)
      return cur.filter((x) => !drop.has(x))
    })
  }, [])
  return useMemo(
    () => ({
      selected,
      count: selected.length,
      isSelected: (id: string) => selected.includes(id),
      toggle,
      toggleMany,
      allSelected: (ids: string[]) => ids.length > 0 && ids.every((id) => selected.includes(id)),
      clear: () => setSelected([]),
    }),
    [selected, toggle, toggleMany],
  )
}

/** Round checkbox matching the design system (brand when checked). */
export function BulkCheckbox({ checked, onChange, label, indeterminate }: {
  checked: boolean
  onChange: (on: boolean) => void
  label: string
  /** Header state: some-but-not-all rows selected. */
  indeterminate?: boolean
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={indeterminate && !checked ? 'mixed' : checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!checked)
      }}
      className={`grid h-[16px] w-[16px] cursor-pointer place-items-center rounded-[5px] border transition ${
        checked || indeterminate ? 'border-brand bg-brand text-white' : 'border-[#cfd7e4] bg-white hover:border-[#9db8ff]'
      }`}
    >
      {checked ? (
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M1.5 5.2 4 7.7 8.5 2.4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      ) : indeterminate ? (
        <span aria-hidden="true" className="h-[2px] w-[8px] rounded-full bg-white" />
      ) : null}
    </button>
  )
}

/**
 * Sticky bottom action bar for the current selection. Render it when
 * `selection.count > 0`; actions live in `children` (the app decides RBAC).
 * The count is announced politely to screen readers; Escape clears via the
 * optional `onClear` (wired by default through `selection.clear`).
 */
export function BulkBar({ count, onClear, children, label = 'Selected items' }: {
  count: number
  onClear: () => void
  /** Action buttons (btn-ghost / btn-danger etc.). */
  children?: ReactNode
  /** Screen-reader label for the count region. */
  label?: string
}) {
  if (count <= 0) return null
  return (
    <div
      role="toolbar"
      aria-label="Bulk actions"
      className="dp-fade-up fixed bottom-5 left-1/2 z-[90] flex -translate-x-1/2 items-center gap-3 rounded-[12px] border border-line bg-white px-3.5 py-2.5 shadow-toast"
    >
      <span aria-live="polite" className="whitespace-nowrap text-[11px] font-bold text-ink">
        {count} {label}
        <span className="sr-only"> selected</span>
      </span>
      <span aria-hidden="true" className="h-5 w-px bg-line" />
      <div className="flex items-center gap-2">{children}</div>
      <button
        type="button"
        onClick={onClear}
        aria-label="Clear selection"
        title="Clear selection"
        className="grid h-[24px] w-[24px] cursor-pointer place-items-center rounded-[7px] text-muted transition hover:bg-surface-2 hover:text-ink"
      >
        <X size={13} />
      </button>
    </div>
  )
}
