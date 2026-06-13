import { useState } from 'react'
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
import { SessionsSidebar } from './SessionsSidebar'
import { ConfirmDialog } from './ConfirmDialog'
import { GitCredentialsDialog } from './GitCredentialsDialog'
import ChatStream from '../terminal/ChatStream'
import CommandInput from '../terminal/CommandInput'
import { emptyPerSessionState } from './types'
import '../terminal/TerminalPage.css'
import './WorkspacePage.css'

const MAX_SESSIONS = 8

interface Props {
  token: string
  onLogout: () => void
}

export function WorkspacePage({ token, onLogout }: Props) {
  const s = useSessions(token)
  useSessionHistoryLoader({
    selectedSessionID: s.selectedSessionID,
    perSession: s.perSession,
    setPerSession: s.setPerSession,
  })

  const [pendingClose, setPendingClose] = useState<string | null>(null)
  const [gitCredsOpen, setGitCredsOpen] = useState(false)

  const selected = s.selectedSessionID
    ? s.sessions.find((x) => x.id === s.selectedSessionID)
    : null
  const ps = s.selectedSessionID
    ? s.perSession.get(s.selectedSessionID) ?? emptyPerSessionState()
    : null
  const composerDisabled = s.connState !== 'open' || !s.selectedSessionID
  const composerBusy = !!ps?.running

  return (
    <div className="workspace">
      <SessionsSidebar
        sessions={s.sessions}
        selectedSessionID={s.selectedSessionID}
        maxSessions={MAX_SESSIONS}
        onCreate={() => s.createSession()}
        onSelect={s.selectSession}
        onRename={(id, name) => s.renameSession(id, name)}
        onClose={(id) => setPendingClose(id)}
      />

      <div className="workspace__main">
        <header className="workspace__header">
          <div className="workspace__brand">{selected?.name ?? 'Headless Alfred'}</div>
          <div className="workspace__status" title={s.connState}>
            <span className={`status-dot status-dot--${s.connState}`} />
          </div>
          <button
            type="button"
            className="workspace__icon-btn"
            aria-label="Git credentials"
            title="Git credentials"
            onClick={() => setGitCredsOpen(true)}
          >
            {/* Lightweight gear glyph; no svg dep */}
            ⚙
          </button>
          <button className="workspace__logout" onClick={onLogout}>Sign out</button>
        </header>

        {s.lastError && (
          <div className="workspace__banner is-error">
            {s.lastError.message || s.lastError.code}
            <button onClick={s.clearError} aria-label="dismiss">×</button>
          </div>
        )}

        {!selected && (
          <div className="workspace__empty">
            <h1>Headless Alfred</h1>
            <p>Create a session to begin.</p>
            <button onClick={() => s.createSession()}>+ New chat</button>
          </div>
        )}

        {selected && ps && (
          <>
            <ChatStream messages={ps.messages} running={ps.running} />
            <div className="workspace__composer">
              <div className="workspace__composer-inner">
                <CommandInput
                  disabled={composerDisabled}
                  busy={composerBusy}
                  onSubmit={(cmd) => s.submit(cmd)}
                  onStop={() => ps.running && s.stop(ps.running.id)}
                />
              </div>
            </div>
          </>
        )}
      </div>

      {gitCredsOpen && (
        <GitCredentialsDialog onClose={() => setGitCredsOpen(false)} />
      )}

      {pendingClose && (
        <ConfirmDialog
          title="Close session?"
          body={
            (() => {
              const target = s.sessions.find((x) => x.id === pendingClose)
              const psTarget = s.perSession.get(pendingClose) ?? emptyPerSessionState()
              const running = psTarget.running
              const count = psTarget.messages.length
              const name = target?.name ?? 'this session'
              return running
                ? `Close '${name}'? 1 command is still running and will be terminated. The session and ${count} commands of history will be permanently deleted.`
                : `Close '${name}'? The session and ${count} commands of history will be permanently deleted.`
            })()
          }
          confirmLabel="Delete"
          onConfirm={() => {
            const id = pendingClose
            setPendingClose(null)
            s.closeSession(id)
          }}
          onCancel={() => setPendingClose(null)}
        />
      )}
    </div>
  )
}
