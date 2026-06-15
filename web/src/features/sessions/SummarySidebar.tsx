import { useCallback, useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getSummary, getTemplate } from '../../lib/api'
import './SummarySidebar.css'

interface Props {
  sessionID: string
  templateId: string
  summaryFetchCounter: number
  onClose: () => void
}

type Tab = 'summary' | 'template'

export function SummarySidebar({ sessionID, templateId, summaryFetchCounter, onClose }: Props) {
  const [tab, setTab] = useState<Tab>('summary')

  // Summary state ------------------------------------------------------------
  const [summary, setSummary] = useState<string>('')
  const [summaryErr, setSummaryErr] = useState<string | null>(null)
  // Only show the loading spinner on the FIRST fetch per session — later
  // bumps from summary_updated are silent re-fetches to avoid flicker.
  const firstSummaryFetchDone = useRef(false)
  const [summaryLoading, setSummaryLoading] = useState(true)

  // Tracks the most recent in-flight template fetch. setStates from older
  // requests are ignored. Also flipped in the cleanup so unmount aborts
  // pending work without React warnings.
  const templateAliveRef = useRef(true)
  useEffect(() => () => { templateAliveRef.current = false }, [])

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

  // Template state -----------------------------------------------------------
  const [template, setTemplate] = useState<string | null>(null)
  const [templateErr, setTemplateErr] = useState<string | null>(null)
  const [templateLoading, setTemplateLoading] = useState(false)

  const ensureTemplateLoaded = useCallback(() => {
    if (template !== null || templateLoading) return
    setTemplateLoading(true)
    getTemplate(templateId)
      .then((text) => {
        if (!templateAliveRef.current) return
        setTemplate(text)
        setTemplateErr(null)
      })
      .catch((e) => {
        if (!templateAliveRef.current) return
        setTemplateErr(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!templateAliveRef.current) return
        setTemplateLoading(false)
      })
  }, [templateId, template, templateLoading])

  useEffect(() => {
    if (tab === 'template') ensureTemplateLoaded()
  }, [tab, ensureTemplateLoaded])

  return (
    <aside className="summary-sidebar" aria-label="Task summary sidebar">
      <header className="summary-sidebar__header">
        <h2 className="summary-sidebar__title">Task Summary</h2>
        <button
          type="button"
          className="summary-sidebar__close"
          onClick={onClose}
          aria-label="Hide summary sidebar"
          title="Hide sidebar"
        >×</button>
      </header>
      <div className="summary-sidebar__tabs" role="tablist">
        <button
          role="tab"
          aria-selected={tab === 'summary'}
          className={`summary-sidebar__tab ${tab === 'summary' ? 'is-active' : ''}`}
          onClick={() => setTab('summary')}
        >Summary</button>
        <button
          role="tab"
          aria-selected={tab === 'template'}
          className={`summary-sidebar__tab ${tab === 'template' ? 'is-active' : ''}`}
          onClick={() => setTab('template')}
        >Template</button>
      </div>
      <div className="summary-sidebar__body">
        {tab === 'summary' && (
          <SummaryView
            text={summary}
            loading={summaryLoading}
            error={summaryErr}
          />
        )}
        {tab === 'template' && (
          <TemplateView
            text={template}
            loading={templateLoading}
            error={templateErr}
          />
        )}
      </div>
    </aside>
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
  return (
    <div className="summary-sidebar__markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code(props) {
            const { className, children, ...rest } = props as React.ComponentPropsWithoutRef<'code'>
            return <code className={className} {...rest}>{children}</code>
          },
        }}
      >{text}</ReactMarkdown>
    </div>
  )
}

function TemplateView({ text, loading, error }: { text: string | null; loading: boolean; error: string | null }) {
  if (error) return <div className="summary-sidebar__error">Failed to load template: {error}</div>
  if (loading || text === null) return <div className="summary-sidebar__placeholder">Loading…</div>
  return (
    <>
      <div className="summary-sidebar__note">
        Read-only. Adjust by asking Claude to update the summary differently.
      </div>
      <pre className="summary-sidebar__template-pre">{text}</pre>
    </>
  )
}
