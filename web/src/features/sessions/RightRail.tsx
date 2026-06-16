import { useCallback, useEffect, useState } from 'react'
import { SummarySection } from './SummarySidebar'
import { NotesPanel } from './NotesPanel'
import './RightRail.css'

interface Props {
  sessionID: string
  showSummary: boolean              // claude+ui+summary-todo
  summaryFetchCounter: number
  noteFetchCounter: number
}

// localStorage flags for each section's collapsed state. Both default
// to expanded the first time the user sees them.
const LS_SUMMARY = 'alfred_right_rail_summary_collapsed'
const LS_NOTES   = 'alfred_right_rail_notes_collapsed'

function readBool(key: string): boolean {
  try { return localStorage.getItem(key) === '1' } catch { return false }
}

function writeBool(key: string, v: boolean): void {
  try { localStorage.setItem(key, v ? '1' : '0') } catch { /* ignore */ }
}

export function RightRail({ sessionID, showSummary, summaryFetchCounter, noteFetchCounter }: Props) {
  const [summaryCollapsed, setSummaryCollapsed] = useState<boolean>(() => readBool(LS_SUMMARY))
  const [notesCollapsed, setNotesCollapsed] = useState<boolean>(() => readBool(LS_NOTES))

  // Whenever the eligible-sections set changes, ensure at least one
  // is expanded so the rail isn't a useless wall of headers.
  useEffect(() => {
    if (!showSummary && notesCollapsed) {
      setNotesCollapsed(false)
      writeBool(LS_NOTES, false)
    }
  }, [showSummary, notesCollapsed])

  const toggleSummary = useCallback(() => {
    setSummaryCollapsed((v) => { writeBool(LS_SUMMARY, !v); return !v })
  }, [])
  const toggleNotes = useCallback(() => {
    setNotesCollapsed((v) => { writeBool(LS_NOTES, !v); return !v })
  }, [])

  return (
    <aside className="right-rail" aria-label="Right rail">
      {showSummary && (
        <section className={`right-rail__section ${summaryCollapsed ? 'is-collapsed' : ''}`}>
          <button
            type="button"
            className="right-rail__header"
            onClick={toggleSummary}
            aria-expanded={!summaryCollapsed}
          >
            <span className="right-rail__chev">{summaryCollapsed ? '▸' : '▾'}</span>
            <h2 className="right-rail__title">Task Summary</h2>
          </button>
          {!summaryCollapsed && (
            <div className="right-rail__body">
              <SummarySection sessionID={sessionID} summaryFetchCounter={summaryFetchCounter} />
            </div>
          )}
        </section>
      )}
      <section className={`right-rail__section ${notesCollapsed ? 'is-collapsed' : ''}`}>
        <button
          type="button"
          className="right-rail__header"
          onClick={toggleNotes}
          aria-expanded={!notesCollapsed}
        >
          <span className="right-rail__chev">{notesCollapsed ? '▸' : '▾'}</span>
          <h2 className="right-rail__title">Notes</h2>
          <span className="right-rail__title-hint">(local only)</span>
        </button>
        {!notesCollapsed && (
          <div className="right-rail__body right-rail__body--notes">
            {/* key={sessionID} forces a fresh NotesPanel mount per
                session. Without it, the panel's lastPushedRef and
                text state would carry over when the user switches
                sessions, and the trailing debounced PUT would write
                the OLD text to the NEW session's file. */}
            <NotesPanel key={sessionID} sessionID={sessionID} noteFetchCounter={noteFetchCounter} />
          </div>
        )}
      </section>
    </aside>
  )
}
