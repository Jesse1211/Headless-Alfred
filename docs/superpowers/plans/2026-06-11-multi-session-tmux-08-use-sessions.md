# Multi-session Plan 8 — Frontend useSessions hook

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `useShell` with `useSessions`, which holds a per-session running state map and reacts to the new WS protocol from Plan 6. Update `lib/ws.ts` types. Update `lib/api.ts` with the new session endpoints. The old `useShell` is deleted.

**Architecture:** A single hook that exposes `{ connState, sessions, selectedSessionID, selectSession, perSession, submit, stop, createSession, renameSession, closeSession, lastError, clearError }`. State held: `sessions: Session[]` (REST), `selectedSessionID: string | null` (localStorage), `perSession: Map<string, { running, messages, messagesLoaded }>`. One ShellSocket open for the whole hook lifetime; all chunks routed by sessionID. StrictMode-safe (idempotent updaters, ref mirrors).

**Tech Stack:** React 18, TypeScript, Vitest. No new packages.

**Spec sections covered:** §7.2 (state model), §7.3 (selection persistence), §7.4 (cross-tab consistency).

---

## File Structure

```
web/src/
├── features/
│   ├── sessions/                       # NEW directory
│   │   ├── useSessions.ts              # NEW
│   │   ├── useSessions.test.ts         # NEW
│   │   └── types.ts                    # NEW (Session, PerSessionState)
│   └── terminal/
│       ├── useShell.ts                 # DELETE (replaced)
│       ├── useShell.test.ts            # DELETE
│       └── types.ts                    # MODIFY: drop CompletedMsg (move to sessions/types)
├── lib/
│   ├── ws.ts                           # MODIFY: messages now carry sessionID
│   └── api.ts                          # MODIFY: new session endpoints + per-session command paths
```

---

## Task 1: Update lib/ws.ts protocol types

**Files:**
- Modify: `web/src/lib/ws.ts`

- [ ] **Step 1: Replace the ServerMsg and ClientMsg unions**

In `web/src/lib/ws.ts`, replace the type definitions:

```typescript
export type ServerMsg =
  | { type: 'reattach'; sessionID: string; cmdId: string; command: string; startedAt: string; outputSoFar: string }
  | { type: 'idle'; sessionID: string }
  | { type: 'started'; sessionID: string; cmdId: string; command: string; startedAt: string }
  | { type: 'chunk'; sessionID: string; cmdId: string; data: string }
  | { type: 'done'; sessionID: string; cmdId: string; exitCode: number; finishedAt: string }
  | { type: 'session_closed'; sessionID: string }
  | { type: 'session_renamed'; sessionID: string; name: string }
  | { type: 'error'; sessionID?: string; code: string; message: string }
  | { type: 'pong' }

export type ClientMsg =
  | { type: 'run'; sessionID: string; command: string }
  | { type: 'ping' }
```

- [ ] **Step 2: Confirm web compiles**

Run: `cd web && npx tsc --noEmit -p tsconfig.json`
Expected: FAIL — `useShell.ts` references the old union shape. That's fine; Tasks 4-5 delete useShell.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/ws.ts
git commit -m "web: WS protocol types carry sessionID; add session_closed/renamed"
```

---

## Task 2: Update lib/api.ts with session endpoints

**Files:**
- Modify: `web/src/lib/api.ts`

- [ ] **Step 1: Add session endpoints + adjust command paths**

Append to `web/src/lib/api.ts` (and remove the old `/api/commands*` helpers):

```typescript
export interface Session {
  id: string
  name: string
  created_at: string
}

export async function listSessions(): Promise<Session[]> {
  const res = await request('/api/sessions')
  return res.json()
}

export async function createSession(name?: string): Promise<Session> {
  const body = name && name.trim() ? JSON.stringify({ name }) : undefined
  const res = await request('/api/sessions', {
    method: 'POST',
    body,
  })
  return res.json()
}

export async function renameSession(id: string, name: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify({ name }),
  })
}

export async function deleteSession(id: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// Existing command helpers, scoped under a session.
export async function listCommands(
  sessionID: string,
  opts: { limit?: number; before?: string } = {},
): Promise<CommandSummary[]> {
  const qs = new URLSearchParams()
  if (opts.limit != null) qs.set('limit', String(opts.limit))
  if (opts.before) qs.set('before', opts.before)
  const res = await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands${qs.size ? '?' + qs.toString() : ''}`,
  )
  return res.json()
}

export async function getCommand(sessionID: string, id: string): Promise<CommandFull> {
  const res = await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands/${encodeURIComponent(id)}`,
  )
  return res.json()
}

