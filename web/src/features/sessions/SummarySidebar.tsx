import { useCallback } from 'react'
import { getSummary } from '../../lib/api'
import { useDocumentSync } from '../../lib/useDocumentSync'
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
  const fetcher = useCallback(() => getSummary(sessionID), [sessionID])
  const { data, error, firstFetchPending } = useDocumentSync<string>(
    fetcher,
    [sessionID, summaryFetchCounter],
  )

  if (firstFetchPending) return <div className="summary-sidebar__placeholder">Loading…</div>
  if (error) return <div className="summary-sidebar__error">Failed to load summary: {error}</div>
  if (!data) {
    return (
      <div className="summary-sidebar__placeholder">
        No summary yet. After Claude&apos;s next reply, the task summary will appear here automatically.
      </div>
    )
  }
  return <MarkdownView text={data} />
}
