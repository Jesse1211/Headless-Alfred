# Summary template — Plan 04: Frontend dialog checkbox + SummarySidebar + e2e

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user opt into the summary template from the Start Claude dialog and read the resulting file in a new always-visible right-hand sidebar that auto-refreshes within ~50 ms of Claude writing to it.

**Architecture:** Adds a checkbox to `StartClaudeDialog` that flows through `enterClaude(sid, renderer, bypass, templateId)` into the `enter_claude` WS frame. New `SummarySidebar` component lives as the third column of the workspace grid. It subscribes to `selectedSessionID` and `templateId`, fetches `GET /api/sessions/{sid}/summary` on changes, and re-fetches on `summary_updated` WS frames. A template-view toggle fetches `GET /api/templates/{id}`. A `localStorage`-persisted close button hides the sidebar globally and a re-open tab restores it.

**Tech Stack:** React 18, react-markdown + remark-gfm + react-syntax-highlighter (already in deps), Playwright for e2e.

**Depends on plans 01, 02, 03.** All backend support must be on `main`.

After this plan: full v1 of the summary-template feature is shipped. Playwright e2e covers the happy path end-to-end with a synthetic file write.

---

### Task 1: WS protocol typing — `summary_updated` outbound + `templateId` on enter_claude

**Files:**
- Modify: `web/src/lib/ws.ts`

- [ ] **Step 1: Add the inbound type**

Modify `web/src/lib/ws.ts`. In the `ServerMsg` union, add a new variant after the last one:

```ts
  | { type: 'summary_updated'; sessionID: string }
```

In the `ClientMsg` union, find the existing `enter_claude` variant and extend it with `templateId`:

```ts
  | { type: 'enter_claude'; sessionID: string; renderer?: ClaudeRenderer; bypassPermissions?: boolean; templateId?: string }
```

- [ ] **Step 2: Verify TS compiles**

Run: `cd web && npx tsc --noEmit`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/ws.ts
git commit -m "feat(ws-types): summary_updated server frame + templateId on enter_claude"
```

---

### Task 2: `enterClaude` callback carries `templateId`

**Files:**
- Modify: `web/src/features/sessions/useSessions.ts`

- [ ] **Step 1: Extend the callback signature**

Find the existing `enterClaude` `useCallback` in `useSessions.ts`:

```ts
  const enterClaude = useCallback(
    (sid: string, renderer?: 'tui' | 'ui', bypassPermissions?: boolean) => {
      socket.send({ type: 'enter_claude', sessionID: sid, renderer, bypassPermissions })
    },
    [socket],
  )
```

Replace with:

```ts
  const enterClaude = useCallback(
    (sid: string, renderer?: 'tui' | 'ui', bypassPermissions?: boolean, templateId?: string) => {
      socket.send({ type: 'enter_claude', sessionID: sid, renderer, bypassPermissions, templateId })
    },
    [socket],
  )
```

- [ ] **Step 2: TS check**

Run: `cd web && npx tsc --noEmit`
Expected: WorkspacePage.tsx will now have a type error because it calls `enterClaude(id, renderer, bypass)` — leave that for Task 4 to fix; for now skip if it's the only error.

Actually let's keep TS clean per commit: bridge it by accepting an undefined fourth arg (already valid). The signature is backward-compatible (`templateId?` is optional). TS should be clean.

Run: `cd web && npx tsc --noEmit`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add web/src/features/sessions/useSessions.ts
git commit -m "feat(useSessions): enterClaude forwards templateId to WS"
```

---

### Task 3: StartClaudeDialog — new checkbox

**Files:**
- Modify: `web/src/features/sessions/StartClaudeDialog.tsx`
- Modify: `web/src/features/sessions/StartClaudeDialog.css` (reuse existing checkbox class)

- [ ] **Step 1: Add state + onStart signature**

Find the existing state block in `StartClaudeDialog.tsx`:

```ts
  const [bypass, setBypass] = useState(true)
```

