/* Local loading/feedback primitives (kept per-app so the shared packages
 * boundary stays frozen). The `.skeleton` shimmer lives in each app's
 * styles.css; shapes below mirror the shell's real content blocks. */

/** Inline spinner for buttons while a mutation is in flight. */
export function Spinner({ size = 13 }: { size?: number }) {
  return (
    <span
      aria-hidden="true"
      className="inline-block animate-spin rounded-full border-2 border-current border-t-transparent"
      style={{ width: size, height: size }}
    />
  )
}

/**
 * Suspense fallback for lazy routes rendered inside the shell: page-title +
 * stat-card row + table-shaped rows, so the layout never jumps.
 */
export function RouteFallback() {
  return (
    <div className="p-[22px]" role="status" aria-live="polite" aria-label="Loading page">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="skeleton h-[26px] w-48" />
          <div className="skeleton mt-2 h-[12px] w-72" />
        </div>
        <div className="skeleton h-[34px] w-[110px] rounded-[9px]" />
      </div>
      <div className="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <div key={i} className="card p-[15px]">
            <div className="flex items-start justify-between gap-2.5">
              <div className="min-w-0 flex-1">
                <div className="skeleton h-[10px] w-20" />
                <div className="skeleton mt-2.5 h-[24px] w-16" />
              </div>
              <div className="skeleton h-[34px] w-[34px] rounded-[10px]" />
            </div>
          </div>
        ))}
      </div>
      <div className="card overflow-hidden">
        <div className="border-b border-line px-4 py-3">
          <div className="skeleton h-[12px] w-32" />
        </div>
        <div className="p-4">
          {[0, 1, 2, 3, 4].map((i) => (
            <div key={i} className="skeleton mb-2 h-12 w-full last:mb-0" />
          ))}
        </div>
      </div>
    </div>
  )
}

/** Full-viewport fallback for the outer Suspense (login/boot routes). */
export function ScreenFallback() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-app" role="status" aria-live="polite" aria-label="Loading">
      <div className="w-full max-w-[420px] px-6">
        <div className="mb-6 flex flex-col items-center gap-3">
          <div className="skeleton h-[42px] w-[42px] rounded-[12px]" />
          <div className="skeleton h-[16px] w-40" />
        </div>
        <div className="card p-6">
          <div className="skeleton mb-3 h-[12px] w-24" />
          <div className="skeleton mb-4 h-[38px] w-full rounded-[9px]" />
          <div className="skeleton mb-3 h-[12px] w-24" />
          <div className="skeleton mb-5 h-[38px] w-full rounded-[9px]" />
          <div className="skeleton h-[38px] w-full rounded-[9px]" />
        </div>
      </div>
    </div>
  )
}
