import { PerSessionState } from './types'

// turnStatus distinguishes three things the user might need to know:
//
//   - 'needsAction': Claude is waiting on the user to make a decision
//     — a PreToolUse Allow/Deny card is pending, or an AskUserQuestion
//     card needs answers. Most urgent: user has to do something or
//     Claude sits forever. Yellow dot.
//
//   - 'busy': Claude / a shell command is currently working on the
//     user's request. User can sit back; the next thing on screen is
//     a reply. Red dot.
//
//   - 'idle': Nothing in flight, no pending approvals — your turn to
//     type. Green dot.
//
// Priority is needsAction > busy > idle: if a tool approval came in
// mid-turn (inFlight is still true while pending sits there), the
// user's action is what unblocks Claude, so the indicator surfaces
// that rather than the in-flight state.
export type TurnStatus = 'idle' | 'busy' | 'needsAction'

export function turnStatus(ps: PerSessionState | undefined): TurnStatus {
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
