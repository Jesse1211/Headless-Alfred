import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { WorkspacePage } from './WorkspacePage'
import type { PerSessionState } from './types'
import { emptyClaudeState } from './types'

// ─── Mock useSessions ─────────────────────────────────────────────────────────

// Base mock return value that tests can override per-test.
const mockUseSessions = {
  connState: 'open' as const,
  connInfo: {},
  wsEpoch: 0,
  sessions: [{ id: 'sess-1', name: 'Test Session', created_at: '2026-06-19T00:00:00Z' }],
  selectedSessionID: 'sess-1',
  selectSession: vi.fn(),
  perSession: new Map<string, PerSessionState>(),
  setPerSession: vi.fn(),
  submit: vi.fn(),
  stop: vi.fn(),
  createSession: vi.fn(),
  renameSession: vi.fn(),
  closeSession: vi.fn(),
  enterClaude: vi.fn(),
  exitClaude: vi.fn(),
  sendStdin: vi.fn(),
  registerPtyHandler: vi.fn(),
  claudePrompt: vi.fn(),
  toolDecision: vi.fn(),
  interruptClaude: vi.fn(),
  submitQuestionAnswer: vi.fn(),
  lastError: null,
  clearError: vi.fn(),
  recapFetchCounter: 0,
  createOrEnterRecap: vi.fn(),
  setSessionMeta: vi.fn(),
  diskUsage: null,
  subscribeBgTaskLog: vi.fn(),
  unsubscribeBgTaskLog: vi.fn(),
  fetchBgTaskLogTail: vi.fn().mockResolvedValue({ bytes: '', size: 0, truncated: false }),
}

vi.mock('./useSessions', () => ({
  useSessions: () => mockUseSessions,
}))

// ─── Mock other hooks + leaf components ──────────────────────────────────────

vi.mock('./useSessionHistoryLoader', () => ({ useSessionHistoryLoader: () => {} }))
vi.mock('./useClaudeStateLoader', () => ({ useClaudeStateLoader: () => {} }))
vi.mock('../../lib/api', () => ({
  listTemplates: vi.fn(() => Promise.resolve([])),
  listSessions: vi.fn(() => Promise.resolve([])),
}))

// Stub heavy leaf components so we don't need to satisfy all their deps
vi.mock('./SessionsSidebar', () => ({
  SessionsSidebar: () => <div data-testid="sessions-sidebar" />,
}))
vi.mock('./ClaudeChatView', () => ({
  ClaudeChatView: () => <div data-testid="claude-chat-view" />,
}))
vi.mock('./BackgroundTasksPanel', () => ({
  BackgroundTasksPanel: (props: { open: boolean }) =>
    props.open ? <div data-testid="bg-tasks-panel" /> : null,
}))
vi.mock('./DiskUsageBanner', () => ({ DiskUsageBanner: () => null }))
vi.mock('./RightRail', () => ({ RightRail: () => null }))
vi.mock('./RecapSidebar', () => ({ RecapSidebar: () => null }))
vi.mock('../claude/ClaudeTerminal', () => ({ ClaudeTerminal: () => null }))
vi.mock('../terminal/ChatStream', () => ({ default: () => null }))
vi.mock('../terminal/CommandInput', () => ({ default: () => null }))
vi.mock('./sessionStatus', () => ({ sessionIndicator: () => 'idle' as const }))
vi.mock('./SessionIndicatorDot', () => ({ SessionIndicatorDot: () => null }))

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeUIPerSession(
  bgTaskCount: number,
  subagentCount: number,
  inFlight = true,
): Map<string, PerSessionState> {
  const bgTasks: Record<string, import('./types').BgTask> = {}
  for (let i = 0; i < bgTaskCount; i++) {
    bgTasks[`task-${i}`] = {
      taskId: `task-${i}`,
      toolUseId: `tu-${i}`,
      description: `Task ${i}`,
      taskType: 'Monitor',
      startedAt: new Date(Date.now() - 5000).toISOString(),
      status: 'in_progress',
      notificationCount: 0,
    }
  }
  const subagents: Record<string, import('./types').SubagentEntry> = {}
  for (let i = 0; i < subagentCount; i++) {
    subagents[`sa-${i}`] = {
      hookId: `sa-${i}`,
      agentType: 'general-purpose',
      startedAt: new Date(Date.now() - 3000).toISOString(),
      // no finishedAt → active
    }
  }
  const ps: PerSessionState = {
    running: null,
    messages: [],
    messagesLoaded: true,
    mode: 'claude',
    renderer: 'ui',
    claude: {
      ...emptyClaudeState(),
      inFlight,
      bgTasks,
      subagents,
      turnsLoaded: true,
    },
  }
  return new Map([['sess-1', ps]])
}

function makeShellPerSession(): Map<string, PerSessionState> {
  const ps: PerSessionState = {
    running: null,
    messages: [],
    messagesLoaded: true,
    mode: 'shell',
    renderer: '',
    claude: emptyClaudeState(),
  }
  return new Map([['sess-1', ps]])
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('WorkspacePage — badge + panel (T14)', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    // Reset perSession to empty so tests start clean
    mockUseSessions.perSession = new Map()
    mockUseSessions.subscribeBgTaskLog = vi.fn()
    mockUseSessions.unsubscribeBgTaskLog = vi.fn()
    mockUseSessions.fetchBgTaskLogTail = vi.fn().mockResolvedValue({ bytes: '', size: 0, truncated: false })
  })

  it('badge counts running bgTasks+subagents', () => {
    // 2 running bg tasks + 1 running subagent → badge shows "3"
    mockUseSessions.perSession = makeUIPerSession(2, 1, true)
    render(<WorkspacePage token="tok" onLogout={vi.fn()} />)
    const badge = screen.getByRole('button', { name: /background tasks/i })
    expect(badge.textContent).toMatch(/3/)
  })

  it('clicking badge toggles panel and persists to localStorage', () => {
    // Need at least 1 running task so badge is enabled
    mockUseSessions.perSession = makeUIPerSession(1, 0, false)
    render(<WorkspacePage token="tok" onLogout={vi.fn()} />)

    // Panel should not be open initially
    expect(screen.queryByTestId('bg-tasks-panel')).toBeNull()

    // Click badge → panel opens → localStorage = 'true'
    const badge = screen.getByRole('button', { name: /background tasks/i })
    fireEvent.click(badge)
    expect(localStorage.getItem('alfred_bg_tasks_panel_open')).toBe('true')

    // Click badge again → panel closes → localStorage = 'false'
    fireEvent.click(badge)
    expect(localStorage.getItem('alfred_bg_tasks_panel_open')).toBe('false')
  })

  it('panel only renders in UI mode (mode === claude && renderer === ui)', () => {
    // Start in shell mode — panel must not render even if open flag is set
    mockUseSessions.perSession = makeShellPerSession()
    // Pre-set panel to open via localStorage, but there's nothing to count in shell mode
    localStorage.setItem('alfred_bg_tasks_panel_open', 'true')

    const { rerender } = render(<WorkspacePage token="tok" onLogout={vi.fn()} />)
    expect(screen.queryByTestId('bg-tasks-panel')).toBeNull()

    // Switch to UI mode with 1 running task (so badge is enabled and panel can open)
    mockUseSessions.perSession = makeUIPerSession(1, 0, false)
    // We need to re-render with updated perSession and bgTasksOpen=true already set
    rerender(<WorkspacePage token="tok" onLogout={vi.fn()} />)
    expect(screen.getByTestId('bg-tasks-panel')).toBeTruthy()
  })
})
