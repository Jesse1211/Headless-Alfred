# Multi-session Plan 9 — Frontend Sidebar component

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the left sidebar — `+ New chat` button (disabled at 8), session rows with hover-only × button, double-click rename inline input. Pure-presentation component; gets data and callbacks from `useSessions` via props. No global state, no API calls.

**Architecture:** Single `<SessionsSidebar>` component + nested `<SessionRow>`. Props mirror what `useSessions` returns. CSS in a co-located file matching the ChatGPT reference screenshot the user shared (`#212121` background, gray bubbles, white accents). All keyboard/mouse interactions handled inline.

**Tech Stack:** React 18, TypeScript. No new packages.

**Spec sections covered:** §7.1 (layout), §7.6 (confirmation dialogs are in Plan 10, not here).

---

## File Structure

```
web/src/features/sessions/
├── SessionsSidebar.tsx       # NEW
├── SessionsSidebar.css       # NEW
└── SessionsSidebar.test.tsx  # NEW
```

---

## Task 1: SessionsSidebar component + tests

**Files:**
- Create: `web/src/features/sessions/SessionsSidebar.tsx`
- Create: `web/src/features/sessions/SessionsSidebar.test.tsx`
- Create: `web/src/features/sessions/SessionsSidebar.css`

- [ ] **Step 1: Write failing tests**

Create `web/src/features/sessions/SessionsSidebar.test.tsx`:

```typescript
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { SessionsSidebar } from './SessionsSidebar'
import { Session } from '../../lib/api'

function sess(id: string, name: string): Session {
  return { id, name, created_at: '2026-06-11T00:00:00Z' }
}

const MAX = 8

describe('SessionsSidebar', () => {
  it('renders New chat button enabled when under limit', () => {
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    const btn = screen.getByRole('button', { name: /new chat/i })
    expect(btn).not.toBeDisabled()
  })

  it('disables New chat at limit', () => {
    const many: Session[] = Array.from({ length: MAX }, (_, i) => sess('S' + i, 'S' + i))
    render(
      <SessionsSidebar
        sessions={many}
        selectedSessionID="S0"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    expect(screen.getByRole('button', { name: /new chat/i })).toBeDisabled()
  })

  it('calls onCreate when New chat is clicked', () => {
    const onCreate = vi.fn()
    render(
      <SessionsSidebar
        sessions={[]}
        selectedSessionID={null}
        maxSessions={MAX}
        onCreate={onCreate}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /new chat/i }))
    expect(onCreate).toHaveBeenCalled()
  })

  it('highlights the selected session row', () => {
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A'), sess('B', 'B')]}
        selectedSessionID="B"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    const rowB = screen.getByText('B').closest('[data-testid="session-row"]')
    expect(rowB?.className).toMatch(/is-selected/)
  })

  it('calls onSelect on click', () => {
    const onSelect = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A'), sess('B', 'B')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={onSelect}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    fireEvent.click(screen.getByText('B'))
    expect(onSelect).toHaveBeenCalledWith('B')
  })

  it('double-click swaps name to an input; Enter commits and calls onRename', () => {
    const onRename = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'Session 1')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={onRename}
        onClose={() => {}}
      />,
    )
    fireEvent.doubleClick(screen.getByText('Session 1'))
    const input = screen.getByDisplayValue('Session 1') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'training' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onRename).toHaveBeenCalledWith('A', 'training')
  })

  it('Esc cancels rename without calling onRename', () => {
    const onRename = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'Session 1')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={onRename}
        onClose={() => {}}
      />,
    )
    fireEvent.doubleClick(screen.getByText('Session 1'))
    const input = screen.getByDisplayValue('Session 1')
    fireEvent.change(input, { target: { value: 'changed' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onRename).not.toHaveBeenCalled()
    // Original name still shown.
    expect(screen.getByText('Session 1')).toBeTruthy()
  })

  it('× button calls onClose with session id', () => {
    const onClose = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={onClose}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /close session/i }))
    expect(onClose).toHaveBeenCalledWith('A')
  })
})
```

- [ ] **Step 2: Run, confirm test failure**

Run: `cd web && npm test -- SessionsSidebar`
Expected: FAIL (component doesn't exist).

- [ ] **Step 3: Implement SessionsSidebar**

Create `web/src/features/sessions/SessionsSidebar.tsx`:

```typescript
import { useState, KeyboardEvent } from 'react'
import { Session } from '../../lib/api'
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
    if (e.key === 'Enter') {
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
```

- [ ] **Step 4: Add CSS**

Create `web/src/features/sessions/SessionsSidebar.css`:

```css
.sessions-sidebar {
  width: 260px;
  border-right: 1px solid var(--border);
  background: var(--surface);
  display: flex;
  flex-direction: column;
  padding: 12px 8px;
  gap: 12px;
}

.sessions-sidebar__new {
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  padding: 8px 12px;
  text-align: left;
  font-size: 13px;
  cursor: pointer;
}

.sessions-sidebar__new:hover:not(:disabled) {
  background: var(--bubble);
}

.sessions-sidebar__new:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.sessions-sidebar__header {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  letter-spacing: 0.06em;
  padding: 0 12px;
}

.sessions-sidebar__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.sessions-sidebar__empty {
  padding: 12px;
  color: var(--text-faint);
  font-size: 13px;
}

.session-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 13px;
  color: var(--text);
}

.session-row:hover {
  background: var(--bubble);
}

.session-row.is-selected {
  background: var(--bubble);
}

.session-row__name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-row__input {
  flex: 1;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  padding: 2px 6px;
  font-size: 13px;
}

.session-row__close {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 0 4px;
  font-size: 16px;
  opacity: 0;
  transition: opacity 0.15s;
}

.session-row:hover .session-row__close {
  opacity: 1;
}
```

- [ ] **Step 5: Run, confirm green**

Run: `cd web && npm test -- SessionsSidebar`
Expected: 8 PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/sessions/SessionsSidebar.tsx web/src/features/sessions/SessionsSidebar.test.tsx web/src/features/sessions/SessionsSidebar.css
git commit -m "web: SessionsSidebar component (New chat, hover-×, double-click rename)"
```

---

## Plan 9 acceptance

- `cd web && npm test -- SessionsSidebar` is green (8 tests).
- Component is pure-presentation; takes no API/global-state imports beyond `Session` from lib/api.
- × button shows only on row hover (CSS opacity transition).
- Disabled state on "New chat" appears at exactly sessions.length === maxSessions.

## Plan 9 self-review checklist

- [ ] No `useSessions` import in the sidebar; only props.
- [ ] `data-testid="session-row"` is the only test selector hack; everything else uses accessible names.
- [ ] Rename input has `autoFocus` and commits on blur for keyboard-and-mouse parity.
