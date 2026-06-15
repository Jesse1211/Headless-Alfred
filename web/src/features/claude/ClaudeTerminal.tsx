import { useEffect, useRef } from 'react'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'
import './ClaudeTerminal.css'

interface Props {
  sessionID: string
  registerPtyHandler: (sid: string, cb: (bytes: Uint8Array) => void) => () => void
  sendStdin: (sid: string, bytes: Uint8Array) => void
}

/**
 * ClaudeTerminal renders an xterm.js terminal bound to the WS session.
 * - Server pty_data → terminal.write
 * - User keystrokes → sendStdin (raw bytes)
 *
 * One Terminal per mount. When the user exits claude (mode flip) the
 * parent unmounts this component, disposing the terminal.
 */
export function ClaudeTerminal({ sessionID, registerPtyHandler, sendStdin }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)

  useEffect(() => {
    if (!containerRef.current) return
    const term = new Terminal({
      cursorBlink: true,
      fontFamily:
        '"SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace',
      fontSize: 13,
      theme: {
        background: '#1e1e1e',
        foreground: '#dcdfe4',
        cursor: '#dcdfe4',
      },
      // We don't ask the backend to resize the PTY (yet); default 80x24
      // is fine for v1 of claude mode. The fit addon makes the visual
      // terminal match the container size; the PTY itself stays 80x24,
      // which claude handles by line-wrapping.
      scrollback: 5000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()
    termRef.current = term

    // Send any user keystrokes upstream as base64-encoded stdin.
    const dataDisposable = term.onData((str) => {
      const bytes = new TextEncoder().encode(str)
      sendStdin(sessionID, bytes)
    })

    // Forward server pty_data into the terminal.
    const unregister = registerPtyHandler(sessionID, (bytes) => {
      // term.write accepts Uint8Array directly.
      term.write(bytes)
    })

    // Resize the visual area when the window changes.
    const onResize = () => {
      try { fit.fit() } catch { /* container removed */ }
    }
    window.addEventListener('resize', onResize)

    // Focus on mount so keystrokes go to the terminal immediately.
    term.focus()

    return () => {
      window.removeEventListener('resize', onResize)
      unregister()
      dataDisposable.dispose()
      // xterm.js schedules internal viewport refreshes via rAF /
      // setTimeout. Calling term.dispose() synchronously while one
      // of those is queued causes "Cannot read properties of
      // undefined (reading 'dimensions')" the next tick. Defer
      // dispose so the queued refresh runs against a still-live
      // renderer, then nuke. The setTimeout(0) is enough — xterm
      // schedules at most one frame ahead.
      const t = term
      termRef.current = null
      setTimeout(() => {
        try { t.dispose() } catch { /* already gone */ }
      }, 0)
    }
  }, [sessionID, registerPtyHandler, sendStdin])

  return <div className="claude-terminal" ref={containerRef} />
}
