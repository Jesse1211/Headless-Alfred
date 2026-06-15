import { useEffect } from 'react'
import { getClaudeHistory } from '../../lib/api'
import { PerSessionState, emptyClaudeState, emptyPerSessionState } from './types'

interface Args {
  selectedSessionID: string | null
  perSession: Map<string, PerSessionState>
  setPerSession: (updater: (prev: Map<string, PerSessionState>) => Map<string, PerSessionState>) => void
}

// useClaudeHistoryLoader observes the selected session and, when it
// enters Claude UI mode for the first time in this page lifecycle,
// fetches the rebuilt chat history from the backend jsonl-restore
// endpoint and seeds claude.turns. Idempotent — guarded by
// claude.turnsLoaded so it fires at most once per session per page
// load.
//
// Errors are absorbed: the flag is still flipped to true so we don't
// retry-loop, and lastError carries the reason. The existing
// ClaudeChatView already renders state.lastError as a banner so no
// UI change is needed.
export function useClaudeHistoryLoader({ selectedSessionID, perSession, setPerSession }: Args) {
  // Snapshot the fields the effect reads so dependency tracking is
  // straightforward without re-running on unrelated state changes.
  const ps = selectedSessionID ? perSession.get(selectedSessionID) : undefined
  const mode = ps?.mode
  const renderer = ps?.renderer
  const turnsLoaded = ps?.claude?.turnsLoaded === true

  useEffect(() => {
    if (!selectedSessionID) return
    if (mode !== 'claude' || renderer !== 'ui') return
    if (turnsLoaded) return

    let alive = true
    getClaudeHistory(selectedSessionID)
      .then((turns) => {
        if (!alive) return
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          const c = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            claude: { ...c, turns, turnsLoaded: true, lastError: undefined },
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
          const c = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            // Don't clobber existing turns on error.
            claude: { ...c, turnsLoaded: true, lastError: { code: 'history_unavailable', message: msg } },
          })
          return next
        })
      })
    return () => { alive = false }
  }, [selectedSessionID, mode, renderer, turnsLoaded, setPerSession])
}
