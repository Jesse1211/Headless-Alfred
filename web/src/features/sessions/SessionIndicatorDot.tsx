import type { SessionIndicator } from './sessionStatus'
import type { ConnInfo } from '../../lib/ws'

const LABEL: Record<SessionIndicator, string> = {
  idle: 'Connected · your turn',
  busy: 'Connected · waiting for reply',
  needsAction: 'Needs your decision (allow / deny / answer)',
  disconnected: 'Disconnected',
}

interface Props {
  status: SessionIndicator
  // Tooltip anchor side. Use 'left' near the right edge of the viewport
  // so the bubble doesn't get clipped (header indicator, etc.).
  tooltipSide?: 'left' | 'right'
  // Optional WebSocket diagnostic detail. Only consulted when status
  // is 'disconnected' — composes the tooltip body. Pass undefined to
  // keep the generic 'Disconnected' label (e.g. for sidebar rows
  // where we don't want per-row connection detail).
  connInfo?: ConnInfo
}

// Single combined connection + turn indicator. Renders a small dot
// for idle/busy/needsAction and a warning glyph for disconnected.
export function SessionIndicatorDot({ status, tooltipSide, connInfo }: Props) {
  if (status === 'disconnected') {
    const tooltip = formatDisconnectedTooltip(connInfo)
    return (
      <span
        className="session-indicator session-indicator--disconnected"
        data-tooltip={tooltip}
        data-tooltip-side={tooltipSide}
        aria-label={tooltip}
        role="img"
      >
        {/* Triangle with exclamation mark — pure SVG, no font dep */}
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
          <path
            d="M8 1.5 L15 14 L1 14 Z"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinejoin="round"
            fill="none"
          />
          <line x1="8" y1="6" x2="8" y2="10" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
          <circle cx="8" cy="12" r="0.8" fill="currentColor" />
        </svg>
      </span>
    )
  }
  return (
    <span
      className={`session-indicator session-indicator--${status}`}
      data-tooltip={LABEL[status]}
      data-tooltip-side={tooltipSide}
      aria-label={LABEL[status]}
      role="img"
    />
  )
}

// formatDisconnectedTooltip composes a multi-line tooltip body from
// the WebSocket diagnostic info we capture in ShellSocket. Returns
// just "Disconnected" when info is missing or empty so the user
// always gets *something* even if the diagnostics weren't recorded
// (e.g. very first page load before any connect attempted).
//
// Lines kept short — tooltip is data-tooltip + ::after pseudo, no
// width constraint, so very long single lines wrap awkwardly. We
// emit one fact per line with `\n` separators; the index.css rule
// for tooltips uses `white-space: pre-line` so newlines render.
function formatDisconnectedTooltip(info: ConnInfo | undefined): string {
  const lines: string[] = ['Disconnected']
  if (!info) return lines.join('\n')
  if (info.lastCloseCode != null) {
    const codeName = closeCodeName(info.lastCloseCode)
    let line = `code: ${info.lastCloseCode}`
    if (codeName) line += ` (${codeName})`
    if (info.lastCloseReason) line += ` · ${info.lastCloseReason}`
    lines.push(line)
  }
  if (info.lastOpenAt) {
    const ago = relativeAgo(info.lastOpenAt)
    if (ago) lines.push(`last seen ${ago}`)
  }
  if (info.retries != null && info.retries > 0) {
    lines.push(`reconnect attempt ${info.retries}`)
  }
  return lines.join('\n')
}

// closeCodeName maps the small set of WebSocket close codes we
// actually see to human labels. Unknown codes return ''.
function closeCodeName(code: number): string {
  switch (code) {
    case 1000: return 'normal'
    case 1001: return 'going away'
    case 1006: return 'abnormal'    // network blip, server killed, etc — no Close frame
    case 1011: return 'server error'
    case 1012: return 'service restart'
    case 4001: return 'auth'        // application-defined; we use 4001 for auth in some places
    default:
      if (code >= 4000) return 'app-defined'
      return ''
  }
}

// relativeAgo turns an ISO timestamp into "12s ago" / "3m ago".
// Returns '' on parse failure rather than NaN-poisoning the tooltip.
function relativeAgo(iso: string): string {
  const t = Date.parse(iso)
  if (isNaN(t)) return ''
  const sec = Math.max(0, Math.round((Date.now() - t) / 1000))
  if (sec < 60) return `${sec}s ago`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.round(min / 60)
  return `${hr}h ago`
}