Add another state line right under it:

```ts
  // Task-summary template default ON for Chat UI. Disabled (and
  // visually muted) when the renderer is TUI, since TUI users
  // already have claude's own /memory machinery.
  const [summary, setSummary] = useState(true)
```

Change the `Props` interface's `onStart` to carry the templateId:

```ts
interface Props {
  defaultRenderer?: ClaudeRenderer
  onStart: (renderer: ClaudeRenderer, bypassPermissions: boolean, templateId: string) => void
  onCancel: () => void
}
```

Update the Enter-key effect's `onStart` call:

```ts
      if (e.key === 'Enter') onStart(renderer, bypass, summary && renderer === 'ui' ? 'summary-todo' : '')
```

Update the dependency array of that useEffect to include `summary`.

Update the Start button's `onClick`:

```ts
            onClick={() => onStart(renderer, bypass, summary && renderer === 'ui' ? 'summary-todo' : '')}
```

- [ ] **Step 2: Add the checkbox markup**

Find the existing bypass checkbox `<label className="start-claude__checkbox">`. Immediately after its closing `</label>`, add:

```tsx
        <label className={`start-claude__checkbox ${renderer === 'tui' ? 'is-disabled' : ''}`}>
          <input
            type="checkbox"
            checked={summary && renderer === 'ui'}
            onChange={(e) => setSummary(e.target.checked)}
            disabled={renderer === 'tui'}
          />
          <div>
            <div className="start-claude__checkbox-title">
              Maintain a task summary
            </div>
            <div className="start-claude__checkbox-desc">
              After every reply, Claude updates a short summary you
              can read in the right sidebar. Lets you pick up where
              you left off without re-explaining yourself.
              {renderer === 'tui' && ' (Chat UI only.)'}
            </div>
          </div>
        </label>
```

- [ ] **Step 3: Tiny CSS tweak for disabled state**

Append to `StartClaudeDialog.css`:

```css
.start-claude__checkbox.is-disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
```

- [ ] **Step 4: TS check**

Run: `cd web && npx tsc --noEmit`
Expected: error at WorkspacePage's `onStart` callback — leave for Task 4.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/StartClaudeDialog.tsx web/src/features/sessions/StartClaudeDialog.css
git commit -m "feat(StartClaudeDialog): summary-template opt-in checkbox"
```

---

### Task 4: WorkspacePage wires the new onStart arg

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`

- [ ] **Step 1: Update the StartClaudeDialog usage**

Find:

```tsx
          onStart={(renderer: ClaudeRenderer, bypass: boolean) => {
            const id = startClaudeFor
            setStartClaudeFor(null)
            s.enterClaude(id, renderer, bypass)
          }}
```

Replace with:

```tsx
          onStart={(renderer: ClaudeRenderer, bypass: boolean, templateId: string) => {
            const id = startClaudeFor
            setStartClaudeFor(null)
            s.enterClaude(id, renderer, bypass, templateId || undefined)
          }}
```

- [ ] **Step 2: TS check**

Run: `cd web && npx tsc --noEmit`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add web/src/features/sessions/WorkspacePage.tsx
git commit -m "feat(WorkspacePage): forward templateId from StartClaudeDialog"
```

---

### Task 5: Persist `templateId` in per-session state

**Files:**
- Modify: `web/src/features/sessions/types.ts` (add `templateId` to `PerSessionState`)
- Modify: `web/src/features/sessions/claudeReducer.ts` or `sessionsReducer.ts` (set/clear on relevant frames)

- [ ] **Step 1: Where templateId comes from in the protocol**

The backend currently does NOT echo `templateId` back in `claude_entered` or `idle` / `reattach`. For v1 the frontend's source of truth is the local state set when the user clicks Start in the dialog. We piggyback on the existing optimistic flow:

- When `enterClaude(sid, renderer, bypass, templateId)` is called by WorkspacePage, **also** stash `templateId` into perSession state.

- [ ] **Step 2: Add the field**

Modify `web/src/features/sessions/types.ts`. In `PerSessionState`, after the existing `renderer?` field, add:

```ts
  templateId?: string
