import { useEffect, useRef, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { useAuth } from '@/context/AuthContext'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

export function TerminalPage() {
  const { org, user } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const termDiv = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<'connecting' | 'open' | 'closed'>('connecting')
  const [error, setError] = useState('')
  const everOpened = useRef(false)

  useEffect(() => {
    if (!org || !user) return
    const term = new XTerm({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'Menlo, monospace',
      theme: { background: '#0D1321' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(termDiv.current!)
    fit.fit()

    // WebSockets connect straight to the API (the vite dev proxy does not
    // upgrade cleanly in v8/rolldown; in production the API sits behind the
    // same origin so this falls back naturally).
    const apiBase = (import.meta as any).env?.VITE_API_URL || `${location.protocol}//${location.hostname}:8080`
    const proto = apiBase.startsWith('https') ? 'wss' : 'ws'
    const ws = new WebSocket(
      `${proto}://${apiBase.replace(/^https?:\/\//, '')}/v1/organizations/${org.id}/websites/${websiteId}/terminal`,
    )
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => {
      everOpened.current = true
      setStatus('open')
    }
    ws.onmessage = (ev) => term.write(new Uint8Array(ev.data))
    ws.onclose = () => {
      // Normal after idle timeout or logout — only warn if we never connected.
      setStatus('closed')
      if (!everOpened.current) setError('Could not connect to the terminal — is the panel running?')
    }
    ws.onerror = () => {
      if (!everOpened.current) setError('WebSocket error — is the panel reachable?')
    }

    term.onData((data: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(data))
    })
    const onResize = () => {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ resize: { rows: term.rows, cols: term.cols } }))
      }
    }
    window.addEventListener('resize', onResize)
    onResize()

    return () => {
      window.removeEventListener('resize', onResize)
      ws.close()
      term.dispose()
    }
  }, [org?.id, websiteId, user])

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Link to={`/sites/${websiteId ?? ''}`} className="icon-btn !h-[34px] !w-[34px]" title="Back" aria-label="Back to site">
          <ArrowLeft size={15} />
        </Link>
        <div className="min-w-0 flex-1">
          <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Terminal</h1>
          <p className="mt-[5px] text-[12px] text-muted">Restricted shell running as this site's user. Idle sessions close after 10 minutes.</p>
        </div>
        <span className={`status-chip ${
          status === 'open' ? 'status-live' : status === 'connecting' ? 'status-warning' : ''
        }`}>
          {status.toUpperCase()}
        </span>
      </div>
      {error && <div className="mb-4 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">{error}</div>}
      <div className="overflow-hidden rounded-[14px] border border-line bg-[#0D1321] p-2 shadow-card">
        <div ref={termDiv} className="h-[62vh] w-full" />
      </div>
    </div>
  )
}

