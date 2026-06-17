import { useState, useCallback } from 'react'
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
import { useClaudeHistoryLoader } from './useClaudeHistoryLoader'
import { useResizableWidth } from './useResizableWidth'
import { SessionsSidebar } from './SessionsSidebar'
import { sessionIndicator } from './sessionStatus'
import { SessionIndicatorDot } from './SessionIndicatorDot'
import { DiskUsageBanner } from './DiskUsageBanner'
import { ConfirmDialog } from './ConfirmDialog'
import { GitCredentialsDialog } from './GitCredentialsDialog'
import { ClaudeCredentialsDialog } from './ClaudeCredentialsDialog'
import { ClaudeVersionDialog } from './ClaudeVersionDialog'
import { ClaudeTerminal } from '../claude/ClaudeTerminal'
import { RightRail } from './RightRail'
import { RecapSidebar } from './RecapSidebar'
import { ClaudeChatView } from './ClaudeChatView'
import { StartClaudeDialog } from './StartClaudeDialog'
import ChatStream from '../terminal/ChatStream'
import CommandInput from '../terminal/CommandInput'
import { emptyClaudeState, emptyPerSessionState } from './types'
import type { ClaudeRenderer } from '../../lib/ws'
import '../terminal/TerminalPage.css'
import './WorkspacePage.css'

const MAX_SESSIONS = 8

interface Props {
  token: string
  onLogout: () => void
}

