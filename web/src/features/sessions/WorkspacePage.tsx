import { useState } from 'react'
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
import { SessionsSidebar } from './SessionsSidebar'
import { ConfirmDialog } from './ConfirmDialog'
import { GitCredentialsDialog } from './GitCredentialsDialog'
import { ClaudeCredentialsDialog } from './ClaudeCredentialsDialog'
import { ClaudeTerminal } from '../claude/ClaudeTerminal'
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

  const [pendingClose, setPendingClose] = useState<string | null>(null)
  const [gitCredsOpen, setGitCredsOpen] = useState(false)
  const [claudeCredsOpen, setClaudeCredsOpen] = useState(false)
  const [credsMenuOpen, setCredsMenuOpen] = useState(false)
  // Session ID for which the "Start Claude" renderer-pick dialog is open.
  const [startClaudeFor, setStartClaudeFor] = useState<string | null>(null)

  const selected = s.selectedSessionID
    ? s.sessions.find((x) => x.id === s.selectedSessionID)
    : null
  const ps = s.selectedSessionID
    ? s.perSession.get(s.selectedSessionID) ?? emptyPerSessionState()
    : null
  const composerDisabled = s.connState !== 'open' || !s.selectedSessionID
  const composerBusy = !!ps?.running

  return (
    <div className="workspace">
      <SessionsSidebar
        sessions={s.sessions}
        selectedSessionID={s.selectedSessionID}
        maxSessions={MAX_SESSIONS}
        onCreate={() => s.createSession()}
        onSelect={s.selectSession}
        onRename={(id, name) => s.renameSession(id, name)}
        onClose={(id) => setPendingClose(id)}
      />

      <div className="workspace__main">
        <header className="workspace__header">
          <div className="workspace__brand">{selected?.name ?? 'Headless Alfred'}</div>
          <div className="workspace__status" title={s.connState}>
            <span className={`status-dot status-dot--${s.connState}`} />
          </div>
          {selected && ps && ps.mode === 'claude' && (
            <button
              type="button"
              className="workspace__claude-btn workspace__claude-btn--exit"
              onClick={() => s.exitClaude(selected.id)}
              title="Send Ctrl+C to Claude and return to shell view"
            >
              Exit Claude
            </button>
          )}
          {selected && ps && ps.mode !== 'claude' && (
            <button
              type="button"
              className="workspace__claude-btn"
              onClick={() => setStartClaudeFor(selected.id)}
              disabled={composerBusy}
              title="Start Claude in this session"
            >
              Claude
            </button>
          )}
          <div className="workspace__creds-menu">
            <button
              type="button"
              className="workspace__icon-btn"
              aria-label="Credentials"
              aria-haspopup="menu"
              aria-expanded={credsMenuOpen}
              title="Credentials"
              onClick={() => setCredsMenuOpen((v) => !v)}
              onBlur={() => {
                // Defer so the menu item click handler runs first.
                setTimeout(() => setCredsMenuOpen(false), 150)
              }}
            >
              {/* Lightweight gear glyph; no svg dep */}
              ⚙
            </button>
            {credsMenuOpen && (
              <div className="workspace__creds-menu-popup" role="menu">
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setCredsMenuOpen(false)
                    setGitCredsOpen(true)
                  }}
                >
                  Git credentials
                </button>
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setCredsMenuOpen(false)
                    setClaudeCredsOpen(true)
                  }}
                >
                  Claude credentials
                </button>
              </div>
            )}
          </div>
          <button className="workspace__logout" onClick={onLogout}>Sign out</button>
        </header>

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

      {gitCredsOpen && (
        <GitCredentialsDialog onClose={() => setGitCredsOpen(false)} />
      )}

      {claudeCredsOpen && (
        <ClaudeCredentialsDialog onClose={() => setClaudeCredsOpen(false)} />
      )}

      {startClaudeFor && (
        <StartClaudeDialog
          onCancel={() => setStartClaudeFor(null)}
          onStart={(renderer: ClaudeRenderer) => {
            const id = startClaudeFor
            setStartClaudeFor(null)
            s.enterClaude(id, renderer)
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
