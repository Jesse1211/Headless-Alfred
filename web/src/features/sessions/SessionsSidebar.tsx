import { useState, KeyboardEvent } from 'react'
import { Session } from '../../lib/api'
import { isSubmitKey } from '../../lib/keyboard'
import './SessionsSidebar.css'

interface Props {
  sessions: Session[]
  selectedSessionID: string | null
  maxSessions: number
  onCreate: () => void
  onSelect: (id: string) => void
  onRename: (id: string, name: string) => void
  onClose: (id: string) => void
}

export function SessionsSidebar({
  sessions,
  selectedSessionID,
  maxSessions,
  onCreate,
  onSelect,
  onRename,
  onClose,
}: Props) {
  const atLimit = sessions.length >= maxSessions
  return (
    <aside className="sessions-sidebar">
      <button
        type="button"
        className="sessions-sidebar__new"
        onClick={onCreate}
        disabled={atLimit}
        title={atLimit ? 'Close one first' : 'New chat'}
      >
        + New chat
      </button>
      <div className="sessions-sidebar__header">ACTIVE SESSIONS</div>
      <ul className="sessions-sidebar__list">
        {sessions.map((s) => (
          <SessionRow
            key={s.id}
            session={s}
            selected={s.id === selectedSessionID}
            onSelect={onSelect}
            onRename={onRename}
            onClose={onClose}
          />
        ))}
        {sessions.length === 0 && (
          <li className="sessions-sidebar__empty">No sessions yet.</li>
        )}
      </ul>
    </aside>
  )
}

interface RowProps {
  session: Session
  selected: boolean
  onSelect: (id: string) => void
  onRename: (id: string, name: string) => void
  onClose: (id: string) => void
}

function SessionRow({ session, selected, onSelect, onRename, onClose }: RowProps) {
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
