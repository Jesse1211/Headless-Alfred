import { useShell } from './useShell'
import ChatStream from './ChatStream'
import CommandInput from './CommandInput'
import './TerminalPage.css'

interface Props {
  token: string
  onLogout: () => void
}

export default function TerminalPage({ token: _token, onLogout }: Props) {
  const { connState, running, lastError, clearError, submit, stop, messages } = useShell(_token)

  const busy = running != null

  return (
    <div className="terminal-page">
      <header className="terminal-page__header">
        <div className="terminal-page__brand">Headless Alfred</div>
        <div className="terminal-page__status" title={connState}>
          <span className={`status-dot status-dot--${connState}`} />
        </div>
        <button className="terminal-page__logout" onClick={onLogout}>Sign out</button>
      </header>

      {lastError && (
        <div className="terminal-page__banner is-error">
          {lastError.message || lastError.code}
          <button onClick={clearError} aria-label="dismiss">×</button>
        </div>
      )}

      <ChatStream messages={messages} running={running} />

      <div className="terminal-page__composer">
        <div className="terminal-page__composer-inner">
          <CommandInput
            disabled={connState !== 'open'}
            busy={busy}
            onSubmit={submit}
            onStop={() => running && stop(running.id)}
          />
        </div>
      </div>
    </div>
  )
}
