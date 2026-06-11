import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react'

interface Props {
  disabled: boolean
  busy: boolean
  onSubmit: (cmd: string) => void
  onStop: () => void
}

export default function CommandInput({ disabled, busy, onSubmit, onStop }: Props) {
  const [value, setValue] = useState('')
  const taRef = useRef<HTMLTextAreaElement>(null)

  // Autosize the textarea up to a cap.
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = Math.min(ta.scrollHeight, 200) + 'px'
  }, [value])

  function submit(e: FormEvent) {
    e.preventDefault()
    const v = value.trim()
    if (!v || disabled || busy) return
    onSubmit(v)
    setValue('')
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit(e as unknown as FormEvent)
    }
  }

  const sendDisabled = disabled || !value.trim()

  return (
    <form className="composer" onSubmit={submit}>
      <textarea
        ref={taRef}
        rows={1}
        placeholder={busy ? 'Command is running…' : 'Type a command'}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKey}
        disabled={disabled && !busy}
      />
      {busy ? (
        <button
          type="button"
          className="composer__btn composer__btn--stop"
          onClick={onStop}
          aria-label="Stop"
          title="Stop"
        >
          <span className="composer__stop-icon" />
        </button>
      ) : (
        <button
          type="submit"
          className="composer__btn composer__btn--send"
          disabled={sendDisabled}
          aria-label="Send"
          title="Send"
        >
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
            <path d="M8 13V3M8 3L3.5 7.5M8 3l4.5 4.5" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </button>
      )}
    </form>
  )
}
