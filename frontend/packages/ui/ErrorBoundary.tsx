/* ErrorBoundary (production hardening): catches render/lifecycle crashes in
 * its subtree, logs them to the console and shows a retry UI instead of a
 * white screen. Pure presentation + props — no app imports, per the package
 * contract. Pass `resetKey` (e.g. the route pathname) so navigating away
 * clears the crashed state automatically.
 */
import { Component, ReactNode } from 'react'
import { TriangleAlert } from 'lucide-react'

interface ErrorBoundaryProps {
  children: ReactNode
  /** Changing this value resets the boundary (e.g. pathname on navigation). */
  resetKey?: unknown
  /** Accessible region label for the fallback. */
  label?: string
}

interface ErrorBoundaryState {
  error: Error | null
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: { componentStack?: string | null }) {
    // eslint-disable-next-line no-console
    console.error('[EpicPanel] UI error:', error, info.componentStack ?? '')
  }

  componentDidUpdate(prev: ErrorBoundaryProps) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null })
    }
  }

  private retry = () => this.setState({ error: null })

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <div
        role="alert"
        aria-label={this.props.label ?? 'This view failed to load'}
        className="flex min-h-[50vh] items-center justify-center p-6"
      >
        <div className="card w-full max-w-[420px] p-6 text-center fade-up">
          <div className="mx-auto mb-3 grid h-[42px] w-[42px] place-items-center rounded-[12px] bg-danger-soft text-danger">
            <TriangleAlert size={21} strokeWidth={1.8} aria-hidden="true" />
          </div>
          <div className="text-[16px] font-bold tracking-[-.02em] text-ink">Something went wrong</div>
          <p className="mx-auto mt-1.5 max-w-[320px] text-[12px] text-muted">
            This view crashed. You can retry — if it keeps happening, sign out and back in.
          </p>
          <details className="mt-3 text-left">
            <summary className="cursor-pointer text-[11px] font-semibold text-sub">Error details</summary>
            <pre className="mt-2 overflow-x-auto rounded-[9px] border border-line bg-surface-2 p-3 text-[10.5px] leading-relaxed text-sub">
              {error.message || String(error)}
            </pre>
          </details>
          <div className="mt-4 flex items-center justify-center gap-2">
            <button className="btn-brand" onClick={this.retry}>
              Retry
            </button>
            <button className="btn-ghost" onClick={() => window.location.reload()}>
              Reload page
            </button>
          </div>
        </div>
      </div>
    )
  }
}
