## IME-aware composer Enter

Both composers in the web UI (`CommandInput.tsx` for shell mode and the
inline `<textarea>` in `ClaudeChatView.tsx` for Claude UI mode) treat
`Enter` without `Shift` as "send". This breaks for users typing with an
IME (Chinese, Japanese, Korean), because the same Enter keypress also
commits the in-progress IME composition. The current code submits a
half-finished message and clears the textarea before the IME has even
finished its commit.

### Fix

All three keyboard-driven submit sites gate their Enter on the
**`isComposing` flag** exposed by the underlying browser
`KeyboardEvent`. While the IME is composing — including the keydown
that commits the selection — this flag is `true` and the browser also
reports `keyCode === 229`. Reading it via React is
`e.nativeEvent.isComposing`. This is the standard React solution;
works in Chrome 90+, Firefox 116+, Safari 17+ (older Safari has a
known bug where the commit-frame Enter reports `isComposing: false`,
but the user has Safari 17+ already).

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
| `web/src/features/sessions/SessionsSidebar.tsx` | replace inline check in rename input's `onKey` |
| `web/src/lib/keyboard.test.ts` | new — unit test the helper |

The SessionsSidebar rename input is included because users may give
a session a non-ASCII name (e.g. "复盘测试"). The credentials dialogs
are NOT included — passwords and OAuth tokens are not entered via an
IME in any plausible scenario.

### Testing

Unit tests on `isSubmitKey`. jsdom's synthetic React events don't
populate `nativeEvent.isComposing` by default, so the test constructs
a real DOM `KeyboardEvent` and wraps it in a `React.KeyboardEvent`
shape:

```ts
function fakeKeyEvent(opts: {
  key: string
  shiftKey?: boolean
  isComposing?: boolean
}): React.KeyboardEvent {
  const native = new KeyboardEvent('keydown', {
    key: opts.key,
    shiftKey: opts.shiftKey ?? false,
    // isComposing is read-only on real KeyboardEvent constructor; the
    // option exists in the spec but jsdom doesn't honor it. Fall back
    // to defineProperty after construction.
    isComposing: opts.isComposing ?? false,
  })
  if (opts.isComposing && !native.isComposing) {
    Object.defineProperty(native, 'isComposing', { value: true })
  }
  return { nativeEvent: native, key: native.key, shiftKey: native.shiftKey } as React.KeyboardEvent
}
```

| input | expect |
|---|---|
| `fakeKeyEvent({ key: 'Enter' })` | `true` |
| `fakeKeyEvent({ key: 'Enter', shiftKey: true })` | `false` |
| `fakeKeyEvent({ key: 'Enter', isComposing: true })` | `false` |
| `fakeKeyEvent({ key: 'a' })` | `false` |

The `Object.defineProperty` fallback is the failsafe: if jsdom ever
starts honoring the constructor option, the test still passes; if it
doesn't, we forcibly set the property.

No e2e — Playwright doesn't have a reliable IME-simulation API. The
unit test above covers the logic; manual verification by the user
(typing 中文 in each of the three inputs, hitting Enter to commit a
candidate, and confirming nothing fires) is the acceptance test.

### Out of scope

- Custom IME-state event handling via `compositionstart` /
  `compositionend`: the native `isComposing` flag covers it.
- Old-Safari workarounds: covered by the browser-version baseline
  above; not in v1.
