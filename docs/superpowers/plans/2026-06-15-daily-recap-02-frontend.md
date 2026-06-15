# Daily Recap (Frontend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the frontend half of the Daily Recap feature: `+ 复盘` button, recap session lifecycle from the client (create-on-click, delete-on-switch-away), shared `MarkdownView`, new `RecapSidebar` (mounted instead of SummarySidebar when `selected.kind === 'recap'`), `recap_updated` WS frame plumbing, and Playwright e2e.

**Architecture:** `useSessions` gains `createOrEnterRecap` + auto-delete-on-switch-away, a top-level `recapFetchCounter` (NOT in perSession), and a `setSessionMeta` injector for the recap session that doesn't appear in the chat list. A new shared `MarkdownView` extracts SummarySidebar's react-markdown wiring; SummarySidebar consumes it (no behavior change). RecapSidebar reuses MarkdownView for content and adds a date list + generate button at the top. WorkspacePage swaps in RecapSidebar when the selected session is `kind === 'recap'`.

**Tech Stack:** TypeScript, React 18, Vitest, Playwright, react-markdown (existing).

**Prereq:** Backend plan `2026-06-15-daily-recap-01-backend.md` must be merged to `main` first. This plan assumes `Kind`, `recap-daily` template, recap endpoints, and `recap_updated` WS frame all exist server-side.

---

## File Structure

| file | role | task |
|---|---|---|
| `web/src/lib/ws.ts` | add `recap_updated` ServerMsg variant | T1 |
| `web/src/lib/api.ts` | 4 new API helpers (create/delete recap session, list/get recap files) | T2 |
| `web/src/features/sessions/types.ts` | add `kind?` to Session interface | T3 |
| `web/src/features/sessions/useSessions.ts` | `createOrEnterRecap`, `recapFetchCounter`, auto-delete, `setSessionMeta` | T4 |
| `web/src/features/sessions/sessionsReducer.ts` | handle `recap_updated` (no-op for perSession; counter lives on useSessions) | T4 |
| `web/src/features/sessions/SessionsSidebar.tsx` | `+ 复盘` button at bottom; list still chat-only because backend already filters | T5 |
| `web/src/features/sessions/MarkdownView.tsx` | new — extracted markdown renderer | T6 |
| `web/src/features/sessions/MarkdownView.css` | new — extracted markdown styles | T6 |
| `web/src/features/sessions/SummarySidebar.tsx` | refactor to use MarkdownView | T6 |
| `web/src/features/sessions/SummarySidebar.css` | drop the now-extracted markdown rules | T6 |
| `web/src/features/sessions/RecapSidebar.tsx` | new component | T7 |
| `web/src/features/sessions/RecapSidebar.css` | new styles | T7 |
| `web/src/features/sessions/WorkspacePage.tsx` | mount RecapSidebar when `kind === 'recap'`; load session meta on selection | T8 |
| `web/e2e/regression.spec.ts` | end-to-end recap flow | T9 |

Nine tasks, roughly: protocol → API → types → state hook → sidebar button → markdown DRY → recap sidebar → workspace integration → e2e.

---

### Task 1: `recap_updated` ServerMsg variant

**Files:**
- Modify: `web/src/lib/ws.ts`

- [ ] **Step 1: Add the new variant**

Find the `ServerMsg` union (around line 25, look for the existing `'summary_updated'` variant). Add immediately after:

```ts
| { type: 'recap_updated'; date: string }
```

The full block context (showing only the change):

```ts
export type ServerMsg =
  ...
  | { type: 'summary_updated'; sessionID: string }
  | { type: 'recap_updated'; date: string }
  | { type: 'error'; sessionID?: string; code: string; message: string }
  | { type: 'pong' }
```

- [ ] **Step 2: TS check**

```bash
cd web && npx tsc --noEmit
```

Expected: clean — no existing code references the new variant yet, just the type.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/ws.ts
git commit -m "feat(ws): recap_updated ServerMsg variant"
```

---

### Task 2: API client helpers

**Files:**
- Modify: `web/src/lib/api.ts`

Four helpers: `createRecapSession`, `deleteRecapSession`, `listRecaps`, `getRecap`. Also needs `getSession(id)` for selected-session meta lookup (recap session won't be in the list). Five total.

- [ ] **Step 1: Append helpers**

Open `web/src/lib/api.ts`. At the top, add `import type { Session } from '../features/sessions/types'` if not already imported (Session might not be exported from there yet — Task 3 adds `kind` to it; for now, define a minimal shape inline if needed and adjust in T3). Append at the END of the file:

```ts
// getSession fetches one session's metadata by id. Used to look up
// the currently selected recap session, which doesn't appear in the
// chat-only sessions list.
export async function getSession(id: string): Promise<Session> {
  const res = await request(`/api/sessions/${encodeURIComponent(id)}`)
  return res.json()
}

// createRecapSession is POST /api/recap-sessions — find-or-create
// the singleton recap session. Returns the session metadata
// (including the new `kind: 'recap'` field).
export async function createRecapSession(): Promise<Session> {
  const res = await request('/api/recap-sessions', { method: 'POST' })
  return res.json()
}

