import { PerSessionState } from './types'
import type { ConnState } from '../../lib/ws'

// SessionIndicator is the single combined status badge shown in the
// header and in each sidebar row. Priorities:
//
//   - 'disconnected' (⚠️ icon): WebSocket isn't open. Overrides
//     everything else — without a connection neither idle nor busy
//     nor needsAction is meaningful (we don't actually know what
//     Claude is doing).
//
//   - 'needsAction' (yellow dot): connected AND Claude has a pending
//     tool approval (PreToolUse Allow/Deny) or a pending
//     AskUserQuestion card. Higher priority than 'busy' because
//     inFlight is usually still true while these sit there — but
//     the user's decision is what unblocks Claude, so surface the
//     actionable state rather than the merely-in-flight one.
//
//   - 'busy' (red dot): connected AND Claude in-flight / shell
//     command running, with no pending user action. User waits.
//
//   - 'idle' (green dot): connected AND nothing in flight, no
//     pending user action. Your turn to type.
export type SessionIndicator = 'idle' | 'busy' | 'needsAction' | 'disconnected'

export function sessionIndicator(
  connState: ConnState,
  ps: PerSessionState | undefined | null,
): SessionIndicator {
  if (connState !== 'open') return 'disconnected'
  if (!ps) return 'idle'
  if (ps.mode === 'claude') {
    const c = ps.claude
    if (c && (c.pending.length > 0 || c.pendingQuestions.length > 0)) {
      return 'needsAction'
    }
    return c?.inFlight ? 'busy' : 'idle'
  }
  return ps.running != null ? 'busy' : 'idle'
}
