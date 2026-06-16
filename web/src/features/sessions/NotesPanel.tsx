import { useCallback, useEffect, useRef, useState } from 'react'
import { getNote, putNote } from '../../lib/api'
import './NotesPanel.css'

interface Props {
  sessionID: string
  // Bumps when the backend reports a note_updated frame. The panel
  // refetches IF the textarea isn't currently focused (so a server
  // echo doesn't overwrite the user's mid-typing buffer).
  noteFetchCounter: number
}

const SAVE_DEBOUNCE_MS = 600

export function NotesPanel({ sessionID, noteFetchCounter }: Props) {
  const [text, setText] = useState<string>('')
  const [loaded, setLoaded] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  // Snapshot the "last server-side value" so we can avoid re-PUTting
  // when text == server (the read effect just hydrated us).
  const lastPushedRef = useRef<string>('')

  // Initial fetch + WS-driven refetch.
  //
  // Deps deliberately exclude `loaded` — including it would trigger a
  // second fetch the moment setLoaded(true) runs. The first fetch is
  // unconditional (loaded starts as false; we WANT it to override the
  // empty textarea). Subsequent fetches are gated by the focus check
  // so a server echo doesn't clobber the user's in-flight typing.
  useEffect(() => {
    let alive = true
    const isFirstFetch = !loaded
    getNote(sessionID)
      .then((body) => {
        if (!alive) return
        if (!isFirstFetch && document.activeElement === taRef.current) {
          // User is typing right now — skip the refetch. The local
          // text is more current; the next debounced PUT will catch
          // server up.
          return
        }
        setText(body)
        lastPushedRef.current = body
        setError(null)
      })
      .catch((e) => {
        if (!alive) return
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        if (!loaded) setLoaded(true)
      })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionID, noteFetchCounter])

  // Debounced save.
  useEffect(() => {
    if (!loaded) return
    if (text === lastPushedRef.current) return
    const handle = setTimeout(() => {
      putNote(sessionID, text)
        .then(() => {
          lastPushedRef.current = text
          setSavedAt(Date.now())
          setError(null)
        })
        .catch((e) => {
          setError(e instanceof Error ? e.message : String(e))
        })
    }, SAVE_DEBOUNCE_MS)
    return () => clearTimeout(handle)
  }, [text, sessionID, loaded])

  const onChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setText(e.target.value)
  }, [])

  return (
    <div className="notes-panel">
      <textarea
        ref={taRef}
        className="notes-panel__textarea"
        value={text}
        onChange={onChange}
        placeholder="Personal notes for this session. Markdown ok. Not sent to Claude."
        spellCheck={false}
      />
      <div className="notes-panel__footer">
        {error && <span className="notes-panel__error">{error}</span>}
        {!error && savedAt && (
          <span className="notes-panel__saved">Saved · {new Date(savedAt).toLocaleTimeString()}</span>
        )}
        {!error && !savedAt && loaded && (
          <span className="notes-panel__hint">Autosaves while you type</span>
        )}
      </div>
    </div>
  )
}
