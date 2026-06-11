import { useEffect, useState } from 'react'
import { useShell } from './useShell'
import HistoryList from './HistoryList'
import OutputView from './OutputView'
import CommandInput from './CommandInput'
import { getCommand, CommandFull } from '../../lib/api'
import './TerminalPage.css'

interface Props {
  token: string
  onLogout: () => void
}

export default function TerminalPage({ token, onLogout }: Props) {
  const { connState, running, idle: _idle, lastError, clearError, submit, stop, historyVersion } = useShell(token)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selected, setSelected] = useState<CommandFull | null>(null)
  const [loadingSelected, setLoadingSelected] = useState(false)

  // When a command starts, deselect history (right pane shows live output).
  useEffect(() => {
    if (running) setSelectedId(null)
  }, [running])

  // Load a history record when one is selected.
  useEffect(() => {
    let alive = true
    if (!selectedId) {
      setSelected(null)
      return
    }
    setLoadingSelected(true)
    getCommand(selectedId)
      .then((r) => {
        if (alive) {
          setSelected(r)
          setLoadingSelected(false)
        }
      })
      .catch(() => {
        if (alive) setLoadingSelected(false)
      })
    return () => {
      alive = false
    }
  }, [selectedId])

  const showingLive = running != null

  return (
    <div className="terminal-page">
      <header className="terminal-page__header">
        <div className="terminal-page__brand">Headless Alfred</div>
        <div className="terminal-page__status">
          <span className={`status-dot status-dot--${connState}`} /> {connState}
        </div>
        <button className="terminal-page__logout" onClick={onLogout}>Sign out</button>
      </header>

      {lastError && (
        <div className="terminal-page__banner is-error">
          {lastError.message || lastError.code}
          <button onClick={clearError}>×</button>
        </div>
      )}

      <div className="terminal-page__split">
        <HistoryList
          selectedId={selectedId}
          runningId={running?.id ?? null}
          onSelect={setSelectedId}
          refreshTrigger={historyVersion}
        />
        <main className="terminal-page__main">
          {showingLive && running && (
            <OutputView command={running.command} output={running.output} isLive />
          )}
          {!showingLive && selected && (
            <OutputView
              command={selected.command}
              output={selected.output}
              isLive={false}
              exitCode={selected.exit_code}
              truncated={selected.output_truncated}
            />
          )}
          {!showingLive && !selected && (
            <div className="terminal-page__empty">
              {loadingSelected ? 'Loading…' : 'Pick a command from the left, or run a new one below.'}
            </div>
          )}
          <CommandInput
            disabled={connState !== 'open'}
            busy={showingLive}
            onSubmit={submit}
            onStop={() => running && stop(running.id)}
          />
        </main>
      </div>
    </div>
  )
}
