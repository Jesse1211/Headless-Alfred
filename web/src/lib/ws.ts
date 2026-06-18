// ShellSocket: a thin reconnecting wrapper around the browser WebSocket.
// Exposes typed message variants matching the backend protocol, an
// onMessage callback, and a connection state machine. Reconnects with
// exponential backoff capped at 30 s.

export type ClaudeRenderer = 'tui' | 'ui'

// One parsed stream-json event from the in-pod `claude -p` invocation.
// `payload` is the variant-specific shape; consumers narrow by
// `eventKind`. See internal/claude/event.go for the source of truth.
export type ClaudeEventKind =
  | 'system'
  | 'rate_limit'
  | 'text_delta'
  | 'text_block_end'
  | 'thinking_delta'
  | 'tool_use_start'
  | 'tool_use_end'
  | 'tool_result'
  | 'message_start'
  | 'message_delta'
  | 'message_stop'
  | 'result'
  // v0.4: CLI lifecycle events (emitted under --include-hook-events).
  | 'task_started'
  | 'task_notification'
  | 'task_updated'
  | 'hook_started'
  | 'hook_response'
  | 'unknown'

// DiskUsage payload on the disk_usage WS frame (and the
// GET /api/disk-usage response). Bytes from the backend; the
// frontend formats with formatBytes for display.
export interface DiskUsage {
  path: string
  totalBytes: number
  usedBytes: number
  availableBytes: number
  // Used / (Used + Available) * 100, one-decimal rounded.
  usedPercent: number
}

export type ServerMsg =
  | { type: 'reattach'; sessionID: string; cmdId: string; command: string; startedAt: string; outputSoFar: string; mode?: 'shell' | 'claude'; renderer?: ClaudeRenderer | ''; templateId?: string }
  | { type: 'idle'; sessionID: string; mode?: 'shell' | 'claude'; renderer?: ClaudeRenderer | ''; templateId?: string }
  | { type: 'started'; sessionID: string; cmdId: string; command: string; startedAt: string }
  | { type: 'chunk'; sessionID: string; cmdId: string; data: string }
  | { type: 'done'; sessionID: string; cmdId: string; exitCode: number; finishedAt: string }
  | { type: 'session_closed'; sessionID: string }
  | { type: 'session_renamed'; sessionID: string; name: string }
  | { type: 'claude_entered'; sessionID: string; renderer?: ClaudeRenderer }
  | { type: 'claude_exited'; sessionID: string }
  | { type: 'pty_data'; sessionID: string; data: string }
  | { type: 'claude_event'; sessionID: string; eventKind: ClaudeEventKind; payload: unknown; timestamp?: string }
  | { type: 'tool_approval_request'; sessionID: string; toolUseId: string; tool: string; toolInput: unknown }
  | { type: 'claude_error'; sessionID: string; code: string; message: string; timestamp?: string }
  // claude_run_ended fires when the per-prompt `claude -p` process
  // exits, REGARDLESS of whether a `result` event was emitted first.
  // It's a backstop so the frontend always clears inFlight even if
  // the runner died abnormally (OOM kill, parser bug, etc.). When
  // the run ended cleanly with a `result`, the frame is redundant
  // and the reducer treats it as a no-op for the turn state.
  | { type: 'claude_run_ended'; sessionID: string; message?: string; timestamp?: string }
  | { type: 'summary_updated'; sessionID: string }
  | { type: 'note_updated'; sessionID: string }
  | { type: 'recap_updated'; date: string }
  | { type: 'disk_usage'; diskUsage: DiskUsage }
  | { type: 'user_prompt'; sessionID: string; text: string }
  | { type: 'error'; sessionID?: string; code: string; message: string }
  | { type: 'pong' }

export type ClientMsg =
  | { type: 'run'; sessionID: string; command: string }
  | { type: 'enter_claude'; sessionID: string; renderer?: ClaudeRenderer; bypassPermissions?: boolean; templateId?: string }
  | { type: 'exit_claude'; sessionID: string }
  | { type: 'stdin'; sessionID: string; data: string }
  | { type: 'claude_prompt'; sessionID: string; text: string; renderTemplate?: string; templates?: string[] }
  | { type: 'tool_decision'; sessionID: string; toolUseId: string; decision: 'allow' | 'deny'; reason?: string }
  | { type: 'interrupt'; sessionID: string }
  | { type: 'ping' }

export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed'