// deleteRecapSession is DELETE /api/recap-sessions/current —
// idempotent kill. 204 even if no recap session exists.
export async function deleteRecapSession(): Promise<void> {
  await request('/api/recap-sessions/current', { method: 'DELETE' })
}

export interface RecapEntry {
  date: string
  isToday: boolean
}

// listRecaps returns the dates that have recap files, newest first.
export async function listRecaps(): Promise<RecapEntry[]> {
  const res = await request('/api/recaps')
  return res.json()
}

// getRecap returns the markdown body of one date's recap.
// Throws ApiError(404, 'not_found', ...) when no such recap exists.
export async function getRecap(date: string): Promise<string> {
  const res = await request(`/api/recaps/${encodeURIComponent(date)}`)
  return res.text()
}
```

- [ ] **Step 2: TS check**

```bash
cd web && npx tsc --noEmit
```

Expected: clean (Session import may complain — if so, the type is fine to import; Task 3 will make sure `kind` is on it). If TSC errors about `Session` not exported, add an export to `types.ts` first as a one-liner.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "feat(api): recap session + content client helpers + getSession"
```

---

### Task 3: `Session.kind` field

**Files:**
- Modify: `web/src/features/sessions/types.ts`

- [ ] **Step 1: Inspect**

```bash
grep -n "export interface Session\|export type Session" web/src/features/sessions/types.ts
```

If `Session` isn't defined in `types.ts`, it likely lives in `web/src/lib/api.ts`. Wherever it is, add the field there.

- [ ] **Step 2: Add the field**

Find the `Session` interface (most likely in `web/src/lib/api.ts` based on how things are typically organized). Currently:

```ts
export interface Session {
  id: string
  name: string
  created_at: string
}
```

Add `kind` after `name`:

```ts
export interface Session {
  id: string
  name: string
  // 'chat' (default; empty/missing on old records) or 'recap'.
  kind?: 'chat' | 'recap'
  created_at: string
}
```

The backend marshals empty string `""` as missing (omitempty), so old chat sessions return `{}` (no `kind` field) — TS will see `undefined`, which we treat as `'chat'`.

- [ ] **Step 3: TS check + vitest**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green.

- [ ] **Step 4: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "feat(types): Session.kind field"
```

---

### Task 4: `useSessions` recap state

**Files:**
- Modify: `web/src/features/sessions/useSessions.ts`
- Modify: `web/src/features/sessions/sessionsReducer.ts`
- Modify: `web/src/features/sessions/useSessions.test.ts` (add 3 tests)

Three additions: a top-level `recapFetchCounter` bumped on every `recap_updated` frame; a `createOrEnterRecap` callback that POSTs the backend create endpoint and selects the returned session; an effect that fires `deleteRecapSession()` whenever the selected sid changes AWAY from a recap session.

- [ ] **Step 1: Add `recap_updated` to the reducer (no-op for perSession)**

In `web/src/features/sessions/sessionsReducer.ts`, find the `switch (m.type)` block. Add a new case just before `default:`:

```ts
case 'recap_updated': {
  // Recap files are global (not per-session); the counter that
  // triggers refetch lives on useSessions top-level state, NOT
  // perSession. Reducer is a no-op here.
  return { perSession: prev }
}
```

- [ ] **Step 2: Add `recapFetchCounter` + `createOrEnterRecap` to useSessions**

In `web/src/features/sessions/useSessions.ts`, near the top of the hook body where the other useStates live, add:

```ts
const [recapFetchCounter, setRecapFetchCounter] = useState(0)
```

In the `onMessage` callback (around line 98), find the existing `if (m.type === 'session_renamed')` branch. Add a new branch immediately after it:

```ts
if (m.type === 'recap_updated') {
  setRecapFetchCounter((c) => c + 1)
  return
}
```

After the existing `enterClaude` callback (around line 238), add the new helpers. We also need a `setSessionMeta` injector for the recap session (it won't appear in the list, so we manually inject):

```ts
// setSessionMeta replaces or inserts a Session meta in the local list.
// Used after createOrEnterRecap when the new session isn't in the
// chat-filtered list, and after getSession() rehydration on selection.
const setSessionMeta = useCallback((s: Session) => {
  setSessions((prev) => {
    const idx = prev.findIndex((x) => x.id === s.id)
    if (idx >= 0) {
      const next = [...prev]
      next[idx] = s
      return next
    }
    return [...prev, s]
  })
}, [])

