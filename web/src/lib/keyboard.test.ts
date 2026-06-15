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
