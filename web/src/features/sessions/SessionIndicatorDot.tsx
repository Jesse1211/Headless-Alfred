import type { SessionIndicator } from './sessionStatus'

const LABEL: Record<SessionIndicator, string> = {
  idle: 'Connected · your turn',
  busy: 'Connected · waiting for reply',
  disconnected: 'Disconnected',
}

interface Props {
  status: SessionIndicator
  // Tooltip anchor side. Use 'left' near the right edge of the viewport
  // so the bubble doesn't get clipped (header indicator, etc.).
  tooltipSide?: 'left' | 'right'
}

// Single combined connection + turn indicator. Renders a small dot
// for idle/busy and a warning glyph for disconnected.
export function SessionIndicatorDot({ status, tooltipSide }: Props) {
  if (status === 'disconnected') {
    return (
      <span
        className="session-indicator session-indicator--disconnected"
        data-tooltip={LABEL[status]}
        data-tooltip-side={tooltipSide}
        aria-label={LABEL[status]}
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
