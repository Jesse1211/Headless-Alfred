import { useEffect, useRef, useState } from 'react'
import { getSummary } from '../../lib/api'
import { MarkdownView } from './MarkdownView'
import './SummarySidebar.css'

interface Props {
  sessionID: string
  summaryFetchCounter: number
}

// SummarySection is the content of the summary section in the right
// rail. RightRail owns the outer wrapper + accordion header; this
// component just renders the markdown body (or loading/error/empty).
export function SummarySection({ sessionID, summaryFetchCounter }: Props) {
  const [summary, setSummary] = useState<string>('')
  const [summaryErr, setSummaryErr] = useState<string | null>(null)
  // Only show the loading spinner on the FIRST fetch per session — later
  // bumps from summary_updated are silent re-fetches to avoid flicker.
  const firstSummaryFetchDone = useRef(false)
  const [summaryLoading, setSummaryLoading] = useState(true)

  useEffect(() => {
    // On sessionID change the parent typically remounts us (key={sid}),
    // but reset the flag here too so an unmounted-then-remounted same-
    // sid call still shows the spinner first.
    firstSummaryFetchDone.current = false
  }, [sessionID])

  useEffect(() => {
    let alive = true
    const showSpinner = !firstSummaryFetchDone.current
    if (showSpinner) setSummaryLoading(true)
    getSummary(sessionID)
      .then((text) => {
        if (!alive) return
        setSummary(text)
        setSummaryErr(null)
      })
      .catch((e) => {
        if (!alive) return
        setSummaryErr(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        firstSummaryFetchDone.current = true
        if (showSpinner) setSummaryLoading(false)
      })
    return () => { alive = false }
  }, [sessionID, summaryFetchCounter])

  return (
    <SummaryView
      text={summary}
      loading={summaryLoading}
      error={summaryErr}
    />
  )
}

function SummaryView({ text, loading, error }: { text: string; loading: boolean; error: string | null }) {
  if (loading) return <div className="summary-sidebar__placeholder">Loading…</div>
  if (error) return <div className="summary-sidebar__error">Failed to load summary: {error}</div>
  if (!text) {
    return (
      <div className="summary-sidebar__placeholder">
        No summary yet. After Claude&apos;s next reply, the task summary will appear here automatically.
      </div>
    )
  }
  return <MarkdownView text={text} />
}
