import { useCallback } from 'react'
import { getSummary, MarkdownDoc } from '../../lib/api'
import { useDocumentSync } from '../../lib/useDocumentSync'
import { MarkdownView } from './MarkdownView'
import { PathStrip } from './PathStrip'
import './SummarySidebar.css'

interface Props {
  sessionID: string
  summaryFetchCounter: number
}

// SummarySection is the content of the summary section in the right
// rail. RightRail owns the outer wrapper + accordion header; this
// component renders a thin path strip followed by the markdown body
// (or loading/error/empty state).
export function SummarySection({ sessionID, summaryFetchCounter }: Props) {
  const fetcher = useCallback(() => getSummary(sessionID), [sessionID])
  const { data, error, firstFetchPending } = useDocumentSync<MarkdownDoc>(
    fetcher,
    [sessionID, summaryFetchCounter],
  )

  return (
    <>
      <PathStrip path={data?.path ?? null} />
      <SummaryBody
        text={data?.text ?? ''}
        loading={firstFetchPending}
        error={error}
      />
    </>
  )
}

function SummaryBody({ text, loading, error }: { text: string; loading: boolean; error: string | null }) {
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
