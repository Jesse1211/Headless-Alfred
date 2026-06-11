import { useEffect, useState } from 'react'
import { listCommands, CommandSummary, ApiError } from '../../lib/api'

interface Props {
  selectedId: string | null
  runningId: string | null
  onSelect: (id: string) => void
  refreshTrigger: number
}

export default function HistoryList({ selectedId, runningId, onSelect, refreshTrigger }: Props) {
  const [items, setItems] = useState<CommandSummary[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    listCommands({ limit: 100 })
      .then((rows) => {
        if (alive) {
          setItems(rows)
          setError(null)
        }
      })
      .catch((e) => {
        if (alive) setError(e instanceof ApiError ? e.message : 'failed to load history')
      })
    return () => {
      alive = false
    }
  }, [refreshTrigger])

  return (
    <aside className="history-list">
      <header className="history-list__header">History</header>
      {error && <div className="history-list__error">{error}</div>}
      <ul>
        {items.map((it) => {
          const isRunning = it.id === runningId
          const isSelected = it.id === selectedId
          const interactive = !runningId
          return (
            <li
              key={it.id}
              className={`history-list__item ${isSelected ? 'is-selected' : ''} ${isRunning ? 'is-running' : ''} ${interactive ? '' : 'is-locked'}`}
              onClick={() => interactive && onSelect(it.id)}
              role="button"
              tabIndex={interactive ? 0 : -1}
            >
              <div className="history-list__cmd">{it.command || '(empty)'}</div>
              <div className="history-list__meta">
                {it.status}
                {it.exit_code != null && it.exit_code !== 0 ? ` · exit ${it.exit_code}` : ''}
              </div>
            </li>
          )
        })}
        {items.length === 0 && !error && (
          <li className="history-list__empty">No commands yet.</li>
        )}
      </ul>
    </aside>
  )
}
