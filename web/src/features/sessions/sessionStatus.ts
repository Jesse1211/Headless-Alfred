import { PerSessionState } from './types'

// turnStatus: whose turn is it?
//
//   - 'busy' (red): Claude or a shell command is running. User waits.
//   - 'idle' (green): nothing in flight — your turn to type.
//
// Pending tool approvals / AskUserQuestion cards live in the chat
// stream as their own UI; we don't surface them on the indicator
// (they ride along with inFlight=true so the dot stays red until
// the user resolves them, which is the same red 'don't go away'
// signal).
export type TurnStatus = 'idle' | 'busy'

export function turnStatus(ps: PerSessionState | undefined): TurnStatus {
  if (!ps) return 'idle'
  if (ps.mode === 'claude') {
    return ps.claude?.inFlight ? 'busy' : 'idle'
  }
  return ps.running != null ? 'busy' : 'idle'
}
