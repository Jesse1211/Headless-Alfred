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