const createOrEnterRecap = useCallback(async () => {
  try {
    const s = await apiCreateRecapSession()
    setSessionMeta(s)
    selectSession(s.id)
    // No StartClaudeDialog: backend has already entered Claude UI mode
    // with bypassPermissions=true. The renderer is reset to 'ui' by
    // an explicit enterClaude here to ensure the perSession state knows
    // it's in claude mode immediately, even before the WS 'idle' frame
    // arrives.
    socket.send({
      type: 'enter_claude',
      sessionID: s.id,
      renderer: 'ui',
      bypassPermissions: true,
      templateId: '', // recap doesn't use the summary template
    })
  } catch (e: any) {
    setLastError({ code: e?.code ?? 'recap_create_failed', message: e?.message ?? 'failed' })
  }
}, [socket, selectSession, setSessionMeta])
```

Add the missing import at the top:

```ts
import {
  Session,
  listSessions,
  getCommand,
  createSession as apiCreateSession,
  renameSession as apiRenameSession,
  deleteSession as apiDeleteSession,
  stopCommand as apiStopCommand,
  createRecapSession as apiCreateRecapSession,
  deleteRecapSession as apiDeleteRecapSession,
  getSession as apiGetSession,
} from '../../lib/api'
```

- [ ] **Step 3: Auto-delete on switch-away**

Add a new useEffect AFTER the existing useEffects that auto-fires `deleteRecapSession` when the selected sid changes AND the previous selection was a recap. The trick: we don't know the previous selection's `kind` after it changes, so we track it via a ref.

```ts
// Track the previously selected session's kind so we can detect a
// switch-away from a recap session and trigger backend cleanup.
const prevRecapIdRef = useRef<string | null>(null)
useEffect(() => {
  const sid = selectedSessionID
  const meta = sid ? sessions.find((x) => x.id === sid) : undefined
  const isRecap = meta?.kind === 'recap'

  // If we WERE on a recap and are now somewhere else, kill it.
  if (prevRecapIdRef.current && prevRecapIdRef.current !== sid) {
    apiDeleteRecapSession().catch(() => {
      // Idempotent; if the network fails the next createOrEnter
      // call's orphan-cleanup will recover.
    })
  }
  prevRecapIdRef.current = isRecap ? sid : null
}, [selectedSessionID, sessions])
```

- [ ] **Step 4: Rehydrate selected session meta on first paint**

The selected session might be a recap session restored from localStorage — but the chat-filtered list won't include it. After the initial `listSessions()` call, if the persisted selection isn't found, fetch it explicitly:

In the initial REST useEffect (around line 68), find:

```ts
listSessions()
  .then((list) => {
    if (!alive) return
    setSessions(list)
    const stored = localStorage.getItem(STORAGE_KEY)
    let pick = list.find((s) => s.id === stored)
    if (!pick) pick = list[0]
    setSelectedSessionID(pick?.id ?? null)
    if (pick) localStorage.setItem(STORAGE_KEY, pick.id)
  })
```

Replace with:

```ts
listSessions()
  .then(async (list) => {
    if (!alive) return
    setSessions(list)
    const stored = localStorage.getItem(STORAGE_KEY)
    let pick: Session | undefined = list.find((s) => s.id === stored)
    if (!pick && stored) {
      // Maybe the selection is a recap session (not in chat list).
      // Fetch it explicitly; tolerate 404 (session was deleted).
      try {
        const single = await apiGetSession(stored)
        setSessionMeta(single)
        pick = single
      } catch {
        // 404 or other — fall through to default selection
      }
    }
    if (!pick) pick = list[0]
    if (!alive) return
    setSelectedSessionID(pick?.id ?? null)
    if (pick) localStorage.setItem(STORAGE_KEY, pick.id)
  })
```

- [ ] **Step 5: Export the new fields**

Find the `return { ... }` at the end of the hook (around line 324). Add `recapFetchCounter`, `createOrEnterRecap`, and `setSessionMeta` to the returned object:

```ts
return {
  connState, sessions, selectedSessionID, selectSession, perSession, setPerSession,
  submit, stop, createSession, renameSession, closeSession,
  enterClaude, exitClaude, sendStdin, registerPtyHandler,
  claudePrompt, toolDecision, interruptClaude, submitQuestionAnswer,
  lastError, clearError,
  recapFetchCounter, createOrEnterRecap, setSessionMeta,
}
```

- [ ] **Step 6: Tests**

In `web/src/features/sessions/useSessions.test.ts`, add three tests at the end (matching the existing file's pattern — read the existing `enterClaude` tests to discover the mocking style for ShellSocket):

```ts
it('recap_updated frame bumps recapFetchCounter', async () => {
  vi.spyOn(api, 'listSessions').mockResolvedValue([])
  const { result } = renderHook(() => useSessions('tkn'))
  await waitFor(() => expect(result.current.connState).toBeDefined())
  act(() => {
    // Simulate an incoming WS frame. The test harness exposes a way
    // to inject; if not, use the same pattern other tests in this
    // file use. (Many tests reach into MockShellSocket directly.)
    deliverServerMsg({ type: 'recap_updated', date: '2026-06-15' })
  })
  expect(result.current.recapFetchCounter).toBe(1)
})

it('createOrEnterRecap selects the returned session', async () => {
  vi.spyOn(api, 'listSessions').mockResolvedValue([])
  vi.spyOn(api, 'createRecapSession').mockResolvedValue({
    id: 'recap-1', name: 'Recap', kind: 'recap', created_at: '2026-06-15T00:00:00Z',
  } as any)
  const { result } = renderHook(() => useSessions('tkn'))
  await act(async () => { await result.current.createOrEnterRecap() })
  expect(result.current.selectedSessionID).toBe('recap-1')
  expect(result.current.sessions.find((s) => s.id === 'recap-1')?.kind).toBe('recap')
})

