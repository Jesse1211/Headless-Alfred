// ShellSocket: a thin reconnecting wrapper around the browser WebSocket.
// Exposes typed message variants matching the backend protocol, an
// onMessage callback, and a connection state machine. Reconnects with
// exponential backoff capped at 30 s.

export type ServerMsg =
  | { type: 'reattach'; sessionID: string; cmdId: string; command: string; startedAt: string; outputSoFar: string }
  | { type: 'idle'; sessionID: string }
  | { type: 'started'; sessionID: string; cmdId: string; command: string; startedAt: string }
  | { type: 'chunk'; sessionID: string; cmdId: string; data: string }
  | { type: 'done'; sessionID: string; cmdId: string; exitCode: number; finishedAt: string }
  | { type: 'session_closed'; sessionID: string }
  | { type: 'session_renamed'; sessionID: string; name: string }
  | { type: 'error'; sessionID?: string; code: string; message: string }
  | { type: 'pong' }

export type ClientMsg =
  | { type: 'run'; sessionID: string; command: string }
  | { type: 'ping' }

export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface ShellSocketOptions {
  url: string
  getToken: () => string
  onMessage: (m: ServerMsg) => void
  onState: (s: ConnState) => void
}

const DELAYS = [1000, 2000, 4000, 8000, 16000, 30000]

export class ShellSocket {
  private opts: ShellSocketOptions
  private ws: WebSocket | null = null
  private retry = 0
  private stopped = false
  private pingTimer: number | null = null

  constructor(opts: ShellSocketOptions) {
    this.opts = opts
  }

  start(): void {
    this.stopped = false
    this.connect()
  }

  stop(): void {
    this.stopped = true
    if (this.pingTimer != null) {
      window.clearInterval(this.pingTimer)
      this.pingTimer = null
    }
    this.ws?.close()
    this.ws = null
    this.opts.onState('closed')
  }

  send(m: ClientMsg): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
    }
  }

  private connect(): void {
    if (this.stopped) return
    this.opts.onState(this.retry === 0 ? 'connecting' : 'reconnecting')
    const token = this.opts.getToken()
    const sep = this.opts.url.includes('?') ? '&' : '?'
    const url = `${this.opts.url}${sep}token=${encodeURIComponent(token)}`
    const ws = new WebSocket(url)
    this.ws = ws

    ws.addEventListener('open', () => {
      this.retry = 0
      this.opts.onState('open')
      this.pingTimer = window.setInterval(() => {
        this.send({ type: 'ping' })
      }, 25_000) as unknown as number
    })

    ws.addEventListener('message', (ev) => {
      try {
        const m = JSON.parse(ev.data as string) as ServerMsg
        this.opts.onMessage(m)
      } catch {
        // ignore malformed payloads — server protocol bug, not our problem
      }
    })

    ws.addEventListener('close', () => {
      if (this.pingTimer != null) {
        window.clearInterval(this.pingTimer)
        this.pingTimer = null
      }
      if (this.stopped) return
      const delay = DELAYS[Math.min(this.retry, DELAYS.length - 1)]
      this.retry += 1
      this.opts.onState('reconnecting')
      window.setTimeout(() => this.connect(), delay)
    })

    ws.addEventListener('error', () => {
      // Close handler will follow with the actual reconnect.
    })
  }
}
