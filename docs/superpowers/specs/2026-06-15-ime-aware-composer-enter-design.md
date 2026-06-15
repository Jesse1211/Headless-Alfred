## IME-aware composer Enter

Both composers in the web UI (`CommandInput.tsx` for shell mode and the
inline `<textarea>` in `ClaudeChatView.tsx` for Claude UI mode) treat
`Enter` without `Shift` as "send". This breaks for users typing with an
IME (Chinese, Japanese, Korean), because the same Enter keypress also
commits the in-progress IME composition. The current code submits a
half-finished message and clears the textarea before the IME has even
finished its commit.

### Fix

Both composers gate their submit-on-Enter on the **`isComposing` flag**
exposed by the underlying browser `KeyboardEvent`. While the IME is
composing — including the keydown that commits the selection — this
flag is `true` and the browser also reports `keyCode === 229`. Reading
it via React is `e.nativeEvent.isComposing`. This is the standard
React solution; works in Chrome / Safari / Firefox.

### Shared helper

A new file `web/src/lib/keyboard.ts` exports one function:

```ts
// isSubmitKey returns true iff this keydown event represents
// "send the form": Enter, without Shift, and NOT in the middle of
// an IME composition.
//
// The isComposing check matters for Chinese / Japanese / Korean
// users: the same Enter that submits their IME selection would
// otherwise also fire a submit and ship a half-finished message.
export function isSubmitKey(e: React.KeyboardEvent): boolean {
  return e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing
}
```

Both call sites change from:

```ts
if (e.key === 'Enter' && !e.shiftKey) { ... }
```

to:

```ts
if (isSubmitKey(e)) { ... }
```

### Files

| file | change |
|---|---|
| `web/src/lib/keyboard.ts` | new — exports `isSubmitKey` |
| `web/src/features/terminal/CommandInput.tsx` | replace inline check in `onKey` |
| `web/src/features/sessions/ClaudeChatView.tsx` | replace inline check in textarea `onKeyDown` |
| `web/src/lib/keyboard.test.ts` | new — unit test the helper |

### Testing

Unit tests around `isSubmitKey` (jsdom doesn't natively model IME
composition, so we construct synthetic events):

| input | expect |
|---|---|
| `{ key: 'Enter', shiftKey: false, nativeEvent: { isComposing: false } }` | `true` |
| `{ key: 'Enter', shiftKey: true,  nativeEvent: { isComposing: false } }` | `false` |
| `{ key: 'Enter', shiftKey: false, nativeEvent: { isComposing: true  } }` | `false` |
| `{ key: 'a',     shiftKey: false, nativeEvent: { isComposing: false } }` | `false` |

No e2e — Playwright doesn't have a reliable IME-simulation API. The
unit test above covers the logic; manual verification by the user
(typing 中文 in the composer, hitting Enter to commit a candidate, and
confirming the message is NOT sent) is the acceptance test.

### Out of scope

- Other inputs that also use Enter (`SessionsSidebar` rename input,
  credentials dialogs): they receive single short ASCII names /
  passwords, IME usage is implausible. Skip — YAGNI.
- Custom IME-state event handling via `compositionstart` /
  `compositionend`: the native `isComposing` flag covers it.
