# IME-aware Composer Enter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the three keyboard-driven Enter-to-submit sites from firing while a Chinese/Japanese/Korean IME is committing a composition.

**Architecture:** New shared helper `isSubmitKey(e)` in `web/src/lib/keyboard.ts` checks Enter + !shift + !`nativeEvent.isComposing`. Replace inline checks in the three call sites with calls to the helper. One unit-test file covers the helper directly.

**Tech Stack:** TypeScript, React 18, Vitest, jsdom.

---

## File Structure

| file | role | task |
|---|---|---|
| `web/src/lib/keyboard.ts` | new — exports `isSubmitKey` | T1 |
| `web/src/lib/keyboard.test.ts` | new — vitest unit tests | T1 |
| `web/src/features/terminal/CommandInput.tsx` | replace inline check in `onKey` | T2 |
| `web/src/features/sessions/ClaudeChatView.tsx` | replace inline check in textarea `onKeyDown` | T2 |
| `web/src/features/sessions/SessionsSidebar.tsx` | replace inline check in rename input's `onKey` | T2 |

Two tasks: helper+tests first (TDD), then the three call sites in one commit.

---

### Task 1: `isSubmitKey` helper + unit tests

**Files:**
- Create: `web/src/lib/keyboard.ts`
- Test: `web/src/lib/keyboard.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
// web/src/lib/keyboard.test.ts
import { describe, it, expect } from 'vitest'
import { isSubmitKey } from './keyboard'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'

// fakeKeyEvent constructs a real DOM KeyboardEvent and wraps it in a
// shape compatible with React.KeyboardEvent. Necessary because jsdom
// doesn't populate `isComposing` from synthetic React events, and
// real KeyboardEvent's `isComposing` is read-only — so we use
// defineProperty as a failsafe.
function fakeKeyEvent(opts: {
  key: string
  shiftKey?: boolean
  isComposing?: boolean
}): ReactKeyboardEvent {
  const native = new KeyboardEvent('keydown', {
    key: opts.key,
    shiftKey: opts.shiftKey ?? false,
  })
  if (opts.isComposing) {
    Object.defineProperty(native, 'isComposing', { value: true })
  }
  return {
    nativeEvent: native,
    key: native.key,
    shiftKey: native.shiftKey,
  } as ReactKeyboardEvent
}

describe('isSubmitKey', () => {
  it('returns true for plain Enter', () => {
    expect(isSubmitKey(fakeKeyEvent({ key: 'Enter' }))).toBe(true)
  })

  it('returns false for Shift+Enter', () => {
    expect(isSubmitKey(fakeKeyEvent({ key: 'Enter', shiftKey: true }))).toBe(false)
  })

  it('returns false when IME is composing', () => {
    expect(isSubmitKey(fakeKeyEvent({ key: 'Enter', isComposing: true }))).toBe(false)
  })

  it('returns false for non-Enter keys', () => {
    expect(isSubmitKey(fakeKeyEvent({ key: 'a' }))).toBe(false)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd web && npx vitest run keyboard
```

Expected: FAIL (`Cannot find module './keyboard'`).

- [ ] **Step 3: Implement the helper**

```ts
// web/src/lib/keyboard.ts
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'

// isSubmitKey returns true iff this keydown event represents
// "send the form": Enter, without Shift, and NOT in the middle of
// an IME composition.
//
// The isComposing check matters for Chinese / Japanese / Korean
// users: the same Enter that commits their IME selection would
// otherwise also fire a submit and ship a half-finished message.
export function isSubmitKey(e: ReactKeyboardEvent): boolean {
  return e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd web && npx vitest run keyboard
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/keyboard.ts web/src/lib/keyboard.test.ts
git commit -m "feat(keyboard): isSubmitKey helper guards Enter against IME composition"
```

---

### Task 2: Wire the helper into all three call sites

**Files:**
- Modify: `web/src/features/terminal/CommandInput.tsx`
- Modify: `web/src/features/sessions/ClaudeChatView.tsx`
- Modify: `web/src/features/sessions/SessionsSidebar.tsx`

All three sites change from `if (e.key === 'Enter' && !e.shiftKey)` to `if (isSubmitKey(e))`. The SessionsSidebar one is slightly different because Enter and Escape are both handled — we only IME-guard the Enter branch.

- [ ] **Step 1: `CommandInput.tsx`**

In `web/src/features/terminal/CommandInput.tsx`, the existing `onKey`:

```tsx
function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit(e as unknown as FormEvent)
  }
}
```

Replace with:

```tsx
function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
  if (isSubmitKey(e)) {
    e.preventDefault()
    submit(e as unknown as FormEvent)
  }
}
```

Add the import at the top of the file (alongside the existing React imports):

```tsx
import { isSubmitKey } from '../../lib/keyboard'
```

- [ ] **Step 2: `ClaudeChatView.tsx`**

In `web/src/features/sessions/ClaudeChatView.tsx`, find the textarea `onKeyDown` (around line 90):

```tsx
onKeyDown={(e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}}
```

Replace with:

```tsx
onKeyDown={(e) => {
  if (isSubmitKey(e)) {
    e.preventDefault()
    submit()
  }
}}
```

Add the import at the top:

```tsx
import { isSubmitKey } from '../../lib/keyboard'
```

- [ ] **Step 3: `SessionsSidebar.tsx`**

In `web/src/features/sessions/SessionsSidebar.tsx`, find the rename input's `onKey` (around line 76):

```tsx
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
```

Replace the Enter branch only (Escape stays untouched — Esc is never composed by an IME):

```tsx
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
```

Add the import at the top of the file:

```tsx
import { isSubmitKey } from '../../lib/keyboard'
```

Note: the rename input doesn't have Shift+Enter as a newline (it's a single-line input), so `isSubmitKey`'s `!e.shiftKey` is mildly more restrictive than the old check (used to accept Shift+Enter as commit). This is fine — Shift+Enter on a single-line input is a no-op-ish accident and shouldn't commit.

- [ ] **Step 4: TS check + full vitest**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + all tests pass (existing 82 + 4 new from Task 1 = 86).

- [ ] **Step 5: Commit**

```bash
git add web/src/features/terminal/CommandInput.tsx \
        web/src/features/sessions/ClaudeChatView.tsx \
        web/src/features/sessions/SessionsSidebar.tsx
git commit -m "fix(composer): use isSubmitKey to avoid IME-commit submits"
```

---

## Final verification

- [ ] **TS + vitest sweep**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean, all green.

- [ ] **Manual user acceptance (per spec, no automated e2e)**

In each of the three inputs:
1. Switch to Chinese IME.
2. Type a few pinyin keys, see the candidate popup.
3. Press Enter to commit a candidate.
4. Confirm: text is committed into the input, NOT submitted/sent/renamed.
5. Press Enter again (no IME active) → submit fires as expected.