export async function stopCommand(sessionID: string, id: string): Promise<void> {
  await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands/${encodeURIComponent(id)}/stop`,
    { method: 'POST' },
  )
}
```

Delete the old non-session-scoped `listCommands` / `getCommand` /
`stopCommand` functions if they still exist in the file.

- [ ] **Step 2: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "web: api.ts adds session endpoints + scopes command paths under /api/sessions/{sid}"
```

---

## Task 3: Sessions feature directory + types

**Files:**
- Create: `web/src/features/sessions/types.ts`

- [ ] **Step 1: Define the per-session state types**

Create `web/src/features/sessions/types.ts`:

```typescript
export interface RunningCmd {
  id: string
  command: string
  startedAt: string
  output: string
  truncatedLossWarned: boolean
}

export interface CompletedMsg {
  id: string
  command: string
  output: string
  startedAt: string
  finishedAt?: string
  exitCode?: number
  status: 'completed' | 'interrupted' | 'stopped' | 'running'
  truncated: boolean
}

export interface PerSessionState {
  running: RunningCmd | null
  messages: CompletedMsg[]
  messagesLoaded: boolean
}

export function emptyPerSessionState(): PerSessionState {
  return { running: null, messages: [], messagesLoaded: false }
}
```

- [ ] **Step 2: Commit**

```bash
git add web/src/features/sessions/types.ts
git commit -m "web: sessions feature types (RunningCmd, CompletedMsg, PerSessionState)"
```

---

## Task 4: useSessions hook — initial sessions + selection from localStorage

The first feature slice: hook construction, fetch sessions, pick
selectedSessionID from localStorage (fallback to first/null).

**Files:**
- Create: `web/src/features/sessions/useSessions.ts`
- Create: `web/src/features/sessions/useSessions.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `web/src/features/sessions/useSessions.test.ts`:

```typescript
import { renderHook, waitFor, act } from '@testing-library/react'
import { useSessions } from './useSessions'
import { describe, it, expect, beforeEach, vi } from 'vitest'

// Mock the lib/api module so we don't make real HTTP calls.
vi.mock('../../lib/api', () => {
  return {
    listSessions: vi.fn(() => Promise.resolve([
      { id: 'sess-A', name: 'A', created_at: '2026-06-11T00:00:00Z' },
      { id: 'sess-B', name: 'B', created_at: '2026-06-11T00:01:00Z' },
    ])),
    listCommands: vi.fn(() => Promise.resolve([])),
    getCommand: vi.fn(),
    createSession: vi.fn((n) =>
      Promise.resolve({ id: 'sess-NEW', name: n || 'Session 3', created_at: 'now' }),
    ),
    renameSession: vi.fn(() => Promise.resolve()),
    deleteSession: vi.fn(() => Promise.resolve()),
    stopCommand: vi.fn(),
  }
})

// Mock the ShellSocket. The hook never actually opens a real socket.
let _onMessage: ((m: any) => void) | null = null
const sendMock = vi.fn()
vi.mock('../../lib/ws', () => {
  return {
    ShellSocket: vi.fn().mockImplementation((opts: any) => {
      _onMessage = opts.onMessage
      opts.onState('open')
      return { start: vi.fn(), stop: vi.fn(), send: sendMock }
    }),
  }
})

beforeEach(() => {
  localStorage.clear()
  sendMock.mockClear()
  _onMessage = null
})

describe('useSessions — initial load', () => {
  it('loads sessions from the API on mount', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    expect(result.current.sessions[0].id).toBe('sess-A')
  })

  it('selects the first session when nothing in localStorage', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
  })

  it('rehydrates selectedSessionID from localStorage if it still exists', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-B')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-B'))
  })

  it('falls back to first session if stored ID is unknown', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-GHOST')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
  })

  it('selectSession persists to localStorage', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => result.current.selectSession('sess-B'))
    expect(result.current.selectedSessionID).toBe('sess-B')
    expect(localStorage.getItem('alfred_selected_session')).toBe('sess-B')
  })
})
```

- [ ] **Step 2: Run, confirm test failure**

Run: `cd web && npm test -- useSessions`
Expected: FAIL (no implementation yet).

- [ ] **Step 3: Implement hook skeleton**

Create `web/src/features/sessions/useSessions.ts`:

```typescript
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import {
  Session,
  listSessions,
  listCommands,
  getCommand,
  createSession as apiCreateSession,
  renameSession as apiRenameSession,
  deleteSession as apiDeleteSession,
  stopCommand as apiStopCommand,
} from '../../lib/api'
import { PerSessionState, emptyPerSessionState, CompletedMsg, RunningCmd } from './types'

const STORAGE_KEY = 'alfred_selected_session'

function b64decode(s: string): string {
  if (typeof atob === 'function') {
    try {
      const bin = atob(s)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      return new TextDecoder().decode(bytes)
    } catch {
      return ''
    }
  }
  return Buffer.from(s, 'base64').toString('utf8')
}

export function useSessions(token: string) {
  const [connState, setConnState] = useState<ConnState>('connecting')
  const [sessions, setSessions] = useState<Session[]>([])
  const [selectedSessionID, setSelectedSessionID] = useState<string | null>(null)
  const [perSession, setPerSession] = useState<Map<string, PerSessionState>>(new Map())
  const [lastError, setLastError] = useState<{ code: string; message: string } | null>(null)

  const tokenRef = useRef(token)
  tokenRef.current = token
  const perSessionRef = useRef(perSession)
  perSessionRef.current = perSession
  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions

  // Initial REST fetch.
  useEffect(() => {
    let alive = true
    listSessions()
      .then((list) => {
        if (!alive) return
        setSessions(list)
        // Rehydrate selection.
        const stored = localStorage.getItem(STORAGE_KEY)
        let pick = list.find((s) => s.id === stored)
        if (!pick) pick = list[0]
        setSelectedSessionID(pick?.id ?? null)
        if (pick) localStorage.setItem(STORAGE_KEY, pick.id)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [])

  const selectSession = useCallback((id: string) => {
    setSelectedSessionID(id)
    localStorage.setItem(STORAGE_KEY, id)
  }, [])

  // WS plumbed in Task 5; this skeleton just opens a no-op socket.
  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (_m: ServerMsg) => {
          // Task 5 fills this in.
        },
      }),
    [],
  )

  useEffect(() => {
    socket.start()
    return () => socket.stop()
  }, [socket])

  const clearError = useCallback(() => setLastError(null), [])

  return {
    connState,
    sessions,
    selectedSessionID,
    selectSession,
    perSession,
    lastError,
    clearError,
    // Placeholders so consumers compile during Task 5:
    submit: (_cmd: string) => {},
    stop: (_cmdID: string) => {},
    createSession: async (_name?: string) => null as Session | null,
    renameSession: async (_id: string, _name: string) => {},
    closeSession: async (_id: string) => {},
  }
}

// Re-export types so consumers can import from one place.
export type { Session, PerSessionState, CompletedMsg, RunningCmd }
```

- [ ] **Step 4: Run, confirm tests pass**

Run: `cd web && npm test -- useSessions`
Expected: 5 PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/useSessions.ts web/src/features/sessions/useSessions.test.ts
git commit -m "web: useSessions hook — initial fetch + localStorage selection rehydration"
```

---

## Task 5: Wire WS handlers, CRUD callbacks, per-session running state

**Files:**
- Modify: `web/src/features/sessions/useSessions.ts`
- Modify: `web/src/features/sessions/useSessions.test.ts`

- [ ] **Step 1: Write more failing tests**

Append to `useSessions.test.ts`:

```typescript
function b64(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64')
}

describe('useSessions — WS events', () => {
  it('idle for session sets perSession state to empty running', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'idle', sessionID: 'sess-A' }))
    const ps = result.current.perSession.get('sess-A')
    expect(ps?.running).toBeNull()
  })

  it('started/chunk/done update running and append to messages', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() =>
      _onMessage!({
        type: 'started',
        sessionID: 'sess-A',
        cmdId: 'X',
        command: 'ls',
        startedAt: 'now',
      }),
    )
    expect(result.current.perSession.get('sess-A')?.running?.id).toBe('X')

    act(() => _onMessage!({ type: 'chunk', sessionID: 'sess-A', cmdId: 'X', data: b64('hello\n') }))
    expect(result.current.perSession.get('sess-A')?.running?.output).toBe('hello\n')

    act(() =>
      _onMessage!({
        type: 'done',
        sessionID: 'sess-A',
        cmdId: 'X',
        exitCode: 0,
        finishedAt: 'fin',
      }),
    )
    const psA = result.current.perSession.get('sess-A')!
    expect(psA.running).toBeNull()
    expect(psA.messages.length).toBe(1)
    expect(psA.messages[0].id).toBe('X')
  })

  it('chunks for one session do not disturb another sessions running state', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'started', sessionID: 'sess-A', cmdId: 'A1', command: 'a', startedAt: 't' }))
    act(() => _onMessage!({ type: 'started', sessionID: 'sess-B', cmdId: 'B1', command: 'b', startedAt: 't' }))
    act(() => _onMessage!({ type: 'chunk', sessionID: 'sess-A', cmdId: 'A1', data: b64('A-data') }))
    expect(result.current.perSession.get('sess-A')?.running?.output).toBe('A-data')
    expect(result.current.perSession.get('sess-B')?.running?.output).toBe('')
  })

  it('session_closed removes session and clears perSession', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'session_closed', sessionID: 'sess-A' }))
    expect(result.current.sessions.find((s) => s.id === 'sess-A')).toBeUndefined()
    expect(result.current.perSession.get('sess-A')).toBeUndefined()
  })

  it('session_closed reassigns selectedSessionID if it pointed at the closed one', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-A')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
    act(() => _onMessage!({ type: 'session_closed', sessionID: 'sess-A' }))
    expect(result.current.selectedSessionID).toBe('sess-B')
  })

  it('session_renamed updates the name', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'session_renamed', sessionID: 'sess-A', name: 'training' }))
    expect(result.current.sessions.find((s) => s.id === 'sess-A')?.name).toBe('training')
  })

  it('submit sends run with sessionID', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => result.current.selectSession('sess-B'))
    act(() => result.current.submit('ls -la'))
    expect(sendMock).toHaveBeenCalledWith({ type: 'run', sessionID: 'sess-B', command: 'ls -la' })
  })
})
```

- [ ] **Step 2: Replace useSessions.ts onMessage + add CRUD callbacks**

Replace the inner onMessage and the placeholder callbacks in
`useSessions.ts`:

```typescript
  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (m: ServerMsg) => {
          switch (m.type) {
            case 'idle':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, { ...(next.get(m.sessionID) ?? emptyPerSessionState()), running: null })
                return next
              })
              break
            case 'reattach':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...(next.get(m.sessionID) ?? emptyPerSessionState()),
                  running: {
                    id: m.cmdId,
                    command: m.command,
                    startedAt: m.startedAt,
                    output: b64decode(m.outputSoFar),
                    truncatedLossWarned: false,
                  },
                })
                return next
              })
              break
            case 'started':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...(next.get(m.sessionID) ?? emptyPerSessionState()),
                  running: {
                    id: m.cmdId,
                    command: m.command,
                    startedAt: m.startedAt,
                    output: '',
                    truncatedLossWarned: false,
                  },
                })
                return next
              })
              break
            case 'chunk':
              setPerSession((prev) => {
                const cur = prev.get(m.sessionID)
                if (!cur || !cur.running || cur.running.id !== m.cmdId) return prev
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...cur,
                  running: { ...cur.running, output: cur.running.output + b64decode(m.data) },
                })
                return next
              })
              break
            case 'done':
              setPerSession((prev) => {
                const cur = prev.get(m.sessionID)
                if (!cur || !cur.running || cur.running.id !== m.cmdId) return prev
                if (cur.messages.some((mm) => mm.id === m.cmdId)) return prev
                const completed: CompletedMsg = {
                  id: m.cmdId,
                  command: cur.running.command,
                  output: cur.running.output,
                  startedAt: cur.running.startedAt,
                  finishedAt: m.finishedAt,
                  exitCode: m.exitCode,
                  status: m.exitCode === 0 ? 'completed' : 'completed',
                  truncated: false,
                }
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...cur,
                  running: null,
                  messages: [...cur.messages, completed],
                })
                // Fire-and-forget: fetch the authoritative record.
                getCommand(m.sessionID, m.cmdId).then((full) => {
                  setPerSession((prev2) => {
                    const cur2 = prev2.get(m.sessionID)
                    if (!cur2) return prev2
                    const idx = cur2.messages.findIndex((mm) => mm.id === full.id)
                    if (idx < 0) return prev2
                    const updated = [...cur2.messages]
                    updated[idx] = {
                      id: full.id,
                      command: full.command,
                      output: full.output,
                      startedAt: full.started_at,
                      finishedAt: full.finished_at,
                      exitCode: full.exit_code,
                      status: full.status,
                      truncated: full.output_truncated,
                    }
                    const next2 = new Map(prev2)
                    next2.set(m.sessionID, { ...cur2, messages: updated })
                    return next2
                  })
                }).catch(() => {})
                return next
              })
              break
            case 'session_closed':
              setSessions((prev) => prev.filter((s) => s.id !== m.sessionID))
              setPerSession((prev) => {
                const next = new Map(prev)
                next.delete(m.sessionID)
                return next
              })
              setSelectedSessionID((prev) => {
                if (prev !== m.sessionID) return prev
                const remaining = sessionsRef.current.filter((s) => s.id !== m.sessionID)
                const next = remaining[0]?.id ?? null
                if (next) localStorage.setItem(STORAGE_KEY, next)
                else localStorage.removeItem(STORAGE_KEY)
                return next
              })
              break
            case 'session_renamed':
              setSessions((prev) => prev.map((s) => (s.id === m.sessionID ? { ...s, name: m.name } : s)))
              break
            case 'error':
              setLastError({ code: m.code, message: m.message })
              break
          }
        },
      }),
    [],
  )
