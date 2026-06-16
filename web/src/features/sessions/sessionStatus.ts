import { PerSessionState } from './types'
import type { ConnState } from '../../lib/ws'

// SessionIndicator is the single combined status badge shown in the
// header and in each sidebar row. Priorities:
//
//   - 'disconnected' (⚠️ icon): WebSocket isn't open. Overrides
//     everything else — without a connection neither idle nor busy
//     is meaningful (we don't actually know what Claude is doing).
//
//   - 'busy' (red dot): connected AND Claude in-flight / shell
//     command running. User waits.
//
//   - 'idle' (green dot): connected AND nothing in flight. Your
//     turn to type.
//
// Pending tool approvals / AskUserQuestion cards aren't surfaced
// here — they ride along with inFlight=true so the dot stays red
// until the user resolves them, and the approval card itself in
// the chat stream is the actionable UI.
export type SessionIndicator = 'idle' | 'busy' | 'disconnected'

export function sessionIndicator(
  connState: ConnState,
  ps: PerSessionState | undefined | null,
): SessionIndicator {
  if (connState !== 'open') return 'disconnected'
  if (!ps) return 'idle'
  if (ps.mode === 'claude') {
    return ps.claude?.inFlight ? 'busy' : 'idle'
  }
  return ps.running != null ? 'busy' : 'idle'
}
