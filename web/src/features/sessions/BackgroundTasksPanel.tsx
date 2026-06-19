import { useCallback, useEffect, useRef, useState } from 'react'
import type { BgTask, SubagentEntry } from './types'
import './BackgroundTasksPanel.css'

// ─── Props contract (spec T13) ────────────────────────────────────────────────

interface BackgroundTasksPanelProps {
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
  inFlight: boolean
  bgTaskLogs: Record<string, string>
  onSubscribeLog: (taskId: string) => void
  onUnsubscribeLog: (taskId: string) => void
  onFetchLogTail: (taskId: string) => Promise<
    { bytes: string; size: number; truncated: boolean } | { status: 'log_unavailable' }
  >
  open: boolean
  onToggle: () => void
  connState: 'open' | 'connecting' | 'closed'
  turnsLoaded: boolean
}

// ─── Small elapsed helpers ────────────────────────────────────────────────────

function useElapsed(startedAt: string | undefined, finishedAt: string | undefined): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!startedAt || finishedAt) return
    const id = window.setInterval(() => setTick((n) => n + 1), 1000)
    return () => window.clearInterval(id)
  }, [startedAt, finishedAt])
  if (!startedAt) return 0
  const end = finishedAt ? Date.parse(finishedAt) : Date.now()
  const start = Date.parse(startedAt)
  void tick // implicit dependency for re-render
  return Math.max(0, Math.floor((end - start) / 1000))
}

function formatElapsed(secs: number): string {
  if (secs < 60) return `${secs}s`
  if (secs < 3600) {
    const m = Math.floor(secs / 60)
    const s = secs % 60
    return `${m}m${s.toString().padStart(2, '0')}s`
  }
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  return `${h}h${m.toString().padStart(2, '0')}m`
}

function formatAgo(finishedAt: string): string {
  const secs = Math.max(0, Math.floor((Date.now() - Date.parse(finishedAt)) / 1000))
  return `${formatElapsed(secs)} ago`
}

// ─── Task row ────────────────────────────────────────────────────────────────

interface TaskRowProps {
  task: BgTask
  logText: string | undefined
  onSubscribeLog: (taskId: string) => void
  onUnsubscribeLog: (taskId: string) => void
  onFetchLogTail: BackgroundTasksPanelProps['onFetchLogTail']
  onLogSubscribed: (taskId: string) => void
  onLogUnsubscribed: (taskId: string) => void
}