export function WorkspacePage({ token, onLogout }: Props) {
  const s = useSessions(token)
  useSessionHistoryLoader({
    selectedSessionID: s.selectedSessionID,
    perSession: s.perSession,
    setPerSession: s.setPerSession,
  })
  useClaudeHistoryLoader({
    selectedSessionID: s.selectedSessionID,
    perSession: s.perSession,
    setPerSession: s.setPerSession,
  })

  const [pendingClose, setPendingClose] = useState<string | null>(null)
  const [gitCredsOpen, setGitCredsOpen] = useState(false)
  const [claudeCredsOpen, setClaudeCredsOpen] = useState(false)
  const [claudeVersionOpen, setClaudeVersionOpen] = useState(false)
  // Session ID for which the "Start Claude" renderer-pick dialog is open.
  const [startClaudeFor, setStartClaudeFor] = useState<string | null>(null)

  // Global "summary sidebar hidden" toggle. Persisted to localStorage so
  // the user's preference survives reload. Per spec it's intentionally
  // global, not per-session — hiding on session A keeps it hidden when
  // switching to session B.
  const [sidebarHidden, setSidebarHidden] = useState<boolean>(() => {
    try {
      // Prefer the new key. Fall back to the old summary-only key for
      // a one-time migration so returning users keep their hide
      // preference.
      const v = localStorage.getItem('alfred_right_sidebar_hidden')
      if (v !== null) return v === '1'
      return localStorage.getItem('alfred_summary_sidebar_hidden') === '1'
    } catch {
      return false
    }
  })

  const setSidebarHiddenPersisted = useCallback((hidden: boolean) => {
    setSidebarHidden(hidden)
    try {
      localStorage.setItem('alfred_right_sidebar_hidden', hidden ? '1' : '0')
      localStorage.removeItem('alfred_summary_sidebar_hidden') // migrate
    } catch {
      // localStorage unavailable
    }
  }, [])

  const selected = s.selectedSessionID
    ? s.sessions.find((x) => x.id === s.selectedSessionID)
    : null
  const ps = s.selectedSessionID
    ? s.perSession.get(s.selectedSessionID) ?? emptyPerSessionState()
    : null
  const composerDisabled = s.connState !== 'open' || !s.selectedSessionID
  const composerBusy = !!ps?.running

  const isRecap = selected?.kind === 'recap'
  // RightRail (Summary + Notes accordion) is eligible for any non-recap
  // session. Notes always renders; Summary nested inside only when
  // claude+ui+template.
  const showRightRail = !!(selected && ps && !isRecap)
  const showSummarySection = !!(selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo' && !isRecap)
  // RecapSidebar is the sidebar for recap-kind sessions. Always shown
  // when the user is on a recap session.
  const showRecapSidebar = !!(selected && isRecap)
  // has-sidebar: any of the right-column sidebars is rendering.
  const sidebarShown = (showRightRail && !sidebarHidden) || showRecapSidebar

  // Resizable left + right widths. Persisted to localStorage independently
  // of the sidebar-hidden flag, so re-opening the summary sidebar restores
  // the last-set width.
  const [leftCollapsed, setLeftCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem('alfred_left_sidebar_collapsed') === '1'
    } catch {
      return false
    }
  })
  const setLeftCollapsedPersisted = useCallback((collapsed: boolean) => {
    setLeftCollapsed(collapsed)
    try { localStorage.setItem('alfred_left_sidebar_collapsed', collapsed ? '1' : '0') } catch { /* ignore */ }
  }, [])
  const COLLAPSED_LEFT_WIDTH = 40
  const leftSidebar = useResizableWidth({
    storageKey: 'alfred_left_sidebar_width',
    initial: 260,
    min: 180,
    max: 480,
    edge: 'right',
    archiveThreshold: 140,
    onArchive: () => setLeftCollapsedPersisted(true),
  })
  const rightSidebar = useResizableWidth({
    storageKey: 'alfred_right_sidebar_width',
    initial: 320,
    min: 220,
    max: 600,
    edge: 'left',
    archiveThreshold: 180,
    onArchive: () => setSidebarHiddenPersisted(true),
  })

  const leftWidthPx = leftCollapsed ? COLLAPSED_LEFT_WIDTH : leftSidebar.width
  const gridTemplateColumns = sidebarShown
    ? `${leftWidthPx}px 1fr ${rightSidebar.width}px`
    : `${leftWidthPx}px 1fr`

  return (
    <div
      className={`workspace ${sidebarShown ? 'has-sidebar' : ''}`}
      style={{ gridTemplateColumns }}
    >
      {leftCollapsed ? (
        <div className="workspace__left-pane workspace__left-pane--collapsed">
          <button
            type="button"
            className="workspace__left-expand"
            onClick={() => setLeftCollapsedPersisted(false)}
            aria-label="Expand sessions sidebar"
            title="Expand sidebar"
          >
            »
          </button>
          <div className="workspace__left-collapsed-footer">
            <button
              type="button"
              className="workspace__left-expand"
              onClick={() => setGitCredsOpen(true)}
              aria-label="Git credentials"
              title="Git credentials"
            >
              G
            </button>
            <button
              type="button"
              className="workspace__left-expand"
              onClick={() => setClaudeCredsOpen(true)}
              aria-label="Claude credentials"
              title="Claude credentials"
            >
              C
            </button>
            <button
              type="button"
              className="workspace__left-expand"
              onClick={onLogout}
              aria-label="Sign out"
              title="Sign out"
            >
              ⎋
            </button>
          </div>
        </div>
      ) : (
        <div className="workspace__left-pane">
          <SessionsSidebar
            // Recap sessions are seeded into useSessions.sessions via
            // setSessionMeta so the rest of the hook can find them, but
            // they should NOT appear in the chat-only sidebar list.
            sessions={s.sessions.filter((sess) => sess.kind !== 'recap')}
            selectedSessionID={s.selectedSessionID}
            maxSessions={MAX_SESSIONS}
            onCreate={() => s.createSession()}
            onCreateRecap={() => s.createOrEnterRecap()}
            onSelect={s.selectSession}
            onRename={(id, name) => s.renameSession(id, name)}
            onClose={(id) => setPendingClose(id)}
            onOpenGitCredentials={() => setGitCredsOpen(true)}
            onOpenClaudeCredentials={() => setClaudeCredsOpen(true)}
            onOpenClaudeVersion={() => setClaudeVersionOpen(true)}
            onLogout={onLogout}
            onCollapse={() => setLeftCollapsedPersisted(true)}
            statusForSession={(id) => sessionIndicator(s.connState, s.perSession.get(id))}
          />
          <div
            className="workspace__resizer workspace__resizer--right"
            {...leftSidebar.dividerProps}
            aria-label="Resize sessions sidebar"
          />
        </div>
      )}

      <div className="workspace__main">
        <header className="workspace__header">
          <div className="workspace__header-left">
            <div className="workspace__brand">{selected?.name ?? 'Headless Alfred'}</div>
            <div className="workspace__status">
              <SessionIndicatorDot status={sessionIndicator(s.connState, ps)} />
            </div>
          </div>
          <div className="workspace__header-center">
            {selected && ps && ps.mode === 'claude' && (() => {
              // UI mode: alfred-server forks `claude -p` per prompt;
              // between prompts there's NO claude process. Exit just
              // flips the mode flag — bash in the pane is untouched,
              // so cwd / env / aliases / shell history all survive.
              // Conversation continues next time you click Claude
              // because `--resume <uuid>` rebuilds context from the
              // jsonl on the PVC.
              //
              // TUI mode: a long-lived `claude` TUI owns the pane.
              // Exit has to SIGKILL it (no clean way to find its
              // pid through tmux), which kills the pane's bash too
              // and forces a respawn — cwd / env get reset.
              const isUI = ps.renderer === 'ui'
              return (
                <button
                  type="button"
                  className="workspace__claude-btn workspace__claude-btn--exit"
                  onClick={() => s.exitClaude(selected.id)}
                  data-tooltip={isUI
                    ? 'Pause Claude — conversation saved, click Claude to resume'
                    : 'Exit Claude — resets shell cwd / env / aliases'}
                >
                  {isUI ? 'Pause Claude' : 'Exit Claude'}
                </button>
              )
            })()}
            {selected && ps && ps.mode !== 'claude' && (
              <button
                type="button"
                className="workspace__claude-btn"
                onClick={() => setStartClaudeFor(selected.id)}
                disabled={composerBusy}
                data-tooltip="Start Claude in this session"
              >
                Claude
              </button>
            )}
          </div>
          <div className="workspace__header-right">
            {selected && ps && !isRecap && (
              <button
                type="button"
                className={`workspace__sidebar-icon-btn ${sidebarHidden ? '' : 'is-active'}`}
                onClick={() => setSidebarHiddenPersisted(!sidebarHidden)}
                data-tooltip={sidebarHidden ? 'Show right sidebar' : 'Hide right sidebar'}
                aria-pressed={!sidebarHidden}
                aria-label="Toggle right sidebar"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                  <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
                  <line x1="10" y1="2.5" x2="10" y2="13.5" stroke="currentColor" strokeWidth="1.3" />
                </svg>
              </button>
            )}
          </div>
        </header>

        <DiskUsageBanner usage={s.diskUsage} />

        {s.lastError && (
          <div className="workspace__banner is-error">
            {s.lastError.message || s.lastError.code}
            <button onClick={s.clearError} aria-label="dismiss">×</button>
          </div>
        )}

        {!selected && (
          <div className="workspace__empty">
            <h1>Headless Alfred</h1>
            <p>Create a session to begin.</p>
            <button onClick={() => s.createSession()}>+ New chat</button>
          </div>
        )}

        {selected && ps && ps.mode === 'claude' && ps.renderer === 'ui' && (
          <ClaudeChatView
            state={ps.claude ?? emptyClaudeState()}
            disabled={s.connState !== 'open'}
            onPrompt={(text) => s.claudePrompt(selected.id, text)}
            onToolDecision={(toolUseId, decision, reason) =>
              s.toolDecision(selected.id, toolUseId, decision, reason)
            }
            onQuestionAnswer={(toolUseId, answer) =>
              s.submitQuestionAnswer(selected.id, toolUseId, answer)
            }
            onInterrupt={() => s.interruptClaude(selected.id)}
          />
        )}

        {selected && ps && ps.mode === 'claude' && ps.renderer !== 'ui' && (
          <ClaudeTerminal
            sessionID={selected.id}
            registerPtyHandler={s.registerPtyHandler}
            sendStdin={s.sendStdin}
          />
        )}

        {selected && ps && ps.mode !== 'claude' && (
          <>
            <ChatStream messages={ps.messages} running={ps.running} />
            <div className="workspace__composer">
              <div className="workspace__composer-inner">
                <CommandInput
                  disabled={composerDisabled}
                  busy={composerBusy}
                  onSubmit={(cmd) => {
                    // Anything that starts with `claude` (the actual CLI)
                    // or `/claude` (the chat-style slash command) flips
                    // this session into Claude mode instead of running
                    // the literal command in bash. Without this guard
                    // typing `claude` would print Claude's full-screen
                    // TUI into the chat-stream view, which can't render
                    // cursor-positioning escapes — looks like garbage.
                    const trimmed = cmd.trim()
                    const isClaude =
                      trimmed === 'claude' ||
                      trimmed === '/claude' ||
                      trimmed.startsWith('claude ') ||
                      trimmed.startsWith('/claude ')
                    if (isClaude) {
                      setStartClaudeFor(selected.id)
                      return
                    }
                    s.submit(cmd)
                  }}
                  onStop={() => ps.running && s.stop(ps.running.id)}
                />
              </div>
            </div>
          </>
        )}
      </div>

      {sidebarShown && (
        <div className="workspace__right-pane">
          <div
            className="workspace__resizer workspace__resizer--left"
            {...rightSidebar.dividerProps}
            aria-label="Resize right sidebar"
          />
          {showRightRail && !sidebarHidden && selected && ps && (
            <RightRail
              key={selected.id}
              sessionID={selected.id}
              showSummary={showSummarySection}
              summaryFetchCounter={ps.summaryFetchCounter ?? 0}
              noteFetchCounter={ps.noteFetchCounter ?? 0}
            />
          )}
          {showRecapSidebar && selected && (
            <RecapSidebar
              recapFetchCounter={s.recapFetchCounter}
              generating={!!ps?.claude?.inFlight}
              onGenerate={() => {
                // Empty text + renderTemplate makes the server render the
                // recap-daily prompt with server-resolved placeholders
                // (date, cwd, absolute recap_path). The client owns NO
                // placeholder logic — the file path uses ALFRED_DATA_DIR,
                // which the frontend doesn't know.
                s.claudePrompt(selected.id, '', {
                  renderTemplate: 'recap-daily',
                  optimisticLabel: "Generate today's recap",
                })
              }}
            />
          )}
        </div>
      )}

      {gitCredsOpen && (
        <GitCredentialsDialog onClose={() => setGitCredsOpen(false)} />
      )}

      {claudeVersionOpen && (
        <ClaudeVersionDialog onClose={() => setClaudeVersionOpen(false)} />
      )}

      {claudeCredsOpen && (
        <ClaudeCredentialsDialog onClose={() => setClaudeCredsOpen(false)} />
      )}

      {startClaudeFor && (
        <StartClaudeDialog
          onCancel={() => setStartClaudeFor(null)}
          onStart={(renderer: ClaudeRenderer, bypass: boolean, templateId: string) => {
            const id = startClaudeFor
            setStartClaudeFor(null)
            s.enterClaude(id, renderer, bypass, templateId)
          }}
        />
      )}

      {pendingClose && (
        <ConfirmDialog
          title="Close session?"
          body={
            (() => {
              const target = s.sessions.find((x) => x.id === pendingClose)
              const psTarget = s.perSession.get(pendingClose) ?? emptyPerSessionState()
              const running = psTarget.running
              const count = psTarget.messages.length
              const name = target?.name ?? 'this session'
              return running
                ? `Close '${name}'? 1 command is still running and will be terminated. The session and ${count} commands of history will be permanently deleted.`
                : `Close '${name}'? The session and ${count} commands of history will be permanently deleted.`
            })()
          }
          confirmLabel="Delete"
          onConfirm={() => {
            const id = pendingClose
            setPendingClose(null)
            s.closeSession(id)
          }}
          onCancel={() => setPendingClose(null)}
        />
      )}
    </div>
  )
}