```

Update `emptyPerSessionState`:

```ts
export function emptyPerSessionState(): PerSessionState {
  return { running: null, messages: [], messagesLoaded: false, mode: 'shell', renderer: '', templateId: '' }
}
```

- [ ] **Step 3: Set it from enterClaude**

Modify `web/src/features/sessions/useSessions.ts`. Find `enterClaude`. Before the `socket.send`, set the local state:

```ts
  const enterClaude = useCallback(
    (sid: string, renderer?: 'tui' | 'ui', bypassPermissions?: boolean, templateId?: string) => {
      setPerSession((prev) => {
        const next = new Map(prev)
        const cur = next.get(sid) ?? emptyPerSessionState()
        next.set(sid, { ...cur, templateId: templateId ?? '' })
        return next
      })
      socket.send({ type: 'enter_claude', sessionID: sid, renderer, bypassPermissions, templateId })
    },
    [socket],
  )
```

- [ ] **Step 4: Clear it on `claude_exited`**

Find the existing `case 'claude_exited':` in `claudeReducer.ts`. After clearing renderer / running, also clear templateId:

```ts
        templateId: '',
```

(Insert into the spread of fields being written into the session entry.)

- [ ] **Step 5: TS check + vitest**

Run:
```bash
cd web && npx tsc --noEmit && npm test
```
Expected: clean compile, 76/76 vitest pass.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/sessions/types.ts web/src/features/sessions/useSessions.ts web/src/features/sessions/claudeReducer.ts
git commit -m "feat(state): persist templateId per session, clear on claude_exited"
```

---

### Task 6: `summary_updated` reducer hook + summary fetch counter

**Files:**
- Modify: `web/src/features/sessions/types.ts` (add `summaryFetchCounter?: number`)
- Modify: `web/src/features/sessions/claudeReducer.ts` (handle frame)

- [ ] **Step 1: Add a counter to PerSessionState**

In `types.ts`, after `templateId?`, add:

```ts
  // Bumped every time backend emits summary_updated. Components
  // depend on this counter and re-fetch when it changes. We use
  // a counter rather than the file body so the component owns
  // its loading state and dedupes its own fetches.
  summaryFetchCounter?: number
```

Update `emptyPerSessionState`:

```ts
  return { running: null, messages: [], messagesLoaded: false, mode: 'shell', renderer: '', templateId: '', summaryFetchCounter: 0 }
```

- [ ] **Step 2: Handle the frame**

In `claudeReducer.ts` (or `sessionsReducer.ts`, wherever generic frames go — `summary_updated` does not require Claude state), add a `case 'summary_updated':` branch in `reducePerSession`:

```ts
    case 'summary_updated': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, {
        ...cur,
        summaryFetchCounter: (cur.summaryFetchCounter ?? 0) + 1,
      })
      return { perSession: next }
    }
```

- [ ] **Step 3: Vitest for the new case**

Add to `sessionsReducer.test.ts`:

```ts
  it('summary_updated bumps summaryFetchCounter for the right session', () => {
    const r1 = reducePerSession(new Map(), { type: 'summary_updated', sessionID: 'A' }, b64decode)
    expect(r1.perSession.get('A')?.summaryFetchCounter).toBe(1)
    const r2 = reducePerSession(r1.perSession, { type: 'summary_updated', sessionID: 'A' }, b64decode)
    expect(r2.perSession.get('A')?.summaryFetchCounter).toBe(2)
    // Untouched session unaffected.
    expect(r2.perSession.get('B')).toBeUndefined()
  })
```

- [ ] **Step 4: Run vitest**

