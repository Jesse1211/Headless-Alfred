# Multi-session Plan 10 — Frontend integration (wire Sidebar + useSessions + empty state + confirm dialog)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `TerminalPage` with `WorkspacePage` that holds `SessionsSidebar` on the left and the existing `ChatStream` + composer on the right. Wire confirm-before-delete. Wire on-mount per-session command history fetch. Render empty state when no sessions exist.

**Architecture:** `WorkspacePage` uses `useSessions(token)`, owns the modal state for the delete confirmation. `ChatStream` continues to render the **selected** session's running + messages. `CommandInput` continues to call `submit` from the hook — but it now sends a sessionID under the hood. Lazy loading: on selection change, if `perSession.get(sid).messagesLoaded === false`, fetch history.

**Tech Stack:** React 18, TypeScript. No new packages.

**Spec sections covered:** §7.1 (whole layout), §7.5 (no-sessions empty state), §7.6 (confirmation), §7.7 (composer draft per tab).

---

## File Structure

```
web/src/
├── App.tsx                        # MODIFY: render WorkspacePage instead of TerminalPage
├── features/sessions/
│   ├── WorkspacePage.tsx          # NEW (replaces TerminalPage)
│   ├── WorkspacePage.css          # NEW
│   ├── ConfirmDialog.tsx          # NEW
│   ├── ConfirmDialog.css          # NEW
│   └── useSessionHistoryLoader.ts # NEW — small effect hook for lazy history fetch
└── features/terminal/
    ├── TerminalPage.tsx           # DELETE
    ├── TerminalPage.css           # MOVE/RENAME (we reuse the chat styles)
    ├── ChatStream.tsx             # KEEP but adjust props to take per-session messages
    └── CommandInput.tsx           # KEEP unchanged
```

---

## Task 1: ConfirmDialog

**Files:**
- Create: `web/src/features/sessions/ConfirmDialog.tsx`
- Create: `web/src/features/sessions/ConfirmDialog.css`
- Create: `web/src/features/sessions/ConfirmDialog.test.tsx`

- [ ] **Step 1: Write the failing tests**

Create `web/src/features/sessions/ConfirmDialog.test.tsx`:

```typescript
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

describe('ConfirmDialog', () => {
  it('renders the title and body', () => {
    render(<ConfirmDialog title="Delete?" body="Are you sure?" confirmLabel="Delete" onConfirm={() => {}} onCancel={() => {}} />)
    expect(screen.getByText('Delete?')).toBeTruthy()
    expect(screen.getByText('Are you sure?')).toBeTruthy()
  })

  it('calls onConfirm when confirm clicked', () => {
    const onConfirm = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={onConfirm} onCancel={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(onConfirm).toHaveBeenCalled()
  })

  it('calls onCancel when cancel clicked', () => {
    const onCancel = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })

  it('Esc triggers onCancel', () => {
    const onCancel = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Implement ConfirmDialog**

Create `web/src/features/sessions/ConfirmDialog.tsx`:

```typescript
import { useEffect } from 'react'
import './ConfirmDialog.css'

