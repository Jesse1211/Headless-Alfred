import { useEffect } from 'react'
import { getClaudeState } from '../../lib/api'
import {
  ClaudeState,
  PerSessionState,
  emptyClaudeState,
  emptyPerSessionState,
} from './types'

interface Args {
  selectedSessionID: string | null
  perSession: Map<string, PerSessionState>
  setPerSession: (
    updater: (prev: Map<string, PerSessionState>) => Map<string, PerSessionState>
  ) => void
  // Stamped onto each successful hydrate so useSessions can detect a
  // stale session ("hydrated at an earlier WS epoch than current") and
  // re-clear turnsLoaded when the user finally switches to it.
  // Optional so existing tests that only care about basic hydration
  // don't have to wire it; defaults to 0.
  wsEpoch?: number
}

// useClaudeStateLoader fetches the server-authoritative ClaudeState
// once per session per page lifecycle. Idempotent — guarded by
// claude.turnsLoaded. Replaces useClaudeHistoryLoader. The previous
// loader had a "preserve local turns if any" merge branch; this one
// does not — the server is the truth source, so we overwrite in full
// on success.
//
// Failure modes:
//   - HTTP error: existing claude state (if any) is preserved; we
//     mark turnsLoaded=true so we don't loop, and surface a
//     lastError banner.
//   - Session not in claude UI mode: hook is a no-op.
export function useClaudeStateLoader({
  selectedSessionID,
  perSession,
  setPerSession,
  wsEpoch = 0,
}: Args) {
  const ps = selectedSessionID ? perSession.get(selectedSessionID) : undefined
  const mode = ps?.mode
  const renderer = ps?.renderer
  const turnsLoaded = ps?.claude?.turnsLoaded === true

  useEffect(() => {
    if (!selectedSessionID) return
    if (mode !== 'claude' || renderer !== 'ui') return
    if (turnsLoaded) return

    let alive = true
    getClaudeState(selectedSessionID)
      .then((state: ClaudeState) => {
        if (!alive) return
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          next.set(selectedSessionID, {
            ...cur,
            claude: { ...state, turnsLoaded: true, hydrateEpoch: wsEpoch },
          })
          return next
        })
      })
      .catch((e: unknown) => {
        if (!alive) return
        const msg = e instanceof Error ? e.message : String(e)
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          const existing = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            claude: {
              ...existing,
              turnsLoaded: true,
              hydrateEpoch: wsEpoch,
              lastError: { code: 'history_unavailable', message: msg },
            },
          })
          return next
        })
      })
    return () => {
      alive = false
    }
  }, [selectedSessionID, mode, renderer, turnsLoaded, setPerSession, wsEpoch])
}
