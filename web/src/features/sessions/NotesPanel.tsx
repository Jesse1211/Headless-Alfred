import { useCallback, useEffect, useRef, useState } from 'react'
import { getNote, putNote } from '../../lib/api'
import { useDocumentSync } from '../../lib/useDocumentSync'
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
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  // Snapshot the "last server-side value" so we can avoid re-PUTting
  // when text == server (the read effect just hydrated us).
  const lastPushedRef = useRef<string>('')

  const fetcher = useCallback(() => getNote(sessionID), [sessionID])
  const { data, error: fetchError, firstFetchPending } = useDocumentSync<string>(
    fetcher,
    [sessionID, noteFetchCounter],
    {
      // Refuse server echo while the user is mid-typing — local text
      // is more current; the next debounced PUT will catch server up.
      skipIf: () => document.activeElement === taRef.current,
    },
  )

  // Hydrate local text from the hook's data on first-fetch and on
  // any accepted refetch (skipIf gated those during typing).
  useEffect(() => {
    if (data === undefined) return
    setText(data)
    lastPushedRef.current = data
  }, [data])

  const loaded = !firstFetchPending

  // Debounced save.
  useEffect(() => {
    if (!loaded) return
    if (text === lastPushedRef.current) return
    const handle = setTimeout(() => {
      putNote(sessionID, text)
        .then(() => {
          lastPushedRef.current = text
          setSavedAt(Date.now())
          setSaveError(null)
        })
        .catch((e) => {
          setSaveError(e instanceof Error ? e.message : String(e))
        })
    }, SAVE_DEBOUNCE_MS)
    return () => clearTimeout(handle)
  }, [text, sessionID, loaded])

  const onChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setText(e.target.value)
  }, [])

  const displayedError = saveError ?? fetchError

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
        {displayedError && <span className="notes-panel__error">{displayedError}</span>}
        {!displayedError && savedAt && (
          <span className="notes-panel__saved">Saved · {new Date(savedAt).toLocaleTimeString()}</span>
        )}
        {!displayedError && !savedAt && loaded && (
          <span className="notes-panel__hint">Autosaves while you type</span>
        )}
      </div>
    </div>
  )
}