interface Props {
  title: string
  body: string
  confirmLabel: string
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({ title, body, confirmLabel, onConfirm, onCancel }: Props) {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  return (
    <div className="confirm-dialog__backdrop" onClick={onCancel}>
      <div className="confirm-dialog" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <h2 className="confirm-dialog__title">{title}</h2>
        <p className="confirm-dialog__body">{body}</p>
        <div className="confirm-dialog__actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" className="confirm-dialog__danger" onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
```

Create `web/src/features/sessions/ConfirmDialog.css`:

```css
.confirm-dialog__backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: grid;
  place-items: center;
  z-index: 50;
}

.confirm-dialog {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 24px;
  width: min(420px, 90vw);
  color: var(--text);
}

.confirm-dialog__title {
  font-size: 16px;
  font-weight: 600;
  margin: 0 0 12px;
}

.confirm-dialog__body {
  font-size: 14px;
  margin: 0 0 20px;
  line-height: 1.5;
}

.confirm-dialog__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.confirm-dialog__actions button {
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  padding: 6px 12px;
  font-size: 13px;
  cursor: pointer;
}

.confirm-dialog__danger {
  background: var(--error) !important;
  color: var(--accent-fg) !important;
  border-color: var(--error) !important;
}
```

- [ ] **Step 3: Run, confirm green**

Run: `cd web && npm test -- ConfirmDialog`
Expected: 4 PASS.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/ConfirmDialog.tsx web/src/features/sessions/ConfirmDialog.css web/src/features/sessions/ConfirmDialog.test.tsx
git commit -m "web: ConfirmDialog component for destructive-action confirmations"
```

---

## Task 2: Per-session history lazy loader hook

A tiny hook that, when `selectedSessionID` changes, ensures the
selected session's `messages` are loaded from REST. Already-loaded
sessions are not re-fetched.

**Files:**
- Create: `web/src/features/sessions/useSessionHistoryLoader.ts`

- [ ] **Step 1: Implement**

```typescript
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
```

- [ ] **Step 2: Expose setPerSession from useSessions**

In `useSessions.ts`, replace the return statement to also expose `setPerSession`:

```typescript
return {
  connState, sessions, selectedSessionID, selectSession, perSession, setPerSession,
  submit, stop, createSession, renameSession, closeSession,
  lastError, clearError,
}
```

- [ ] **Step 3: Commit**

```bash
git add web/src/features/sessions/useSessionHistoryLoader.ts web/src/features/sessions/useSessions.ts
git commit -m "web: useSessionHistoryLoader — lazy fetch on selection change"
```

---

## Task 3: WorkspacePage — assemble Sidebar + ChatStream + composer + dialog

**Files:**
- Create: `web/src/features/sessions/WorkspacePage.tsx`
- Create: `web/src/features/sessions/WorkspacePage.css`
- Modify: `web/src/App.tsx`

- [ ] **Step 1: Implement**

Create `web/src/features/sessions/WorkspacePage.tsx`:

```typescript
import { useState } from 'react'
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
import { SessionsSidebar } from './SessionsSidebar'
import { ConfirmDialog } from './ConfirmDialog'
import ChatStream from '../terminal/ChatStream'
import CommandInput from '../terminal/CommandInput'
import { emptyPerSessionState } from './types'
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
```

Create `web/src/features/sessions/WorkspacePage.css`:

```css
.workspace {
  display: grid;
  grid-template-columns: 260px 1fr;
  height: 100vh;
  background: var(--bg);
}

.workspace__main {
  display: grid;
  grid-template-rows: auto auto 1fr auto;
  min-height: 0;
}

.workspace__header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 20px;
  border-bottom: 1px solid var(--border);
}

.workspace__brand {
  font-weight: 600;
  font-size: 14px;
}

.workspace__status {
  margin-left: auto;
  display: flex;
  align-items: center;
}

.workspace__logout {
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 5px 10px;
  color: var(--text-muted);
  font-size: 12px;
}

.workspace__banner {
  padding: 8px 20px;
  font-size: 13px;
  display: flex;
  gap: 12px;
}

.workspace__banner.is-error {
  background: var(--error-bg);
  color: var(--error);
}

.workspace__empty {
  display: grid;
  place-items: center;
  text-align: center;
  color: var(--text-muted);
  padding: 80px 24px;
}

.workspace__empty h1 {
  font-size: 24px;
  margin: 0 0 12px;
  color: var(--text);
}

.workspace__empty button {
  margin-top: 16px;
  background: var(--accent);
  color: var(--accent-fg);
  border: none;
  border-radius: 8px;
  padding: 8px 14px;
  font-size: 14px;
  cursor: pointer;
}

.workspace__composer {
  background: var(--bg);
  padding: 8px 24px 16px;
}

.workspace__composer-inner {
  max-width: 768px;
  margin: 0 auto;
}
```

- [ ] **Step 2: Update App.tsx**

Replace `web/src/App.tsx`:

```typescript
import { useAuth } from './features/auth/useAuth'
import LoginPage from './features/auth/LoginPage'
import { WorkspacePage } from './features/sessions/WorkspacePage'

export default function App() {
  const { token, isAuthenticated, login, logout } = useAuth()
  if (!isAuthenticated) {
    return <LoginPage onLogin={login} />
  }
  return <WorkspacePage token={token} onLogout={logout} />
}
```

- [ ] **Step 3: Delete TerminalPage**

```bash
rm web/src/features/terminal/TerminalPage.tsx
# Keep TerminalPage.css temporarily if ChatStream + CommandInput
# import from it; otherwise delete:
# rm web/src/features/terminal/TerminalPage.css
```

If `ChatStream.tsx` imports from `TerminalPage.css`, leave the CSS file alone.

- [ ] **Step 4: Run all web tests**

Run: `cd web && npm test`
Expected: all green (auth, useSessions, SessionsSidebar, ConfirmDialog).

Run: `cd web && npm run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/WorkspacePage.tsx web/src/features/sessions/WorkspacePage.css web/src/App.tsx
git rm web/src/features/terminal/TerminalPage.tsx
git commit -m "web: WorkspacePage assembles Sidebar + ChatStream + composer + delete confirm dialog"
```

---

## Plan 10 acceptance

- `cd web && npm test` is green.
- `cd web && npm run build` is green.
- Empty-state shown when no sessions; "New chat" creates and auto-selects.
- Deleting a session pops the ConfirmDialog with body text reflecting whether a command is in flight + the history count.

## Plan 10 self-review checklist

- [ ] No references to the deleted `TerminalPage` in any file.
- [ ] `selectedSessionID === null` does NOT crash — `ps` guard works.
- [ ] Composer is disabled when there is no selected session.
