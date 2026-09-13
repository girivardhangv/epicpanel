// Table primitives (Phase 5): filterable table shell shared by the customer
// cPanel + admin WHM list surfaces.
import { ReactNode, useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import { BulkCheckbox } from '@epicpanel/ui'

export function TableWrap({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>
}

export interface Column<T> {
  key: string
  header: string
  render: (row: T) => ReactNode
  /** Column value used by the client-side text filter. */
  filter?: (row: T) => string
}

/** Search input matching the toolbar look (ref: mini-search). */
export function TableSearch({ value, onChange, placeholder }: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <div className="relative min-w-[180px] flex-1">
      <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[#98a2b3]" />
      <input
        className="h-[34px] w-full rounded-[8px] border border-line bg-white px-2.5 pl-[31px] text-[11px] outline-none placeholder:text-[#98a2b3] focus:border-[#9db8ff]"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}

/**
 * Optional row-selection contract (Phase 14 bulk actions): pass it and a
 * checkbox column is prepended. The selection state itself lives in the
 * app (useBulkSelection from @epicpanel/ui) — this component only renders
 * the checkboxes. Absent = zero visual/behavior change (additive).
 */
export interface TableSelection<T> {
  /** Stable row id used by the checkbox. */
  key: (row: T) => string
  /** Currently selected ids. */
  selected: ReadonlySet<string>
  /** Called with the row id and the new checked state. */
  onToggle: (id: string, on: boolean) => void
  /** Select/deselect every row currently visible. */
  onToggleAll?: (rows: T[], on: boolean) => void
  /** aria-label for the row checkbox. */
  label?: (row: T) => string
}

/**
 * Generic filterable table: search box + optional select filter + empty and
 * loading states. Row header/td styling matches the ref-table classes.
 */
export function DataTable<T>({ columns, rows, rowKey, searchText, filterSlot, loading, empty, minWidth = 670, toolbarExtra, selection }: {
  columns: Column<T>[]
  rows: T[] | null
  rowKey: (row: T) => string
  searchText?: (row: T) => string
  /** Extra controls rendered next to the search box (selects, buttons). */
  filterSlot?: ReactNode
  loading?: boolean
  empty?: ReactNode
  minWidth?: number
  toolbarExtra?: ReactNode
  /** Optional bulk-selection checkbox column (Phase 14, additive). */
  selection?: TableSelection<T>
}) {
  const [query, setQuery] = useState('')

  const filtered = useMemo(() => {
    const list = rows ?? []
    const q = query.toLowerCase().trim()
    if (!q) return list
    if (searchText) return list.filter((r) => searchText(r).toLowerCase().includes(q))
    return list.filter((r) => columns.some((c) => (c.filter?.(r) ?? '').toLowerCase().includes(q)))
  }, [rows, query, searchText, columns])

  const allOn = selection ? filtered.length > 0 && filtered.every((r) => selection.selected.has(selection.key(r))) : false
  const someOn = selection ? filtered.some((r) => selection.selected.has(selection.key(r))) : false

  const toolbar = searchText || filterSlot || toolbarExtra

  return (
    <div className="overflow-hidden">
      {toolbar && (
        <div className="flex flex-wrap items-center gap-2 border-b border-line bg-[#fbfcfe] px-4 py-3">
          {searchText && <TableSearch value={query} onChange={setQuery} placeholder="Search..." />}
          {filterSlot}
          {toolbarExtra}
        </div>
      )}
      {loading ? (
        <div className="space-y-3 p-5">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="skeleton h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        (empty ?? <div className="px-4 py-8 text-center text-[12px] text-muted">Nothing here yet.</div>)
      ) : (
        <TableWrap>
          <table className="w-full border-collapse" style={{ minWidth }}>
            <thead>
              <tr>
                {selection && (
                  <th className="w-[34px] border-b border-line bg-[#fbfcfe] px-4 py-[11px]">
                    <BulkCheckbox
                      label="Select all rows"
                      checked={allOn}
                      indeterminate={!allOn && someOn}
                      onChange={(on) => selection.onToggleAll?.(filtered, on)}
                    />
                  </th>
                )}
                {columns.map((c) => (
                  <th key={c.key} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">
                    {c.header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filtered.map((row) => (
                <tr key={rowKey(row)} className="transition hover:bg-[#fbfcff]">
                  {selection && (
                    <td className="border-b border-line px-4 py-[11px]">
                      <BulkCheckbox
                        label={selection.label?.(row) ?? `Select row ${selection.key(row)}`}
                        checked={selection.selected.has(selection.key(row))}
                        onChange={(on) => selection.onToggle(selection.key(row), on)}
                      />
                    </td>
                  )}
                  {columns.map((c) => (
                    <td key={c.key} className="border-b border-line px-4 py-[11px] align-middle text-[10.5px] text-ink">
                      {c.render(row)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </div>
  )
}