Run: `cd web && npm test`
Expected: 77/77 pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/types.ts web/src/features/sessions/claudeReducer.ts web/src/features/sessions/sessionsReducer.test.ts
git commit -m "feat(reducer): summary_updated bumps per-session fetch counter"
```

---

### Task 7: API helpers

**Files:**
- Modify: `web/src/lib/api.ts` (add `getSummary`, `getTemplate`)

- [ ] **Step 1: Add the two GETs**

Append to `web/src/lib/api.ts`:

```ts
// GET /api/sessions/{sid}/summary
// Returns:
//   - { ok: true, body: string }  on 200 (body may be empty — caller treats empty same as 404)
//   - { ok: false }                on 404
//   - throws                       on other errors
export async function getSummary(sessionID: string): Promise<{ ok: boolean; body: string }> {
  const r = await fetch(`/api/sessions/${encodeURIComponent(sessionID)}/summary`, {
    headers: { Authorization: `Bearer ${authToken()}` },
  })
  if (r.status === 404) return { ok: false, body: '' }
  if (!r.ok) throw new Error(`getSummary: HTTP ${r.status}`)
  const body = await r.text()
  return { ok: true, body }
}

// GET /api/templates/{id}
export async function getTemplate(id: string): Promise<string> {
  const r = await fetch(`/api/templates/${encodeURIComponent(id)}`, {
    headers: { Authorization: `Bearer ${authToken()}` },
  })
  if (!r.ok) throw new Error(`getTemplate: HTTP ${r.status}`)
  return await r.text()
}
```

If `authToken()` is named differently in `api.ts`, mirror existing patterns (e.g. some files read directly from localStorage). Use whatever the file's other helpers use.

- [ ] **Step 2: TS check**

Run: `cd web && npx tsc --noEmit`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "feat(api): getSummary + getTemplate helpers"
```

---

### Task 8: `SummarySidebar` component

**Files:**
- Create: `web/src/features/sessions/SummarySidebar.tsx`
- Create: `web/src/features/sessions/SummarySidebar.css`

- [ ] **Step 1: Component skeleton**

Create `web/src/features/sessions/SummarySidebar.tsx`:

```tsx
import { useEffect, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getSummary, getTemplate } from '../../lib/api'
import './SummarySidebar.css'

interface Props {
  sessionID: string | null
  templateId: string | undefined
  fetchCounter: number
  onClose: () => void
}

type View = 'summary' | 'template'

// SummarySidebar lives as the third column of the Chat-UI workspace.
// Always visible unless the user clicked the × (then it's hidden
// across all sessions; the re-open tab in WorkspacePage restores
// it). Switches between two views: the current summary file body
// (default) and the raw template text the user opted in to.
export function SummarySidebar({ sessionID, templateId, fetchCounter, onClose }: Props) {
  const [view, setView] = useState<View>('summary')
  const [summary, setSummary] = useState<{ ok: boolean; body: string } | null>(null)
  const [template, setTemplate] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  // Fetch summary on session change OR fetchCounter bump.
  useEffect(() => {
    if (!sessionID) { setSummary(null); return }
    let cancel = false
    setErr(null)
    getSummary(sessionID).then((s) => {
      if (cancel) return
      setSummary(s)
    }).catch((e) => {
      if (cancel) return
      setErr(String(e))
    })
    return () => { cancel = true }
  }, [sessionID, fetchCounter])

  // Fetch template content lazily, only when the user toggles to
  // the template view AND it hasn't been fetched yet.
  useEffect(() => {
    if (view !== 'template' || !templateId || template != null) return
    let cancel = false
    getTemplate(templateId).then((t) => { if (!cancel) setTemplate(t) })
                          .catch((e) => { if (!cancel) setErr(String(e)) })
    return () => { cancel = true }
  }, [view, templateId, template])

  const hasTemplate = !!templateId
  const body = view === 'summary' ? (summary?.body ?? '') : (template ?? '')
  const isEmpty = body.trim() === ''

  return (
    <aside className="summary-sidebar" aria-label="Session summary">
      <header className="summary-sidebar__header">
        <button
          type="button"
          className="summary-sidebar__view-toggle"
          onClick={() => setView(view === 'summary' ? 'template' : 'summary')}
          disabled={!hasTemplate}
          title={hasTemplate ? 'Switch view' : 'Enable the summary template to see content here'}
        >
          {view === 'summary' ? 'Summary' : 'Template'}
          {hasTemplate && <span className="summary-sidebar__chev"> ›</span>}
        </button>
        <button
          type="button"
          className="summary-sidebar__close"
          onClick={onClose}
          aria-label="Hide summary sidebar"
        >×</button>
      </header>
      <div className="summary-sidebar__body">
        {err && <div className="summary-sidebar__error">{err}</div>}
        {!hasTemplate && view === 'summary' && (
          <div className="summary-sidebar__empty">
            Summary tracking is off for this session. Start a new
            Chat UI session and check the box to enable it.
          </div>
        )}
        {hasTemplate && view === 'summary' && (summary == null || !summary.ok || isEmpty) && (
          <div className="summary-sidebar__empty">
            No summary yet — send your first prompt and Claude will
            populate this.
          </div>
        )}
        {!isEmpty && (
          <div className="summary-sidebar__md">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{body}</ReactMarkdown>
          </div>
        )}
      </div>
    </aside>
  )
}
```

