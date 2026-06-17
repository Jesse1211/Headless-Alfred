import { useState, KeyboardEvent } from 'react'
import { Session } from '../../lib/api'
import { isSubmitKey } from '../../lib/keyboard'
import { SessionIndicator } from './sessionStatus'
import { SessionIndicatorDot } from './SessionIndicatorDot'
import type { ClaudeState } from './types'
import './SessionsSidebar.css'

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
  onOpenClaudeVersion?: () => void
  onLogout?: () => void
  // Collapse the sidebar to its narrow strip. Optional so tests
  // can render the sidebar in isolation without supplying it.
  onCollapse?: () => void
  // Per-row indicator: 'idle' (green) / 'busy' (red) /
  // 'disconnected' (warning glyph). When undefined, no dot is
  // rendered (tests don't have to wire this up).
  statusForSession?: (sessionID: string) => SessionIndicator
  // Per-row Claude state lookup for the active-task pill. When
  // undefined or returning undefined, the pill is hidden (tests
  // don't need to wire this up).
  claudeForSession?: (sessionID: string) => ClaudeState | undefined
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
  onOpenClaudeVersion,
  onLogout,
  onCollapse,
  statusForSession,
  claudeForSession,
}: Props) {
  const atLimit = sessions.length >= maxSessions
  const hasFooter = !!(onOpenGitCredentials || onOpenClaudeCredentials || onOpenClaudeVersion || onLogout)
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
            claude={claudeForSession?.(s.id)}
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
          {onOpenClaudeVersion && (
            <button
              type="button"
              className="sessions-sidebar__footer-btn"
              onClick={onOpenClaudeVersion}
            >
              Claude version
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
  status?: SessionIndicator
  claude?: ClaudeState
  onSelect: (id: string) => void
  onRename: (id: string, name: string) => void
  onClose: (id: string) => void
}

function SessionRow({ session, selected, status, claude, onSelect, onRename, onClose }: RowProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(session.name)

  const activeBg = claude
    ? Object.values(claude.bgTasks).filter((t) => t.status === 'in_progress').length
    : 0
  const activeSubagents = claude
    ? Object.values(claude.subagents).filter((s) => !s.finishedAt).length
    : 0
  const activeCount = activeBg + activeSubagents

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
        <SessionIndicatorDot status={status} />
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
      {activeCount > 0 && (
        <span className="session-pill" title={`${activeBg} Monitor task${activeBg === 1 ? '' : 's'}, ${activeSubagents} subagent${activeSubagents === 1 ? '' : 's'}`}>
          ⏳ {activeCount}
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