it('switch-away from recap session fires deleteRecapSession', async () => {
  vi.spyOn(api, 'listSessions').mockResolvedValue([
    { id: 'chat-1', name: 'c', created_at: '2026-06-15T00:00:00Z' } as any,
  ])
  const delSpy = vi.spyOn(api, 'deleteRecapSession').mockResolvedValue()
  vi.spyOn(api, 'createRecapSession').mockResolvedValue({
    id: 'recap-1', name: 'Recap', kind: 'recap', created_at: '2026-06-15T00:00:00Z',
  } as any)
  const { result } = renderHook(() => useSessions('tkn'))
  await waitFor(() => expect(result.current.sessions).toHaveLength(1))
  await act(async () => { await result.current.createOrEnterRecap() })
  // Now switch to chat-1
  act(() => { result.current.selectSession('chat-1') })
  await waitFor(() => expect(delSpy).toHaveBeenCalled())
})
```

If the test file doesn't already export a `deliverServerMsg` helper or similar, mirror whatever pattern existing tests use (typically a mock `ShellSocket` that exposes a `_simulate(m: ServerMsg)` method). If you must invent the pattern, prefer extending the existing mock rather than adding a new top-level helper.

- [ ] **Step 7: Run tests + TS check**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green. The new tests bring total from 82 (or 86 if IME plan landed first) to 85/89.

- [ ] **Step 8: Commit**

```bash
git add web/src/features/sessions/useSessions.ts \
        web/src/features/sessions/useSessions.test.ts \
        web/src/features/sessions/sessionsReducer.ts
git commit -m "feat(sessions): createOrEnterRecap, recapFetchCounter, auto-delete on switch-away"
```

---

### Task 5: `+ 复盘` button in SessionsSidebar

**Files:**
- Modify: `web/src/features/sessions/SessionsSidebar.tsx`
- Modify: `web/src/features/sessions/SessionsSidebar.css`

A second action button below `+ New chat`. Calls `onCreateRecap` from props (we don't import the hook into the sidebar directly — that's WorkspacePage's job).

- [ ] **Step 1: Add the prop + button**

Find the existing `+ New chat` button's render code (look for `+ New chat` in the file). Add the prop to the component's Props interface:

```ts
interface Props {
  // ... existing props ...
  onCreateRecap: () => void | Promise<void>
}
```

Add a sibling button immediately after the `+ New chat` one. Use a wrapping div or just place them next to each other:

```tsx
<button
  type="button"
  className="sessions-sidebar__create-recap"
  onClick={() => onCreateRecap()}
  title="Open today's recap"
>
  + 复盘