```

And replace the placeholder callbacks at the bottom:

```typescript
  const submit = useCallback(
    (command: string) => {
      const sid = selectedSessionID
      if (!sid) return
      setLastError(null)
      socket.send({ type: 'run', sessionID: sid, command })
    },
    [socket, selectedSessionID],
  )

  const stop = useCallback(
    async (cmdID: string) => {
      const sid = selectedSessionID
      if (!sid) return
      try { await apiStopCommand(sid, cmdID) } catch {}
    },
    [selectedSessionID],
  )

  const createSession = useCallback(async (name?: string) => {
    try {
      const created = await apiCreateSession(name)
      setSessions((prev) => [...prev, created])
      selectSession(created.id)
      return created
    } catch (e: any) {
      setLastError({ code: e.code ?? 'create_failed', message: e.message ?? 'failed' })
      return null
    }
  }, [selectSession])

  const renameSession = useCallback(async (id: string, name: string) => {
    try {
      await apiRenameSession(id, name)
      setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, name } : s)))
    } catch (e: any) {
      setLastError({ code: e.code ?? 'rename_failed', message: e.message ?? 'failed' })
    }
  }, [])

  const closeSession = useCallback(async (id: string) => {
    try {
      await apiDeleteSession(id)
      // Server will also broadcast session_closed, which removes from state.
      // We don't optimistically remove here to keep cross-tab semantics simple.
    } catch (e: any) {
      setLastError({ code: e.code ?? 'close_failed', message: e.message ?? 'failed' })
    }
  }, [])

  return {
    connState, sessions, selectedSessionID, selectSession, perSession,
    submit, stop, createSession, renameSession, closeSession,
    lastError, clearError,
  }