- [ ] **Step 2: Sidebar CSS**

Create `web/src/features/sessions/SummarySidebar.css`:

```css
.summary-sidebar {
  width: 280px;
  border-left: 1px solid rgba(255, 255, 255, 0.06);
  background: var(--bg, #0f1116);
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.summary-sidebar__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}

.summary-sidebar__view-toggle {
  background: transparent;
  border: none;
  color: var(--fg, #eaeaea);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 4px;
}
.summary-sidebar__view-toggle:hover:not(:disabled) { background: rgba(255, 255, 255, 0.05); }
.summary-sidebar__view-toggle:disabled { opacity: 0.5; cursor: not-allowed; }
.summary-sidebar__chev { color: var(--fg-muted, #9aa3b2); }

.summary-sidebar__close {
  background: transparent;
  border: none;
  color: var(--fg-muted, #9aa3b2);
  font-size: 16px;
  cursor: pointer;
  padding: 0 6px;
}
.summary-sidebar__close:hover { color: var(--fg, #eaeaea); }

.summary-sidebar__body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px;
  font-size: 13px;
  line-height: 1.55;
}

.summary-sidebar__empty,
.summary-sidebar__error {
  color: var(--fg-muted, #9aa3b2);
  font-size: 12.5px;
  line-height: 1.5;
}
.summary-sidebar__error { color: #e88c8c; }

.summary-sidebar__md > *:first-child { margin-top: 0; }
.summary-sidebar__md > *:last-child { margin-bottom: 0; }
.summary-sidebar__md h2 { font-size: 13px; margin: 10px 0 4px 0; }
.summary-sidebar__md ul { padding-left: 18px; margin: 4px 0 8px 0; }
.summary-sidebar__md li { margin: 2px 0; }
```

- [ ] **Step 3: TS check + vitest**

Run: `cd web && npx tsc --noEmit && npm test`
Expected: clean, all vitest pass.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/SummarySidebar.tsx web/src/features/sessions/SummarySidebar.css
git commit -m "feat(SummarySidebar): summary + template view with re-fetch on counter bump"
```

---

### Task 9: Mount SummarySidebar into the workspace grid

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`
- Modify: `web/src/features/sessions/WorkspacePage.css` (grid columns)

- [ ] **Step 1: Mount + hidden-state localStorage**

Modify `web/src/features/sessions/WorkspacePage.tsx`.

Add an import:

```ts
import { SummarySidebar } from './SummarySidebar'
```

Add a state hook near the other `useState`s in the component:

```ts
  const [summaryHidden, setSummaryHidden] = useState(
    () => localStorage.getItem('alfred_summary_sidebar_hidden') === '1'
  )
```

Just before the closing `</div>` of `.workspace`, render the sidebar conditionally (only in Chat UI mode):

