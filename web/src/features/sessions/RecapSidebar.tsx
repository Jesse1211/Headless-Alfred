import { useCallback, useEffect, useRef, useState } from 'react'
import { getRecap, listRecaps, RecapEntry } from '../../lib/api'
import { MarkdownView } from './MarkdownView'
import './RecapSidebar.css'

interface Props {
  // Bumps when the backend reports a recap_updated frame; the
  // sidebar re-fetches the date list and the currently-shown
  // content on every bump.
  recapFetchCounter: number
  // Fires the recap-daily template prompt as a claude_prompt to the
  // current (recap) Claude session. WorkspacePage supplies this.
  onGenerate: () => void
  // True iff Claude is currently mid-prompt in this session. Used
  // to disable the Generate button.
  generating: boolean
}

function todayLocal(): string {
  return new Date().toLocaleDateString('en-CA') // YYYY-MM-DD
}

export function RecapSidebar({ recapFetchCounter, onGenerate, generating }: Props) {
  const [list, setList] = useState<RecapEntry[]>([])
  const [listLoading, setListLoading] = useState(true)
  const [listError, setListError] = useState<string | null>(null)
  const todayDate = todayLocal()
  const [selectedDate, setSelectedDate] = useState<string>(todayDate)
  const [content, setContent] = useState<string>('')
  const [contentLoading, setContentLoading] = useState(false)
  const [contentError, setContentError] = useState<string | null>(null)

  // First-list-fetch shows a spinner; subsequent bumps are silent
  // to avoid flicker.
  const firstListFetchDone = useRef(false)
  useEffect(() => {
    let alive = true
    const showSpinner = !firstListFetchDone.current
    if (showSpinner) setListLoading(true)
    listRecaps()
      .then((entries) => {
        if (!alive) return
        setList(entries)
        setListError(null)
      })
      .catch((e) => {
        if (!alive) return
        setListError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        firstListFetchDone.current = true
        if (showSpinner) setListLoading(false)
      })
    return () => { alive = false }
  }, [recapFetchCounter])

  // Content fetch — refetch on selectedDate change AND on counter
  // bump (the just-updated date may be the selected one). Spinner
  // shows on EVERY refetch (not just first) because changing dates
  // would otherwise briefly show stale content. Not a useDocumentSync
  // fit for that reason.
  useEffect(() => {
    let alive = true
    setContentLoading(true)
    setContentError(null)
    getRecap(selectedDate)
      .then((text) => {
        if (!alive) return
        setContent(text)
      })
      .catch((e) => {
        if (!alive) return
        const status = (e as { status?: number })?.status
        if (status === 404) {
          setContent('')
          return
        }
        setContentError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        setContentLoading(false)
      })
    return () => { alive = false }
  }, [selectedDate, recapFetchCounter])

  const isToday = selectedDate === todayDate
  const hasTodayFile = list.some((e) => e.date === todayDate)
  const generateLabel = hasTodayFile ? "Refresh today's recap" : "Generate today's recap"

  const handleSelect = useCallback((date: string) => {
    setSelectedDate(date)
  }, [])

  return (
    <aside className="recap-sidebar" aria-label="Recap sidebar">
      <header className="recap-sidebar__header">
        <h2 className="recap-sidebar__title">Recap</h2>
      </header>
      {isToday && (
        <div className="recap-sidebar__generate-row">
          <button
            type="button"
            className="recap-sidebar__generate"
            onClick={onGenerate}
            disabled={generating}
          >
            {generating ? 'Generating…' : generateLabel}
          </button>
        </div>
      )}
      <div className="recap-sidebar__list">
        {listLoading && <div className="recap-sidebar__placeholder">Loading…</div>}
        {listError && <div className="recap-sidebar__error">Failed to load: {listError}</div>}
        {!listLoading && !listError && list.length === 0 && !hasTodayFile && (
          <button
            type="button"
            className={`recap-sidebar__date ${selectedDate === todayDate ? 'is-selected' : ''}`}
            onClick={() => handleSelect(todayDate)}
          >
            Today · {todayDate}
          </button>
        )}
        {!listLoading && !listError && !hasTodayFile && list.length > 0 && (
          <button
            type="button"
            className={`recap-sidebar__date ${selectedDate === todayDate ? 'is-selected' : ''}`}
            onClick={() => handleSelect(todayDate)}
          >
            Today · {todayDate}
          </button>
        )}
        {list.map((entry) => (
          <button
            key={entry.date}
            type="button"
            className={`recap-sidebar__date ${selectedDate === entry.date ? 'is-selected' : ''}`}
            onClick={() => handleSelect(entry.date)}
          >
            {entry.isToday ? `Today · ${entry.date}` : entry.date}
          </button>
        ))}
      </div>
      <div className="recap-sidebar__content">
        {contentLoading && <div className="recap-sidebar__placeholder">Loading…</div>}
        {contentError && <div className="recap-sidebar__error">Failed to load: {contentError}</div>}
        {!contentLoading && !contentError && !content && (
          <div className="recap-sidebar__placeholder">
            {isToday
              ? 'No recap for today yet. Click Generate to create one.'
              : 'No recap for this date.'}
          </div>
        )}
        {!contentLoading && !contentError && content && <MarkdownView text={content} />}
      </div>
    </aside>
  )
}