function TaskRow({
  task,
  logText,
  onSubscribeLog,
  onUnsubscribeLog,
  onFetchLogTail,
  onLogSubscribed,
  onLogUnsubscribed,
}: TaskRowProps) {
  const [logsOpen, setLogsOpen] = useState(false)
  const [logUnavailable, setLogUnavailable] = useState(false)
  const subscribedRef = useRef(false)

  const elapsedSecs = useElapsed(task.startedAt, task.finishedAt)
  const isRunning = task.status === 'in_progress'

  const handleViewLogs = useCallback(async () => {
    if (logsOpen) {
      // Close logs and unsubscribe
      if (subscribedRef.current) {
        onUnsubscribeLog(task.taskId)
        onLogUnsubscribed(task.taskId)
        subscribedRef.current = false
      }
      setLogsOpen(false)
      setLogUnavailable(false)
      return
    }

    // Open logs: fetch tail first, then subscribe
    setLogsOpen(true)
    const result = await onFetchLogTail(task.taskId)
    if ('status' in result && result.status === 'log_unavailable') {
      setLogUnavailable(true)
      return
    }
    // Success — subscribe for incremental updates
    onSubscribeLog(task.taskId)
    subscribedRef.current = true
    onLogSubscribed(task.taskId)
  }, [logsOpen, task.taskId, onFetchLogTail, onSubscribeLog, onUnsubscribeLog, onLogSubscribed, onLogUnsubscribed])

  // Cleanup on unmount if subscribed
  useEffect(() => {
    return () => {
      if (subscribedRef.current) {
        onUnsubscribeLog(task.taskId)
        onLogUnsubscribed(task.taskId)
        subscribedRef.current = false
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [task.taskId])

  const chipClass = `bg-tasks-panel__chip bg-tasks-panel__chip--${task.status}`
  const chipLabel: Record<BgTask['status'], string> = {
    in_progress: `${task.taskType} · running`,
    completed:   `${task.taskType} · ✓`,
    stopped:     `${task.taskType} · ✓`,
    killed:      `${task.taskType} · ended`,
    failed:      `${task.taskType} · failed`,
  }

  const elapsedLabel = isRunning
    ? formatElapsed(elapsedSecs)
    : task.finishedAt
    ? formatAgo(task.finishedAt)
    : ''

  const title = task.description || task.taskId

  return (
    <div className="bg-tasks-panel__row">
      <div className="bg-tasks-panel__row-main">
        <span className="bg-tasks-panel__row-title" title={title}>{title}</span>
        <span className={chipClass}>{chipLabel[task.status]}</span>
        {elapsedLabel && (
          <span className="bg-tasks-panel__elapsed">{elapsedLabel}</span>
        )}
        <button
          type="button"
          className="bg-tasks-panel__log-btn"
          onClick={handleViewLogs}
          aria-expanded={logsOpen}
        >
          {logsOpen ? 'Hide logs' : 'View logs'}
        </button>
      </div>
      {task.status === 'killed' && (
        <div className="bg-tasks-panel__row-summary">{task.lastEventSummary || '(ended with main Claude)'}</div>
      )}
      {task.lastEventSummary && task.status !== 'killed' && (
        <div className="bg-tasks-panel__row-summary">{task.lastEventSummary}</div>
      )}
      {logsOpen && (
        <div className="bg-tasks-panel__log-body">
          {logUnavailable ? (
            <span className="bg-tasks-panel__log-unavailable">No log captured</span>
          ) : (
            <pre className="bg-tasks-panel__log-pre">{logText ?? ''}</pre>
          )}
        </div>
      )}
    </div>
  )
}

// ─── Main component ───────────────────────────────────────────────────────────

export function BackgroundTasksPanel({
  bgTasks,
  subagents,
  inFlight,
  bgTaskLogs,
  onSubscribeLog,
  onUnsubscribeLog,
  onFetchLogTail,
  open,
  onToggle,
  connState,
  turnsLoaded,
}: BackgroundTasksPanelProps) {
  // No-op callbacks: TaskRow manages its own subscription lifecycle via
  // its own useEffect cleanup (OQ-07). These are kept for future use
  // but are intentionally no-ops to avoid double-unsubscribing.
  const handleLogSubscribed = useCallback((_taskId: string) => {}, [])
  const handleLogUnsubscribed = useCallback((_taskId: string) => {}, [])

  // Trigger a re-render every second when there are recently-finished tasks
  // within the fade window, so elapsed timers and fade are accurate.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (!open) return
    const recentlyFinished = Object.values(bgTasks).some(
      (t) => t.status !== 'in_progress' && t.finishedAt &&
        Date.now() - Date.parse(t.finishedAt) < 60_000
    )
    if (!recentlyFinished) return
    const id = window.setInterval(() => setTick((n) => n + 1), 1000)
    return () => window.clearInterval(id)
  }, [open, bgTasks])

  if (!open) return null

  // ── Derived data ─────────────────────────────────────────────────────────

  const bgTaskList = Object.values(bgTasks)
  const subagentList = Object.values(subagents)

  // Background bash group: in_progress tasks (no tool-name filter)
  const runningBgTasks = bgTaskList.filter((t) => t.status === 'in_progress')

  // Subagent group: active (no finishedAt) AND inFlight
  const activeSubagents = inFlight
    ? subagentList.filter((s) => !s.finishedAt)
    : []

  // Recently finished group: not in_progress AND within 60s window
  const FADE_MS = 60_000
  const recentlyFinished = bgTaskList.filter(
    (t) =>
      t.status !== 'in_progress' &&
      t.finishedAt &&
      Date.now() - Date.parse(t.finishedAt) < FADE_MS
  )

  // N for the header: running bgTasks + active subagents
  const runningN = runningBgTasks.length + activeSubagents.length

  // ── Header status text ───────────────────────────────────────────────────

  let mainClaudeStatus: string
  if (inFlight) {
    mainClaudeStatus = '● running'
  } else if (bgTaskList.some((t) => t.status === 'killed')) {
    mainClaudeStatus = '✗ exited'
  } else {
    mainClaudeStatus = '○ idle'
  }

  const isDisconnected = connState !== 'open'

  // ── Render ───────────────────────────────────────────────────────────────

  return (
    <div className="bg-tasks-panel" role="region" aria-label="Background tasks">
      <div className="bg-tasks-panel__header">
        <div className="bg-tasks-panel__header-left">
          <span className="bg-tasks-panel__main-status">
            Main Claude: <span className={`bg-tasks-panel__status-indicator bg-tasks-panel__status-indicator--${inFlight ? 'running' : 'idle'}`}>{mainClaudeStatus}</span>
          </span>
        </div>
        <div className="bg-tasks-panel__header-right">
          {turnsLoaded && (isDisconnected ? (
            <span className="bg-tasks-panel__disconnected">
              Disconnected
            </span>
          ) : (
            <span className="bg-tasks-panel__live">
              Live · <strong>{runningN} running</strong>
            </span>
          ))}
          <button
            type="button"
            className="bg-tasks-panel__toggle"
            onClick={onToggle}
            aria-label="Close background tasks panel"
          >
            ✕
          </button>
        </div>
      </div>

      {!turnsLoaded ? (
        <div className="bg-tasks-panel__skeleton">Loading…</div>
      ) : (
        <div className="bg-tasks-panel__body">
          {runningBgTasks.length > 0 && (
            <div className="bg-tasks-panel__group">
              <div className="bg-tasks-panel__group-title">Background bash</div>
              {runningBgTasks.map((task) => (
                <TaskRow
                  key={task.taskId}
                  task={task}
                  logText={bgTaskLogs[task.taskId]}
                  onSubscribeLog={onSubscribeLog}
                  onUnsubscribeLog={onUnsubscribeLog}
                  onFetchLogTail={onFetchLogTail}
                  onLogSubscribed={handleLogSubscribed}
                  onLogUnsubscribed={handleLogUnsubscribed}
                />
              ))}
            </div>
          )}

          {inFlight && activeSubagents.length > 0 && (
            <div className="bg-tasks-panel__group">
              <div className="bg-tasks-panel__group-title">Subagents (blocking main Claude)</div>
              {activeSubagents.map((s) => (
                <div key={s.hookId} className="bg-tasks-panel__row bg-tasks-panel__row--subagent">
                  <span className="bg-tasks-panel__row-title">{s.agentType ?? s.hookId}</span>
                  <span className="bg-tasks-panel__chip bg-tasks-panel__chip--in_progress">subagent · running</span>
                </div>
              ))}
            </div>
          )}

          {recentlyFinished.length > 0 && (
            <div className="bg-tasks-panel__group">
              <div className="bg-tasks-panel__group-title">Recently finished</div>
              {recentlyFinished.map((task) => (
                <TaskRow
                  key={task.taskId}
                  task={task}
                  logText={bgTaskLogs[task.taskId]}
                  onSubscribeLog={onSubscribeLog}
                  onUnsubscribeLog={onUnsubscribeLog}
                  onFetchLogTail={onFetchLogTail}
                  onLogSubscribed={handleLogSubscribed}
                  onLogUnsubscribed={handleLogUnsubscribed}
                />
              ))}
              <div className="bg-tasks-panel__fade-hint">Completed tasks hide after 60s.</div>
            </div>
          )}

          {runningBgTasks.length === 0 && activeSubagents.length === 0 && recentlyFinished.length === 0 && (
            <div className="bg-tasks-panel__empty">No active background tasks.</div>
          )}
        </div>
      )}
    </div>
  )
}