```tsx
        {selected && ps && ps.mode === 'claude' && ps.renderer === 'ui' && !summaryHidden && (
          <SummarySidebar
            sessionID={selected.id}
            templateId={ps.templateId}
            fetchCounter={ps.summaryFetchCounter ?? 0}
            onClose={() => {
              localStorage.setItem('alfred_summary_sidebar_hidden', '1')
              setSummaryHidden(true)
            }}
          />
        )}
        {selected && ps && ps.mode === 'claude' && ps.renderer === 'ui' && summaryHidden && (
          <button
            type="button"
            className="workspace__summary-reopen"
            title="Show summary sidebar"
            onClick={() => {
              localStorage.removeItem('alfred_summary_sidebar_hidden')
              setSummaryHidden(false)
            }}
          >
            ◂
          </button>
        )}
```

- [ ] **Step 2: Grid CSS**

Modify `web/src/features/sessions/WorkspacePage.css`. Find the existing `.workspace` rule with `grid-template-columns: ...`. Change it to:

```css
.workspace {
  display: grid;
  grid-template-columns: 230px 1fr auto;
  height: 100vh;
}
```

The `auto` column holds the SummarySidebar (it's 280px wide), or the reopen tab (small), or collapses if neither is present (e.g. shell mode).

Append:

```css
.workspace__summary-reopen {
  position: fixed;
  right: 0;
  top: 50%;
  transform: translateY(-50%);
  background: var(--bg-elevated, #14171d);
  color: var(--fg-muted, #9aa3b2);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-right: none;
  border-radius: 4px 0 0 4px;
  padding: 18px 4px;
  font-size: 13px;
  cursor: pointer;
  z-index: 30;
}
.workspace__summary-reopen:hover { color: var(--fg, #eaeaea); }
```

- [ ] **Step 3: TS check + vitest**

Run: `cd web && npx tsc --noEmit && npm test`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/WorkspacePage.tsx web/src/features/sessions/WorkspacePage.css
git commit -m "feat(WorkspacePage): mount SummarySidebar + hidden-state localStorage"
```

---

### Task 10: Playwright e2e — happy path

**Files:**
- Modify: `web/e2e/regression.spec.ts` (append a new describe)

- [ ] **Step 1: Write the test**

Append to `regression.spec.ts`:

```ts
test.describe('Summary template — full round-trip', () => {
  test('opt-in → write file from server side → sidebar updates within 2s', async ({ page, request }) => {
    test.setTimeout(90_000)

    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-summary')
    await loginUI(page, tok)
    await selectSession(page, sid)

    // Open Start Claude, ensure summary checkbox is present + default
    // checked, click Start.
    await page.locator('.workspace__claude-btn').click()
    await expect(page.locator('text=Start Claude')).toBeVisible()
    const summaryBox = page.locator('.start-claude__checkbox input[type="checkbox"]').nth(1)
    await expect(summaryBox).toBeChecked()
    await page.locator('label:has-text("Chat UI")').click()
    await page.locator('button:has-text("Start")').click()
    await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 5_000 })

    // The sidebar should appear in the "empty" state.
    const sidebar = page.locator('.summary-sidebar')
    await expect(sidebar).toBeVisible()
    await expect(sidebar).toContainText('No summary yet')

    // Simulate Claude writing the summary file by writing it
    // server-side via the WS probe pattern: a synthetic write.
    // We bypass Claude entirely and just touch the file at
    // <DATA_DIR>/summaries/<sid>.md. The server's fsnotify watcher
    // pushes summary_updated; the frontend re-fetches.
    //
    // The dev server's ALFRED_DATA_DIR is /tmp/alfred-dev/data
    // (from the README's run-local instructions). We exercise the
    // synthetic-write path through the existing /tmp test rig.
    await page.request.post(`${BACKEND}/api/test/summary-write`, {
      headers: { Authorization: `Bearer ${tok}` },
      data: { sessionID: sid, body: '## Goal\nbuild a fizzbuzz\n\n## Status\nin progress' },
    }).catch(() => {
      // This endpoint does NOT exist in production; we add a
      // test-only handler gated by env. If you don't want the
      // endpoint, swap this for a Node fs.writeFile via
      // child_process.exec — see the alternative below.
    })

    // Wait up to 5s for the sidebar to flip to populated state.
    await expect(sidebar).toContainText('build a fizzbuzz', { timeout: 5_000 })

    await page.screenshot({ path: path.join(SHOTS, 'summary-sidebar-populated.png'), fullPage: true })
  })

  test('close × hides, re-open tab restores, persists across reload', async ({ page }) => {
    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-summary-hide')
    await loginUI(page, tok)
    await selectSession(page, sid)

    // Boot into Chat UI with the default template on.
    await page.locator('.workspace__claude-btn').click()
    await expect(page.locator('text=Start Claude')).toBeVisible()
    await page.locator('label:has-text("Chat UI")').click()
    await page.locator('button:has-text("Start")').click()
    await expect(page.locator('.summary-sidebar')).toBeVisible()

    // Click ×.
    await page.locator('.summary-sidebar__close').click()
    await expect(page.locator('.summary-sidebar')).toHaveCount(0)
    await expect(page.locator('.workspace__summary-reopen')).toBeVisible()

    // Reload — state persists in localStorage.
    await page.reload()
    await expect(page.locator('.workspace__summary-reopen')).toBeVisible()
    await expect(page.locator('.summary-sidebar')).toHaveCount(0)

    // Click the re-open tab.
    await page.locator('.workspace__summary-reopen').click()
    await expect(page.locator('.summary-sidebar')).toBeVisible()
  })
})
```

If the production server doesn't expose `/api/test/summary-write` (it doesn't), use this alternative for the first test instead of the page.request.post:

```ts
    // Write the file directly on the test runner's disk.
    // ALFRED_DATA_DIR is /tmp/alfred-dev/data on the dev rig.
    await fs.promises.mkdir('/tmp/alfred-dev/data/summaries', { recursive: true })
    await fs.promises.writeFile(
      `/tmp/alfred-dev/data/summaries/${sid}.md`,
      '## Goal\nbuild a fizzbuzz\n\n## Status\nin progress',
    )
