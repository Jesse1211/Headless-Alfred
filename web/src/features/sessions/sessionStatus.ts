import { PerSessionState } from './types'

// isWaitingForReply: true iff the session is mid-turn from the user's
// perspective — they sent something, the system is working, they
// should wait. Maps to the red turn-indicator.
//
//   - claude mode: Claude has an in-flight turn (streaming, running a
//     tool, awaiting tool decision, etc.)
//   - shell mode: a command is running
//   - no ps yet (hasn't subscribed / first frame not in): treat as
//     idle (green) because there's no signal otherwise
//
// Conversely false means "your turn" — green.
export function isWaitingForReply(ps: PerSessionState | undefined): boolean {
  if (!ps) return false
  if (ps.mode === 'claude') {
    return !!ps.claude?.inFlight
  }
  return ps.running != null
}
