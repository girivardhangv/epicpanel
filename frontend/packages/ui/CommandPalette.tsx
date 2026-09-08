/* Command / search palette (verbatim master-doc component list).
 *
 * Contract:
 * - One instance per app shell, opened with Cmd/Ctrl+K (hotkey built in) or
 *   programmatically via `open`.
 * - The app OWNS the result set: everything flows through the `providers`
 *   prop. RBAC filtering happens there (pass only what the current user may
 *   see/do) — the palette never elevates access.
 * - Navigation + actions: results either navigate (via the app-provided
 *   `perform`) or run an action. Nothing is auto-executed.
 * - Zero emoji, Lucide icons only, 180ms functional transition, full
 *   keyboard operation (arrows / Home / End / Enter / Escape / Tab trap).
 */
import { ReactNode, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { CornerDownLeft, Search } from 'lucide-react'

export interface CommandResult {
  /** Stable id (grouped listbox uses `${groupId}:${id}`). */
  id: string
  label: string
  /** Group heading, e.g. "Tools", "Accounts", "Actions". */
  group?: string
  /** Secondary line (domain, host, role...). */
  hint?: string
  /** Leading Lucide icon node. */
  icon?: ReactNode
  /** Extra match text (searched along label + hint). */
  keywords?: string
  /** Called on Enter/click. The provider decides RBAC before listing. */
  perform: () => void
  /** Marks destructive actions (red text). */
  dangerous?: boolean
}

/**
 * A results provider: receives the current query, returns matches.
 * Synchronous providers filter static lists; async providers may fetch
 * (accounts, nodes, sites) and should debounce themselves cheaply — the
 * palette already coalesces to the latest response.
 */
export type CommandResultsProvider = (query: string) => CommandResult[] | Promise<CommandResult[]>

export function useCommandPaletteHotkey(onOpen: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        onOpen()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onOpen])
}

