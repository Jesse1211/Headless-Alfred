import { useState, KeyboardEvent } from 'react'
import { Session } from '../../lib/api'
import { isSubmitKey } from '../../lib/keyboard'
import { TurnStatus } from './sessionStatus'
import './SessionsSidebar.css'

const TURN_STATUS_LABEL: Record<TurnStatus, string> = {
  idle: 'Your turn',
  busy: 'Waiting for reply',
}

interface Props {
  sessions: Session[]
  selectedSessionID: string | null
  maxSessions: number
  onCreate: () => void
  onCreateRecap: () => void | Promise<void>
  onSelect: (id: string) => void
  onRename: (id: string, name: string) => void
  onClose: (id: string) => void
  // Footer actions. Optional so existing tests (which render the
  // sidebar in isolation) don't need to pass them. In the live app
  // they're always provided by WorkspacePage.
  onOpenGitCredentials?: () => void
  onOpenClaudeCredentials?: () => void
  onLogout?: () => void
  // Collapse the sidebar to its narrow strip. Optional so tests
  // can render the sidebar in isolation without supplying it.
  onCollapse?: () => void
  // Per-row turn indicator: 'idle' (green) / 'busy' (red) /
  // 'needsAction' (yellow). When undefined, no dot is rendered
  // (tests don't have to wire this up).
  statusForSession?: (sessionID: string) => TurnStatus
}

export function SessionsSidebar({
  sessions,
  selectedSessionID,
  maxSessions,
  onCreate,
  onCreateRecap,
  onSelect,
  onRename,
  onClose,
  onOpenGitCredentials,
  onOpenClaudeCredentials,
  onLogout,
  onCollapse,
  statusForSession,
}: Props) {
  const atLimit = sessions.length >= maxSessions
  const hasFooter = !!(onOpenGitCredentials || onOpenClaudeCredentials || onLogout)
  return (
    <aside className="sessions-sidebar">
      <div className="sessions-sidebar__top-row">
        {onCollapse && (
          <button
            type="button"
            className="sessions-sidebar__collapse"
            onClick={onCollapse}
            aria-label="Collapse sessions sidebar"
            title="Collapse sidebar"
          >
            «
          </button>
        )}
        <button
          type="button"
          className="sessions-sidebar__new"
          onClick={onCreate}
          disabled={atLimit}
          title={atLimit ? 'Close one first' : 'New chat'}
        >
          + New chat
        </button>
      </div>
      <button
        type="button"
        className="sessions-sidebar__create-recap"
        onClick={() => onCreateRecap()}
        title="Open today's recap"
      >
        Recap
      </button>
      <div className="sessions-sidebar__header">ACTIVE SESSIONS</div>
      <ul className="sessions-sidebar__list">
        {sessions.map((s) => (
          <SessionRow
            key={s.id}
            session={s}
            selected={s.id === selectedSessionID}
            status={statusForSession?.(s.id)}
            onSelect={onSelect}
            onRename={onRename}
            onClose={onClose}
          />
        ))}
        {sessions.length === 0 && (
          <li className="sessions-sidebar__empty">No sessions yet.</li>
        )}
      </ul>
      {hasFooter && (
        <div className="sessions-sidebar__footer">
          {onOpenGitCredentials && (
            <button
              type="button"
              className="sessions-sidebar__footer-btn"
              onClick={onOpenGitCredentials}
            >
              Git credentials
            </button>
          )}
          {onOpenClaudeCredentials && (
            <button
              type="button"
              className="sessions-sidebar__footer-btn"
              onClick={onOpenClaudeCredentials}
            >
              Claude credentials
            </button>
          )}
          {onLogout && (
            <button
              type="button"
              className="sessions-sidebar__footer-btn sessions-sidebar__footer-btn--signout"
              onClick={onLogout}
            >
              Sign out
            </button>
          )}
        </div>
      )}
    </aside>
  )
}

interface RowProps {
  session: Session
  selected: boolean
  status?: TurnStatus
  onSelect: (id: string) => void
  onRename: (id: string, name: string) => void
  onClose: (id: string) => void
}

function SessionRow({ session, selected, status, onSelect, onRename, onClose }: RowProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(session.name)

  function commit() {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== session.name) {
      onRename(session.id, trimmed)
    }
    setEditing(false)
  }

  function onKey(e: KeyboardEvent<HTMLInputElement>) {
    if (isSubmitKey(e)) {
      e.preventDefault()
      commit()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setDraft(session.name)
      setEditing(false)
    }
  }

  return (
    <li
      data-testid="session-row"
      className={`session-row ${selected ? 'is-selected' : ''}`}
      onClick={() => !editing && onSelect(session.id)}
    >
      {status !== undefined && (
        <span
          className={`turn-dot turn-dot--${status}`}
          title={TURN_STATUS_LABEL[status]}
          aria-label={TURN_STATUS_LABEL[status]}
        />
      )}
      {editing ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={onKey}
          className="session-row__input"
        />
      ) : (
        <span
          className="session-row__name"
          onDoubleClick={(e) => {
            e.stopPropagation()
            setDraft(session.name)
            setEditing(true)
          }}
        >
          {session.name}
        </span>
      )}
      <button
        type="button"
        aria-label="Close session"
        className="session-row__close"
        onClick={(e) => {
          e.stopPropagation()
          onClose(session.id)
        }}
      >
        ×
      </button>
    </li>
  )
}