</button>
```

- [ ] **Step 2: Style**

Append to `web/src/features/sessions/SessionsSidebar.css`:

```css
.sessions-sidebar__create-recap {
  display: block;
  width: 100%;
  text-align: left;
  padding: 8px 10px;
  margin-top: 4px;
  border: 1px dashed rgba(255, 255, 255, 0.12);
  border-radius: 6px;
  background: transparent;
  color: var(--fg-muted, #9aa3b2);
  font-size: 13px;
  cursor: pointer;
}

.sessions-sidebar__create-recap:hover {
  color: var(--fg, #eaeaea);
  border-color: rgba(255, 255, 255, 0.24);
  background: rgba(255, 255, 255, 0.03);
}
```

Distinguished from the primary `+ New chat` button by the dashed border so it's clearly a secondary action.

- [ ] **Step 3: TS + vitest**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: TS will complain that the SessionsSidebar call site (WorkspacePage) doesn't pass `onCreateRecap`. Add `onCreateRecap={s.createOrEnterRecap}` to the WorkspacePage's `<SessionsSidebar />` JSX. Test that vitest passes.

If a unit test on SessionsSidebar exists (`SessionsSidebar.test.tsx`), it likely renders the component with explicit props — add `onCreateRecap: vi.fn()` to the fixture.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/SessionsSidebar.tsx \
        web/src/features/sessions/SessionsSidebar.css \
        web/src/features/sessions/SessionsSidebar.test.tsx \
        web/src/features/sessions/WorkspacePage.tsx
git commit -m "feat(sidebar): + 复盘 button calling createOrEnterRecap"
```

---

### Task 6: Extract `MarkdownView` from SummarySidebar

**Files:**
- Create: `web/src/features/sessions/MarkdownView.tsx`
- Create: `web/src/features/sessions/MarkdownView.css`
- Modify: `web/src/features/sessions/SummarySidebar.tsx`
- Modify: `web/src/features/sessions/SummarySidebar.css`

Pure refactor — no behavior change. Pull the `<ReactMarkdown>` + `remarkGfm` + the matching CSS (`.summary-sidebar__markdown` rules) into a generic component.

- [ ] **Step 1: Read the current SummarySidebar markdown block**

```bash
grep -n "ReactMarkdown\|summary-sidebar__markdown" web/src/features/sessions/SummarySidebar.tsx web/src/features/sessions/SummarySidebar.css
```

Find the `SummaryView` function (around line 122). Its render contains a `<ReactMarkdown>` invocation; everything inside the `<div className="summary-sidebar__markdown">` wrapper is what gets extracted.

- [ ] **Step 2: Create the component**

```tsx
// web/src/features/sessions/MarkdownView.tsx
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { ComponentPropsWithoutRef } from 'react'
import './MarkdownView.css'

interface Props {
  text: string
  className?: string  // wrapper-level extras (e.g. parent's namespacing)
}

// MarkdownView renders a markdown string into the dark-mode styles
// shared by SummarySidebar and RecapSidebar. Code blocks are
// rendered with simple inline styling (no syntax highlighter — the
// content here is short summaries, not code-heavy).
export function MarkdownView({ text, className }: Props) {
  return (
    <div className={`markdown-view ${className ?? ''}`}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code(props) {
            const { className, children, ...rest } =
              props as ComponentPropsWithoutRef<'code'>
            return <code className={className} {...rest}>{children}</code>
          },
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
```

- [ ] **Step 3: Create the styles**

```css
/* web/src/features/sessions/MarkdownView.css */
.markdown-view {
  font-size: 13px;
  line-height: 1.55;
  color: var(--fg, #eaeaea);
}

.markdown-view h1,
.markdown-view h2,
.markdown-view h3 {
  margin: 16px 0 8px 0;
  font-weight: 600;
}

.markdown-view h1 { font-size: 16px; }
.markdown-view h2 { font-size: 14px; }
.markdown-view h3 { font-size: 13px; }

.markdown-view h1:first-child,
.markdown-view h2:first-child,
.markdown-view h3:first-child {
  margin-top: 0;
}

.markdown-view p,
.markdown-view ul,
.markdown-view ol {
  margin: 8px 0;
}

.markdown-view ul,
.markdown-view ol {
  padding-left: 20px;
}

.markdown-view li {
  margin: 4px 0;
}

.markdown-view code {
  background: rgba(255, 255, 255, 0.06);
  padding: 1px 5px;
  border-radius: 3px;
  font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11.5px;
}

.markdown-view a {
  color: var(--accent, #7aa2f7);
  text-decoration: none;
}

.markdown-view a:hover {
  text-decoration: underline;
}

.markdown-view blockquote {
  margin: 8px 0;
  padding: 6px 12px;
  border-left: 3px solid rgba(255, 255, 255, 0.15);
  color: var(--fg-muted, #9aa3b2);
}
```

These rules are lifted verbatim from `SummarySidebar.css` with `.summary-sidebar__markdown` renamed to `.markdown-view`.

- [ ] **Step 4: Update SummarySidebar to use MarkdownView**

In `web/src/features/sessions/SummarySidebar.tsx`, replace the `SummaryView` function's success branch:

```tsx
return (
  <div className="summary-sidebar__markdown">
    <ReactMarkdown ...>...</ReactMarkdown>
  </div>
)
```

With:

```tsx
return <MarkdownView text={text} />
```

Drop the now-unused imports `ReactMarkdown`, `remarkGfm`, and `ComponentPropsWithoutRef` (if they're only used inside SummaryView).

Add the new import:

```tsx
import { MarkdownView } from './MarkdownView'
```

- [ ] **Step 5: Trim SummarySidebar.css**

Open `web/src/features/sessions/SummarySidebar.css`. Delete every rule that targets `.summary-sidebar__markdown` (and its children: `h1`, `h2`, `p`, `ul`, `ol`, `li`, `code`, `a`, `blockquote`). They're now in MarkdownView.css.

- [ ] **Step 6: Smoke test SummarySidebar visually unchanged**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: TS clean, all tests still pass. If a SummarySidebar test existed, it should be unaffected because the rendered DOM shape is the same (just a different className on the wrapping div).

Manual visual check: load the existing summary feature with a session that has a template, confirm the markdown still renders identically.

- [ ] **Step 7: Commit**

```bash
git add web/src/features/sessions/MarkdownView.tsx \
        web/src/features/sessions/MarkdownView.css \
        web/src/features/sessions/SummarySidebar.tsx \
        web/src/features/sessions/SummarySidebar.css
git commit -m "refactor(sidebar): extract MarkdownView; SummarySidebar consumes it"
```

---

### Task 7: `RecapSidebar` component

**Files:**
- Create: `web/src/features/sessions/RecapSidebar.tsx`
- Create: `web/src/features/sessions/RecapSidebar.css`

The sidebar renders: title, Generate/Refresh button (today only), date list, content of the selected date. Selection defaults to today; switching forces a content refetch. The data layer refetches whenever `recapFetchCounter` (passed in from props) bumps.

- [ ] **Step 1: Build the component**

```tsx
// web/src/features/sessions/RecapSidebar.tsx
import { useCallback, useEffect, useRef, useState } from 'react'
import { getRecap, listRecaps, RecapEntry } from '../../lib/api'
import { MarkdownView } from './MarkdownView'
import './RecapSidebar.css'

interface Props {
  // Bumps when the backend reports a recap_updated frame; the
  // sidebar re-fetches the date list and the currently-shown
  // content on every bump.
  recapFetchCounter: number
  // Fires the recap-daily template prompt as a claude_prompt to the
  // current (recap) Claude session. WorkspacePage supplies this.
  onGenerate: () => void
  // True iff Claude is currently mid-prompt in this session. Used
  // to disable the Generate button.
  generating: boolean
}

type Tab = { kind: 'today'; date: string } | { kind: 'past'; date: string }

function today(): string {
  return new Date().toLocaleDateString('en-CA') // YYYY-MM-DD
}

export function RecapSidebar({ recapFetchCounter, onGenerate, generating }: Props) {
  const [list, setList] = useState<RecapEntry[]>([])
  const [listLoading, setListLoading] = useState(true)
  const [listError, setListError] = useState<string | null>(null)
  const todayDate = today()
  const [selectedDate, setSelectedDate] = useState<string>(todayDate)
  const [content, setContent] = useState<string>('')
  const [contentLoading, setContentLoading] = useState(false)
  const [contentError, setContentError] = useState<string | null>(null)

  // Date list fetch — triggered on mount + every counter bump.
  // First fetch shows a spinner; subsequent bumps are silent to
  // avoid flicker.
  const firstListFetchDone = useRef(false)
  useEffect(() => {
    let alive = true
    const showSpinner = !firstListFetchDone.current
    if (showSpinner) setListLoading(true)
    listRecaps()
      .then((entries) => {
        if (!alive) return
        setList(entries)
        setListError(null)
      })
      .catch((e) => {
        if (!alive) return
        setListError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        firstListFetchDone.current = true
        if (showSpinner) setListLoading(false)
      })
    return () => { alive = false }
  }, [recapFetchCounter])

  // Content fetch — when selectedDate changes OR counter bumps AND
  // the bumped date is the selected one. (For simplicity we just
  // refetch on every counter bump regardless of which date —
  // payload is small.)
  useEffect(() => {
    let alive = true
    setContentLoading(true)
    setContentError(null)
    getRecap(selectedDate)
      .then((text) => {
        if (!alive) return
        setContent(text)
      })
      .catch((e: any) => {
        if (!alive) return
        if (e && e.status === 404) {
          setContent('')
          return
        }
        setContentError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        setContentLoading(false)
      })
    return () => { alive = false }
  }, [selectedDate, recapFetchCounter])

  const isToday = selectedDate === todayDate
  const hasTodayFile = list.some((e) => e.date === todayDate)
  const generateLabel = hasTodayFile ? 'Refresh today\'s recap' : 'Generate today\'s recap'

  const handleSelect = useCallback((date: string) => {
    setSelectedDate(date)
  }, [])

  return (
    <aside className="recap-sidebar" aria-label="Recap sidebar">
      <header className="recap-sidebar__header">
        <h2 className="recap-sidebar__title">Recap</h2>
      </header>
      {isToday && (
        <div className="recap-sidebar__generate-row">
          <button
            type="button"
            className="recap-sidebar__generate"
            onClick={onGenerate}
            disabled={generating}
          >
            {generating ? 'Generating…' : generateLabel}
          </button>
        </div>
      )}
      <div className="recap-sidebar__list">
        {listLoading && <div className="recap-sidebar__placeholder">Loading…</div>}
        {listError && <div className="recap-sidebar__error">Failed to load: {listError}</div>}
        {!listLoading && !listError && list.length === 0 && (
          <div className="recap-sidebar__placeholder">No recaps yet.</div>
        )}
        {/* Always render today first, even if no file exists yet */}
        {!hasTodayFile && (
          <button
            type="button"
            className={`recap-sidebar__date ${selectedDate === todayDate ? 'is-selected' : ''}`}
            onClick={() => handleSelect(todayDate)}
          >
            Today · {todayDate}
          </button>
        )}
        {list.map((entry) => (
          <button
            key={entry.date}
            type="button"
            className={`recap-sidebar__date ${selectedDate === entry.date ? 'is-selected' : ''}`}
            onClick={() => handleSelect(entry.date)}
          >
            {entry.isToday ? `Today · ${entry.date}` : entry.date}
          </button>
        ))}
      </div>
      <div className="recap-sidebar__content">
        {contentLoading && <div className="recap-sidebar__placeholder">Loading…</div>}
        {contentError && <div className="recap-sidebar__error">Failed to load: {contentError}</div>}
        {!contentLoading && !contentError && !content && (
          <div className="recap-sidebar__placeholder">
            {isToday
              ? 'No recap for today yet. Click Generate to create one.'
              : 'No recap for this date.'}
          </div>
        )}
        {!contentLoading && !contentError && content && <MarkdownView text={content} />}
      </div>
    </aside>
  )
}
```

- [ ] **Step 2: Styles**

```css
/* web/src/features/sessions/RecapSidebar.css */
.recap-sidebar {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-elevated, #1a1d24);
  border-left: 1px solid rgba(255, 255, 255, 0.06);
  color: var(--fg, #eaeaea);
  overflow: hidden;
}

.recap-sidebar__header {
  padding: 12px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.recap-sidebar__title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}

.recap-sidebar__generate-row {
  padding: 12px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.recap-sidebar__generate {
  width: 100%;
  padding: 8px 12px;
  border-radius: 6px;
  border: none;
  background: var(--accent, #7aa2f7);
  color: #0c0f15;
  font-weight: 600;
  font-size: 13px;
  cursor: pointer;
}

.recap-sidebar__generate:hover:not(:disabled) {
  filter: brightness(1.08);
}

.recap-sidebar__generate:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.recap-sidebar__list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
  max-height: 35%;
  overflow-y: auto;
}

.recap-sidebar__date {
  text-align: left;
  padding: 6px 10px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  color: var(--fg, #eaeaea);
  font-size: 12.5px;
  cursor: pointer;
}

.recap-sidebar__date:hover {
  background: rgba(255, 255, 255, 0.04);
}

.recap-sidebar__date.is-selected {
  background: rgba(122, 162, 247, 0.12);
  border-color: rgba(122, 162, 247, 0.35);
}

.recap-sidebar__content {
  flex: 1;
  overflow-y: auto;
  padding: 14px 16px;
}

.recap-sidebar__placeholder {
  color: var(--fg-muted, #9aa3b2);
  font-size: 12px;
  line-height: 1.6;
}

.recap-sidebar__error {
  color: #f7768e;
  font-size: 12px;
  line-height: 1.5;
}
```

- [ ] **Step 3: TS + vitest**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/RecapSidebar.tsx \
        web/src/features/sessions/RecapSidebar.css
git commit -m "feat(sidebar): RecapSidebar with date list + generate button"
```

---

### Task 8: Mount RecapSidebar in WorkspacePage

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`

When `selected.kind === 'recap'`, mount `<RecapSidebar />` instead of `<SummarySidebar />`. The two are mutually exclusive (per spec). The generate button fires a `claudePrompt` with the recap-daily template; for v1 we render the template client-side by calling a new tiny template-rendering API helper, OR we just have the backend render the template and the frontend send `templateId: 'recap-daily'` somehow.

Simpler: send the literal prompt text. Frontend has no template rendering — the prompt is short, we hard-code it client-side as the user message. But this duplicates the template content. Better: add a new WS frame `claude_prompt_template` where the backend renders. Even simpler for v1: re-use the existing `claudePrompt` and have the frontend send a fixed string like `"/recap"` (a slash command Claude doesn't recognize) — NO, that's a hack.

**Cleanest for v1:** the backend's `recap-daily` template is rendered server-side when a special `claude_prompt` arrives with `text === ""` and a recap-session is selected. But that's coupling text-empty to template-fire which is fragile.

**Final decision:** add a new method `claudeRunRecapPrompt(sid)` on useSessions that POSTs `POST /api/sessions/{sid}/recap-prompt` — a new backend endpoint that internally renders the template and forwards to the existing claude_prompt path. Move this small backend addition into THIS plan's Task 8 since it's the only file using it.

(If you'd rather keep this to pure frontend, an alternative is for the backend to render once on `POST /api/recap-sessions` and store the rendered text in the session meta; the frontend reads it and submits as a normal claude_prompt. Decide based on what's cleaner — for v1 the dedicated POST is simpler.)

- [ ] **Step 1: Decide template-firing mechanism — frontend renders**

The Generate button fires the `recap-daily` template as a regular
`claude_prompt`. The frontend fetches the raw template via the
existing `GET /api/templates/{id}` endpoint, substitutes
placeholders client-side, and submits. No new backend endpoint.

Trade-off: `<cwd>` is unknowable client-side (the backend chooses
based on whether we're in a pod or dev), so we leave it as
`$(pwd)` in the rendered text — Claude will execute it as a Bash
substitution. The other placeholders (`<date>`, `<recap_path>`)
are straightforward.

The substitution lives in `WorkspacePage.tsx`'s `onGenerate`
handler (added in Step 2 below). No new files, no new endpoints.

- [ ] **Step 2: WorkspacePage integration**

Open `web/src/features/sessions/WorkspacePage.tsx`. Add import:

```tsx
import { RecapSidebar } from './RecapSidebar'
import { getTemplate } from '../../lib/api'
```

Find the existing `selected` and `ps` derivation (around line 63):

```tsx
const selected = s.selectedSessionID
  ? s.sessions.find((x) => x.id === s.selectedSessionID)
  : null
```

Add `isRecap`:

```tsx
const isRecap = selected?.kind === 'recap'
```

Replace the existing `showSidebarSlot` computation:

```tsx
const showSidebarSlot = !!(selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo')
```

With one that excludes recap sessions from SummarySidebar logic, plus a separate flag for recap:

```tsx
const showSummarySidebar = !!(selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo' && !isRecap)
const showRecapSidebar = !!(selected && isRecap)
// has-sidebar is true if EITHER sidebar is rendering.
const sidebarShown = (showSummarySidebar && !sidebarHidden) || showRecapSidebar
```

Replace the workspace root `className` to use `sidebarShown`:

```tsx
<div className={`workspace ${sidebarShown ? 'has-sidebar' : ''}`}>
```

Find the existing `<SummarySidebar ... />` mount JSX (around line 238). Wrap it in a conditional that excludes recap:

```tsx
{showSummarySidebar && !sidebarHidden && selected && ps && (
  <SummarySidebar ... />
)}
```

After the SummarySidebar mount block, add the RecapSidebar mount:

```tsx
{showRecapSidebar && selected && (
  <RecapSidebar
    recapFetchCounter={s.recapFetchCounter}
    generating={!!ps?.claude?.inFlight}
    onGenerate={async () => {
      const tpl = await getTemplate('recap-daily')
      const date = new Date().toLocaleDateString('en-CA')
      const recapPath = `recaps/${date}.md`
      const text = tpl
        .replaceAll('<date>', date)
        .replaceAll('<cwd>', '$(pwd)')
        .replaceAll('<recap_path>', recapPath)
      s.claudePrompt(selected.id, text)
    }}
  />
)}
```

Also pass `onCreateRecap={s.createOrEnterRecap}` to the existing `<SessionsSidebar />`.

- [ ] **Step 3: Sidebar handle behavior for recap mode**

The existing "show summary sidebar" handle (`workspace__sidebar-handle`) only makes sense in Chat UI mode. In recap mode the sidebar is intrinsic and never hidden. Find the existing handle logic:

```tsx
const showSidebarHandle = !!(selected && ps && ps.mode === 'claude' && ps.renderer === 'ui' && sidebarHidden)
```

Update to exclude recap:

```tsx
const showSidebarHandle = !!(selected && ps && ps.mode === 'claude' && ps.renderer === 'ui' && sidebarHidden && !isRecap)
```

- [ ] **Step 4: TS + vitest + e2e (existing tests)**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/WorkspacePage.tsx
git commit -m "feat(workspace): mount RecapSidebar for kind=recap; generate fires recap-daily template"
```

---

### Task 9: Playwright e2e

**Files:**
- Modify: `web/e2e/regression.spec.ts`

End-to-end recap flow:
1. Click `+ 复盘` → recap session created + selected, ClaudeChatView visible, RecapSidebar visible.
2. Click Generate → backend writes today's recap (via Claude). Wait up to 90s.
3. RecapSidebar shows today's date + content.
4. Cleanup: delete the recap file + recap session.

Uses real Claude API (no mock), so slow + flaky. Same caveat as the existing `AskUserQuestion` and `history restores` tests.

- [ ] **Step 1: Append new describe at end of file**

```ts
test.describe('Recap: generate today and view in sidebar', () => {
  test('+ 复盘 → Generate → sidebar shows today\'s file', async ({ page }) => {
    test.setTimeout(120_000)

    const tok = await login(page)
    await loginUI(page, tok)
    // We don't pre-create a session — + 复盘 creates one on the backend.

    // Click + 复盘.
    await page.locator('text=+ 复盘').click()

    // Wait for the recap workspace to render — RecapSidebar should appear.
    await expect(page.locator('.recap-sidebar')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('.recap-sidebar__title')).toHaveText('Recap')

    // The ClaudeChatView should be visible in the middle.
    await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 10_000 })

    // Click Generate.
    const generate = page.locator('.recap-sidebar__generate')
    await expect(generate).toBeVisible()
    await generate.click()
    // Button disables while Claude is mid-prompt.
    await expect(generate).toBeDisabled({ timeout: 5_000 })

    // Wait for the file to appear in the data dir.
    const DATA_DIR = process.env.ALFRED_DATA_DIR || '/tmp/alfred-dev/data'
    const today = new Date().toLocaleDateString('en-CA')
    const recapPath = path.join(DATA_DIR, 'recaps', `${today}.md`)
    const deadline = Date.now() + 90_000
    while (Date.now() < deadline) {
      if (fs.existsSync(recapPath)) break
      await page.waitForTimeout(500)
    }
    if (!fs.existsSync(recapPath)) {
      throw new Error(`recap file never appeared at ${recapPath}`)
    }

    // The sidebar's content area should pick it up.
    await expect(page.locator('.recap-sidebar__content .markdown-view'))
      .toBeVisible({ timeout: 15_000 })
    await expect(page.locator('.recap-sidebar__content')).toContainText('Recap')

    // Cleanup.
    try { fs.unlinkSync(recapPath) } catch {}
    // Best-effort kill the recap session.
    await page.request.delete(`${BACKEND}/api/recap-sessions/current`, {
      headers: { Authorization: `Bearer ${tok}` },
    }).catch(() => {})
  })
})
```

- [ ] **Step 2: Run the test against a live backend + frontend**

```bash
cd web && ALFRED_DATA_DIR=/tmp/alfred-dev/data \
  npx playwright test --grep "Generate.*sidebar shows today" 2>&1 | tail -20
```

Expected: PASS within 90s. If it fails:
- If Claude API is unreachable → not a code issue, skip.
- If the recap session doesn't get created → check backend logs; likely the `+ 复盘` button isn't wired.
- If the file appears but the sidebar doesn't refresh → the `recap_updated` WS frame plumbing is broken.

- [ ] **Step 3: Commit**

```bash
git add web/e2e/regression.spec.ts
git commit -m "test(e2e): recap session create + generate + sidebar refresh"
```

---

## Final verification

- [ ] **TS + vitest**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + all green.

- [ ] **Playwright full suite**

```bash
cd web && ALFRED_DATA_DIR=/tmp/alfred-dev/data npx playwright test 2>&1 | tail -20
```

Expected: all existing tests still pass + 1 new recap test green.

- [ ] **Manual smoke** in the browser

1. Click `+ 复盘`.
2. Confirm a recap session opens (right sidebar visible, chat textarea visible).
3. Click Generate. Wait. See "Generating…" on button; eventually it returns to "Refresh today's recap".
4. Today's content appears in the sidebar content area.
5. Click a date in the list (if any past dates exist). Content swaps. Generate button hidden.
6. Click back to "Today · YYYY-MM-DD". Generate button reappears.
7. Click `+ New chat` (or any existing chat). Recap session is killed backend-side; switching back to chat works.
8. Click `+ 复盘` again. A fresh recap session opens. The `claude -c` flag means the prior conversation is continuous.