export function CommandPalette({ open, onClose, providers, placeholder = 'Search tools, resources and actions...', emptyHint = 'No matches. Try a different term.' }: {
  open: boolean
  onClose: () => void
  providers: CommandResultsProvider[]
  placeholder?: string
  emptyHint?: string
}) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<CommandResult[]>([])
  const [loading, setLoading] = useState(false)
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const restoreRef = useRef<HTMLElement | null>(null)
  const listboxId = useId()

  /* Load results (debounced 120ms, latest-response-wins). */
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    const t = window.setTimeout(async () => {
      const q = query.trim()
      const groups = await Promise.all(
        providers.map(async (p) => {
          try {
            return await p(q)
          } catch {
            return [] as CommandResult[]
          }
        }),
      ).catch(() => [] as CommandResult[][])
      if (cancelled) return
      // Client-side narrowing keeps every provider honest for typed queries.
      const needle = q.toLowerCase()
      const all = groups.flat().filter((r) => {
        if (!needle) return true
        return `${r.label} ${r.hint ?? ''} ${r.keywords ?? ''}`.toLowerCase().includes(needle)
      })
      setResults(all)
      setLoading(false)
      setActive(0)
    }, 120)
    return () => {
      cancelled = true
      window.clearTimeout(t)
    }
  }, [open, query, providers])

  /* Open/close lifecycle: reset, focus, scroll-lock, restore focus. */
  useEffect(() => {
    if (!open) return
    restoreRef.current = document.activeElement as HTMLElement | null
    setQuery('')
    setResults([])
    setActive(0)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const t = window.setTimeout(() => inputRef.current?.focus(), 0)
    return () => {
      document.body.style.overflow = prevOverflow
      window.clearTimeout(t)
      restoreRef.current?.focus?.()
    }
  }, [open])

  const grouped = useMemo(() => {
    const out: { group: string; items: { r: CommandResult; index: number }[] }[] = []
    let i = 0
    for (const r of results) {
      const g = r.group ?? 'Results'
      const last = out[out.length - 1]
      const entry = { r, index: i++ }
      if (last && last.group === g) last.items.push(entry)
      else out.push({ group: g, items: [entry] })
    }
    return out
  }, [results])

  const flat = useMemo(() => results, [results])

  const runResult = useCallback(
    (r: CommandResult | undefined) => {
      if (!r) return
      onClose()
      // Defer so focus restoration lands before navigation side effects.
      window.setTimeout(() => r.perform(), 0)
    },
    [onClose],
  )

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((a) => Math.min(flat.length - 1, a + 1))
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((a) => Math.max(0, a - 1))
      return
    }
    if (e.key === 'Home' && flat.length) {
      e.preventDefault()
      setActive(0)
      return
    }
    if (e.key === 'End' && flat.length) {
      e.preventDefault()
      setActive(flat.length - 1)
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      runResult(flat[active])
      return
    }
    if (e.key === 'Tab') {
      // Focus trap: Tab cycles input <-> list (list is aria-activedescendant).
      e.preventDefault()
      listRef.current?.focus()
    }
  }

  if (!open) return null

  return createPortal(
    <div className="fixed inset-0 z-[130] flex items-start justify-center px-4 pt-[12vh]">
      <div
        className="absolute inset-0 bg-[#0b1220]/45 backdrop-blur-[3px] dp-fade-up"
        onClick={onClose}
        aria-hidden="true"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="dp-fade-up relative z-10 w-full max-w-[560px] overflow-hidden rounded-[14px] border border-line bg-white shadow-modal"
      >
        <div className="flex items-center gap-2.5 border-b border-line px-4">
          <Search size={15} className="shrink-0 text-[#98a2b3]" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            role="combobox"
            aria-expanded="true"
            aria-controls={listboxId}
            aria-activedescendant={flat[active] ? `${listboxId}-opt-${active}` : undefined}
            aria-label="Search commands"
            placeholder={placeholder}
            className="h-[52px] w-full bg-transparent text-[13px] text-ink outline-none placeholder:text-[#98a2b3]"
          />
          <kbd className="hidden shrink-0 rounded-[6px] border border-line bg-surface-2 px-1.5 py-0.5 text-[9px] font-bold text-muted sm:block">
            ESC
          </kbd>
        </div>

        <div
          ref={listRef}
          id={listboxId}
          role="listbox"
          aria-label="Results"
          tabIndex={-1}
          className="max-h-[46vh] overflow-y-auto py-1.5 outline-none"
        >
          {loading && results.length === 0 && (
            <div className="space-y-2 px-3 py-2">
              <div className="dp-skeleton h-9 w-full" />
              <div className="dp-skeleton h-9 w-[92%]" />
              <div className="dp-skeleton h-9 w-[96%]" />
            </div>
          )}
          {!loading && flat.length === 0 && (
            <div className="px-4 py-8 text-center text-[12px] text-muted">{emptyHint}</div>
          )}
          {grouped.map((g) => (
            <div key={g.group} role="group" aria-label={g.group}>
              <div className="px-4 pb-1 pt-2 text-[9px] font-extrabold uppercase tracking-[.08em] text-[#8b96a8]">
                {g.group}
              </div>
              {g.items.map(({ r, index: me }) => {
                const isActive = me === active
                return (
                  <div
                    key={`${r.id}`}
                    id={`${listboxId}-opt-${me}`}
                    role="option"
                    aria-selected={isActive}
                    onMouseEnter={() => setActive(me)}
                    onClick={() => runResult(r)}
                    className={`mx-1.5 flex cursor-pointer items-center gap-2.5 rounded-[9px] px-2.5 py-2 transition-colors ${
                      isActive ? 'bg-brand-soft' : ''
                    }`}
                  >
                    {r.icon && (
                      <span className={`grid h-[28px] w-[28px] shrink-0 place-items-center rounded-[8px] ${isActive ? 'bg-white text-brand' : 'bg-surface-2 text-[#596579]'}`}>
                        {r.icon}
                      </span>
                    )}
                    <span className="min-w-0 flex-1">
                      <span className={`block truncate text-[12px] font-semibold ${r.dangerous ? 'text-danger' : 'text-ink'}`}>
                        {r.label}
                      </span>
                      {r.hint && <span className="block truncate text-[10px] text-muted">{r.hint}</span>}
                    </span>
                    {isActive && <CornerDownLeft size={13} className="shrink-0 text-brand" />}
                  </div>
                )
              })}
            </div>
          ))}
        </div>

        <div className="flex items-center gap-3 border-t border-line bg-surface-2 px-4 py-2 text-[9.5px] font-semibold text-muted">
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded-[5px] border border-line bg-white px-1 py-px text-[9px]">↑</kbd>
            <kbd className="rounded-[5px] border border-line bg-white px-1 py-px text-[9px]">↓</kbd>
            navigate
          </span>
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded-[5px] border border-line bg-white px-1 py-px text-[9px]">↵</kbd>
            select
          </span>
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded-[5px] border border-line bg-white px-1 py-px text-[9px]">⌘K</kbd>
            toggle
          </span>
        </div>
      </div>
    </div>,
    document.body,
  )
}