```

(Add `import fs from 'node:fs'` at the top of the file if not present.)

- [ ] **Step 2: Run only the new tests**

Make sure alfred-server + Vite are running, then:

```bash
cd web && npx playwright test --grep "Summary template" --reporter=line
```

Expected: 2/2 PASS.

- [ ] **Step 3: Run the whole regression**

Run: `cd web && npx playwright test --reporter=line`
Expected: all PASS (11 prior + 2 new = 13).

- [ ] **Step 4: Commit**

```bash
git add web/e2e/regression.spec.ts
git commit -m "test(e2e): summary sidebar populates on file write + close/reopen persists"
```

---

### Final smoke

- [ ] **Step 1: Run everything**

```bash
go test ./...                # backend unit
cd web && npm test           # vitest
npx playwright test --reporter=line   # e2e
```

Expected: all PASS.

- [ ] **Step 2: Manual touch — verify the UX end-to-end**

1. Start dev server + Vite.
2. Open http://localhost:5173, log in.
3. + New chat, pick a session, click Claude.
4. Confirm the dialog has both checkboxes; "Maintain a task summary" defaults checked.
5. Click Start (Chat UI + both on).
6. Confirm right sidebar appears with "No summary yet".
7. Send: "build a fizzbuzz in python, three turns".
8. After Claude replies, sidebar shows the summary structure.
9. Click the chevron — switch to Template view. Read-only, shows the raw template with `<sid>` / `<summary_path>` placeholders.
10. Click × → sidebar gone, re-open tab appears. Reload — still gone. Click tab → back.

- [ ] **Step 3: Push**

```bash
git push origin main
```
