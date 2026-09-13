import { Component, ErrorInfo, ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

/**
 * App-shell crash fence (local to each app on purpose — package boundary is
 * frozen). Any render error, including a failed lazy-chunk fetch after a
 * deploy, lands here instead of a white screen: offer an in-place retry
 * first, then a hard reload which re-resolves stale asset hashes.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[EpicPanel] UI crashed:', error, info.componentStack)
  }

  private reset = () => this.setState({ error: null })

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="flex min-h-screen items-center justify-center bg-app p-4">
        <div className="card w-full max-w-[440px] p-8 text-center fade-up">
          <div className="mx-auto mb-4 grid h-11 w-11 place-items-center rounded-[12px] bg-danger-soft text-danger">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M12 9v4" /><path d="M12 17h.01" />
              <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
            </svg>
          </div>
          <div className="text-[15px] font-bold text-ink">This screen hit an unexpected error</div>
          <p className="mt-1.5 text-[12px] text-muted">
            Your data is safe — the interface just needs to recover.
          </p>
          <p className="mt-3 max-h-20 overflow-auto rounded-[9px] bg-surface-2 px-3 py-2 text-left font-mono text-[10.5px] text-sub">
            {this.state.error.message || String(this.state.error)}
          </p>
          <div className="mt-5 flex gap-2">
            <button className="btn-ghost flex-1 justify-center" onClick={this.reset} aria-label="Try again">
              Try again
            </button>
            <button className="btn-brand flex-1 justify-center" onClick={() => window.location.reload()} aria-label="Reload the page">
              Reload page
            </button>
          </div>
        </div>
      </div>
    )
  }
}
