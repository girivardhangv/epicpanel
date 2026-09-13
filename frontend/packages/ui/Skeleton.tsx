/* Skeleton loaders (verbatim work item: "skeleton loaders — no
 * spinners-as-content"). Pure geometry: shimmering blocks that mirror the
 * shape of the content they stand in for. The shimmer lives in the
 * design-system stylesheet (`.skeleton` / `.dp-skeleton`); these components
 * add the accessible wiring (role="status" + label, aria-hidden blocks).
 */
import { ReactNode } from 'react'

export function Skeleton({ className = '', rounded = 'rounded-[9px]', label }: {
  /** Tailwind size classes, e.g. `h-7 w-16`. */
  className?: string
  rounded?: string
  /** Accessible loading label. When set, the block announces as a status. */
  label?: string
}) {
  if (label) {
    return (
      <div role="status" aria-label={label} aria-live="polite">
        <div aria-hidden="true" className={`skeleton ${rounded} ${className}`} />
      </div>
    )
  }
  return <div aria-hidden="true" className={`skeleton ${rounded} ${className}`} />
}

/** Multi-line paragraph placeholder (last line shorter, like real copy). */
export function SkeletonText({ lines = 3, label }: { lines?: number; label?: string }) {
  const widths = ['w-full', 'w-[96%]', 'w-[88%]', 'w-[92%]', 'w-[70%]']
  return (
    <div role={label ? 'status' : undefined} aria-label={label} aria-live={label ? 'polite' : undefined}>
      {Array.from({ length: lines }).map((_, i) => (
        <div
          key={i}
          aria-hidden="true"
          className={`skeleton mt-1.5 h-[10px] first:mt-0 ${widths[i % widths.length]}`}
        />
      ))}
    </div>
  )
}

/** Stat-card placeholder matching StatCard geometry (icon chip + big number). */
export function SkeletonCard({ label }: { label?: string }) {
  return (
    <div role={label ? 'status' : undefined} aria-label={label} aria-live={label ? 'polite' : undefined}>
      <div aria-hidden="true" className="card p-[15px]">
        <div className="flex items-start justify-between gap-2.5">
          <div className="min-w-0 flex-1">
            <div className="skeleton h-[10px] w-20" />
            <div className="skeleton mt-2.5 h-[24px] w-16" />
          </div>
          <div className="skeleton h-[34px] w-[34px] rounded-[10px]" />
        </div>
      </div>
    </div>
  )
}

/** List of row placeholders matching the ref-table row height. */
export function SkeletonTable({ rows = 4, height = 'h-12', label }: {
  rows?: number
  height?: string
  label?: string
}) {
  return (
    <div role={label ? 'status' : undefined} aria-label={label} aria-live={label ? 'polite' : undefined}>
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} aria-hidden="true" className={`skeleton mb-2 ${height} w-full last:mb-0`} />
      ))}
    </div>
  )
}

/** Full-screen gate placeholder (auth/session boot), replaces bare spinners. */
export function SkeletonScreen({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-app" role="status" aria-live="polite" aria-label={label}>
      <div className="w-full max-w-[860px] px-6">
        <div className="mb-6 flex items-center gap-3">
          <div aria-hidden="true" className="skeleton h-[42px] w-[42px] rounded-[12px]" />
          <div aria-hidden="true" className="skeleton h-[16px] w-48" />
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} aria-hidden="true" className="skeleton h-[92px] rounded-card" />
          ))}
        </div>
        <div aria-hidden="true" className="skeleton mt-4 h-[220px] rounded-card" />
      </div>
    </div>
  )
}

/** Branded page-body skeleton for route transitions (Suspense fallback):
 * matches the standard page container (`max-w-[1400px] px-4 py-6`) so the
 * swap from skeleton to real content causes no layout jump. */
export function RouteFallback({ label = 'Loading page' }: { label?: string }) {
  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6" role="status" aria-live="polite" aria-label={label}>
      <div aria-hidden="true" className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="skeleton h-[24px] w-44" />
          <div className="skeleton mt-2 h-[10px] w-64" />
        </div>
        <div className="skeleton h-[38px] w-[132px] rounded-[9px]" />
      </div>
      <div className="mb-4 grid grid-cols-1 gap-3.5 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <SkeletonCard key={i} />
        ))}
      </div>
      <div className="skeleton h-[240px] rounded-card" />
    </div>
  )
}

/** Convenience: wrap any children as an announced loading region. */
export function LoadingRegion({ label, children }: { label: string; children?: ReactNode }) {
  return (
    <div role="status" aria-live="polite" aria-label={label}>
      {children}
    </div>
  )
}