// ConnInfo carries diagnostic detail about the WebSocket's recent
// history. Useful for the disconnected-state tooltip so the user
// can see WHY they got dropped (server kicked them / network blip /
// auth expired) rather than just a generic ⚠ icon. All fields are
// optional — emitted alongside state changes when they're known.
export interface ConnInfo {
  // WebSocket close code from the last close event. 1000 = clean,
  // 1006 = abnormal (most common: network or server gone), 4xxx =
  // application-defined.
  lastCloseCode?: number
  // Human-readable reason the server sent (often empty for 1006).
  lastCloseReason?: string
  // ISO timestamp of the last successful open. Lets the tooltip
  // say "last seen 12s ago".
  lastOpenAt?: string
  // Reconnect attempts since the last successful open. 0 while
  // connected; 1+ during reconnecting.
  retries?: number
}

export interface ShellSocketOptions {
  url: string
  getToken: () => string
  onMessage: (m: ServerMsg) => void
  onState: (s: ConnState, info?: ConnInfo) => void
}

const DELAYS = [1000, 2000, 4000, 8000, 16000, 30000]

export class ShellSocket {
  private opts: ShellSocketOptions
  private ws: WebSocket | null = null
  private retry = 0
  private stopped = false
  private pingTimer: number | null = null
  // Carried in onState calls so the consumer (useSessions) can
  // surface it in the disconnected-dot tooltip. Updated on
  // open / close events.
  private info: ConnInfo = {}

  constructor(opts: ShellSocketOptions) {
    this.opts = opts
  }

  start(): void {
    this.stopped = false
    // If start() is called while a previous ws is still hanging
    // around (React StrictMode dev double-mount races: stop() set
    // stopped=true and called ws.close(), but the browser hasn't
    // actually torn down the underlying socket yet), abandon the
    // old socket BEFORE creating a new one. Otherwise the old
    // socket can keep firing onMessage from in-flight frames AND
    // the server sees two concurrent WS clients for several seconds
    // — which doubles pty_data, doubles tool approvals, and lets
    // user keystrokes echo on the stale xterm too.
    this.abandonCurrent()
    this.connect()
  }

  stop(): void {
    this.stopped = true
    this.abandonCurrent()
    this.opts.onState('closed', this.info)
  }

  // abandonCurrent detaches all listeners from this.ws so any
  // late-firing events (a final pty_data, the close handler that
  // would otherwise schedule a reconnect, etc.) don't leak into
  // the parent's handlers. Then closes the socket. Safe to call
  // when this.ws is null.
  private abandonCurrent(): void {
    if (this.pingTimer != null) {
      window.clearInterval(this.pingTimer)
      this.pingTimer = null
    }
    const ws = this.ws
    if (!ws) return
    ws.onopen = null
    ws.onmessage = null
    ws.onclose = null
    ws.onerror = null
    try { ws.close() } catch { /* already closing */ }
    this.ws = null
  }

  send(m: ClientMsg): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
    }
  }

  private connect(): void {
    if (this.stopped) return
    this.info = { ...this.info, retries: this.retry }
    this.opts.onState(this.retry === 0 ? 'connecting' : 'reconnecting', this.info)
    const token = this.opts.getToken()
    const sep = this.opts.url.includes('?') ? '&' : '?'
    const url = `${this.opts.url}${sep}token=${encodeURIComponent(token)}`
    const ws = new WebSocket(url)
    this.ws = ws

    // Use direct on* assignment instead of addEventListener so that
    // abandonCurrent() can wipe them with a single assignment.
    ws.onopen = () => {
      // Guard: if this socket was abandoned while in CONNECTING state,
      // ignore its eventual open. (Browsers fire onopen before they
      // honour an early close() in some implementations.)
      if (this.ws !== ws) return
      this.retry = 0
      this.info = { ...this.info, lastOpenAt: new Date().toISOString(), retries: 0 }
      this.opts.onState('open', this.info)
      this.pingTimer = window.setInterval(() => {
        this.send({ type: 'ping' })
      }, 25_000) as unknown as number
    }

    ws.onmessage = (ev) => {
      if (this.ws !== ws) return
      try {
        const m = JSON.parse(ev.data as string) as ServerMsg
        this.opts.onMessage(m)
      } catch {
        // ignore malformed payloads — server protocol bug, not our problem
      }
    }

    ws.onclose = (ev) => {
      if (this.ws !== ws) return // abandoned
      if (this.pingTimer != null) {
        window.clearInterval(this.pingTimer)
        this.pingTimer = null
      }
      this.ws = null
      // Capture WHY the socket closed so the disconnected-state
      // tooltip can show something useful.
      this.info = {
        ...this.info,
        lastCloseCode: ev.code,
        lastCloseReason: ev.reason || undefined,
      }
      if (this.stopped) return
      const delay = DELAYS[Math.min(this.retry, DELAYS.length - 1)]
      this.retry += 1
      this.info = { ...this.info, retries: this.retry }
      this.opts.onState('reconnecting', this.info)
      window.setTimeout(() => this.connect(), delay)
    }

    ws.onerror = () => {
      // Close handler will follow with the actual reconnect.
    }
  }
}
