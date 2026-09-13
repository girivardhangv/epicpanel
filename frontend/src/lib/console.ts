import { useEffect, useRef, useState } from 'react'

export interface ConsoleLine {
  seq: number
  ts: string
  text: string
}

type Kind = 'minecraft' | 'bots'

function wsBase(): string {
  const apiBase =
    (import.meta as any).env?.VITE_API_URL ||
    `${location.protocol}//${location.hostname}:8080`
  const proto = apiBase.startsWith('https') ? 'wss' : 'ws'
  return `${proto}://${apiBase.replace(/^https?:\/\//, '')}`
}

export function useConsoleStream(orgID: string | undefined, kind: Kind, resourceID: string | undefined) {
  const [lines, setLines] = useState<ConsoleLine[]>([])
  const [live, setLive] = useState(false)
  const [err, setErr] = useState('')
  const cursorRef = useRef(0)
  const frameType = kind === 'minecraft' ? 'mc_console' : 'bot_console'

  // echo appends a line locally the instant the customer submits it, so the
  // typed command shows with zero latency; the real server output follows.
  const echo = (text: string) => {
    const line: ConsoleLine = {
      seq: Number.MAX_SAFE_INTEGER - 1,
      ts: new Date().toISOString(),
      text: '> ' + text,
    }
    setLines((prev) => [...prev.slice(-999), line])
  }

  useEffect(() => {
    if (!orgID || !resourceID) return
    const path = `/v1/organizations/${orgID}/${kind}/${resourceID}/console`
    let closed = false
    let ws: WebSocket | null = null
    let retry: number | null = null

    const connect = () => {
      if (closed) return
      try {
        ws = new WebSocket(`${wsBase()}${path}/ws`)
      } catch {
        return
      }
      ws.onopen = () => setLive(true)
      ws.onmessage = (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const msg = JSON.parse(ev.data)
          if (msg?.type !== frameType) return
          if (Array.isArray(msg.tail)) {
            setLines(msg.tail)
            cursorRef.current = msg.tail.length ? msg.tail[msg.tail.length - 1].seq : cursorRef.current
          } else if (msg.line) {
            cursorRef.current = Math.max(cursorRef.current, msg.line.seq)
            setLines((prev) => [...prev.slice(-999), msg.line])
          }
        } catch {
          /* ignore malformed frames */
        }
      }
      ws.onerror = () => setErr('console stream error')
      ws.onclose = () => {
        setLive(false)
        if (!closed) retry = window.setTimeout(connect, 3000)
      }
    }
    connect()
    return () => {
      closed = true
      if (retry) window.clearTimeout(retry)
      ws?.close()
    }
  }, [orgID, resourceID, kind, frameType])

  return { lines, live, err, echo }
}