```

- [ ] **Step 3: Run, confirm green**

Run: `cd web && npm test`
Expected: All tests in `useSessions.test.ts` PASS plus existing auth tests still PASS.

- [ ] **Step 4: Delete old useShell + redirect dependents**

```bash
rm web/src/features/terminal/useShell.ts
rm web/src/features/terminal/useShell.test.ts
```

Replace `web/src/features/terminal/types.ts` with a re-export so
that `ChatStream.tsx` and `CommandInput.tsx` continue to compile
without touching their imports. They currently pull `RunningCmd` and
`CompletedMsg` from this file (some directly, some transitively via
useShell which we just deleted).

```typescript
// Re-export so existing imports `import { CompletedMsg, RunningCmd }
// from './types'` keep working. The source of truth lives in
// features/sessions/types.ts now.
export type { RunningCmd, CompletedMsg } from '../sessions/types'
```

Audit `ChatStream.tsx`: if it has `import { CompletedMsg } from
'./useShell'`, change to `import { CompletedMsg } from './types'`.
Same for any other terminal/* files that referenced `useShell`'s
exports.

Verify nothing still imports the deleted file:

```bash
grep -rE "from ['\"].*useShell['\"]" web/src/
```

Expected: empty.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/useSessions.ts web/src/features/sessions/useSessions.test.ts web/src/features/terminal/types.ts
git rm web/src/features/terminal/useShell.ts web/src/features/terminal/useShell.test.ts
git commit -m "web: useSessions full implementation (WS events, CRUD callbacks, per-session running state)"
```

---

## Plan 8 acceptance

- `cd web && npm test` is green (all `useSessions.test.ts` tests + existing auth tests).
- `useShell` is deleted; no file references it.
- localStorage key `alfred_selected_session` is the only client-side persisted state.
- WS message dispatch is StrictMode-safe (idempotent updates; id-deduped messages append).

## Plan 8 self-review checklist

- [ ] `grep -rE "useShell" web/src/` returns empty.
- [ ] The mocked `ShellSocket.send` is called with `{ type: 'run', sessionID, command }` in tests.
- [ ] `session_closed` reassignment of `selectedSessionID` uses `sessionsRef.current` (not stale `sessions` closure).
