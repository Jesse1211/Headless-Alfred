import { useEffect } from 'react'
import { listCommands, getCommand } from '../../lib/api'
import { CompletedMsg, PerSessionState, emptyPerSessionState } from './types'

interface Args {
  selectedSessionID: string | null
  perSession: Map<string, PerSessionState>
  setPerSession: (updater: (prev: Map<string, PerSessionState>) => Map<string, PerSessionState>) => void
}

// useSessionHistoryLoader observes selectedSessionID changes and loads
// command history for that session if not yet loaded. Fire-and-forget.
export function useSessionHistoryLoader({ selectedSessionID, perSession, setPerSession }: Args) {
  useEffect(() => {
    if (!selectedSessionID) return
    const cur = perSession.get(selectedSessionID) ?? emptyPerSessionState()
    if (cur.messagesLoaded) return

    let alive = true
    listCommands(selectedSessionID, { limit: 50 })
      .then(async (rows) => {
        const sorted = [...rows].sort((a, b) => a.started_at.localeCompare(b.started_at))
        const full = await Promise.all(
          sorted.map((r) => getCommand(selectedSessionID, r.id).catch(() => null)),
        )
        if (!alive) return
        const msgs: CompletedMsg[] = []
        for (let i = 0; i < sorted.length; i++) {
          const f = full[i]
          if (!f) continue
          msgs.push({
            id: f.id,
            command: f.command,
            output: f.output,
            startedAt: f.started_at,
            finishedAt: f.finished_at,
            exitCode: f.exit_code,
            status: f.status,
            truncated: f.output_truncated,
          })
        }
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur2 = next.get(selectedSessionID) ?? emptyPerSessionState()
          next.set(selectedSessionID, { ...cur2, messages: msgs, messagesLoaded: true })
          return next
        })
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [selectedSessionID, perSession, setPerSession])
}
