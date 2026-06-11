import { FormEvent, KeyboardEvent, useState } from 'react'

interface Props {
  disabled: boolean
  busy: boolean
  onSubmit: (cmd: string) => void
  onStop: () => void
}

export default function CommandInput({ disabled, busy, onSubmit, onStop }: Props) {
  const [value, setValue] = useState('')

  function submit(e: FormEvent) {
    e.preventDefault()
    const v = value.trim()
    if (!v || disabled) return
    onSubmit(v)
    setValue('')
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit(e as unknown as FormEvent)
    }
  }

  return (
    <form className="command-input" onSubmit={submit}>
      <textarea
        rows={1}
        placeholder={busy ? 'Command is running…' : 'Type a command, Enter to run, Shift+Enter for newline'}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKey}
        disabled={disabled || busy}
      />
      {busy ? (
        <button type="button" className="command-input__stop" onClick={onStop}>Stop</button>
      ) : (
        <button type="submit" disabled={disabled || !value.trim()}>Run</button>
      )}
    </form>
  )
}
