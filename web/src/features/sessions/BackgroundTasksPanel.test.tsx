import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { BackgroundTasksPanel } from './BackgroundTasksPanel'
import type { BgTask, SubagentEntry } from './types'

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeTask(id: string, overrides: Partial<BgTask> = {}): BgTask {
  return {
    taskId: id,
    toolUseId: `tu-${id}`,
    description: `Task ${id}`,
    taskType: 'Monitor',
    startedAt: new Date(Date.now() - 5000).toISOString(),
    status: 'in_progress',
    notificationCount: 0,
    ...overrides,
  }
}

function makeSubagent(id: string, overrides: Partial<SubagentEntry> = {}): SubagentEntry {
  return {
    hookId: id,
    agentType: 'general-purpose',
    startedAt: new Date(Date.now() - 3000).toISOString(),
    ...overrides,
  }
}

const defaultProps = {
  bgTasks: {} as Record<string, BgTask>,
  subagents: {} as Record<string, SubagentEntry>,
  inFlight: false,
  bgTaskLogs: {} as Record<string, string>,
  onSubscribeLog: vi.fn(),
  onUnsubscribeLog: vi.fn(),
  onFetchLogTail: vi.fn().mockResolvedValue({ bytes: btoa('hello'), size: 5, truncated: false }),
  open: true,
  onToggle: vi.fn(),
  connState: 'open' as const,
  turnsLoaded: true,
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('BackgroundTasksPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders 0 from empty maps and disables interactions', () => {
    render(<BackgroundTasksPanel {...defaultProps} />)
    // Panel header should be present
    expect(screen.getByRole('region', { name: /background tasks/i })).toBeTruthy()
    // No group headings rendered (all empty)
    expect(screen.queryByText('Background bash')).toBeNull()
    expect(screen.queryByText(/Subagents/i)).toBeNull()
    expect(screen.queryByText('Recently finished')).toBeNull()
    // Main Claude idle since no inFlight and no tasks
    expect(screen.getByText(/idle/i)).toBeTruthy()
    // N=0 running
    expect(screen.getByText(/0 running/i)).toBeTruthy()
  })

  it('N counts bgTasks+subagents in_progress only', () => {
    const finishedAt = new Date(Date.now() - 10000).toISOString() // 10s ago → still in 60s window
    const bgTasks = {
      t1: makeTask('t1', { status: 'in_progress' }),
      t2: makeTask('t2', { status: 'completed', finishedAt }),
    }
    const subagents = {
      s1: makeSubagent('s1'), // no finishedAt → active
    }
    render(
      <BackgroundTasksPanel
        {...defaultProps}
        bgTasks={bgTasks}
        subagents={subagents}
        inFlight={true}
      />
    )
    // N should be 2 (1 in_progress bgTask + 1 active subagent), NOT 3
    expect(screen.getByText(/2 running/i)).toBeTruthy()
    // Recently finished row should appear (t2 within 60s window)
    expect(screen.getByText('Recently finished')).toBeTruthy()
  })

  it('Bash task without Monitor name still renders', () => {
    const bgTasks = {
      bash1: makeTask('bash1', { taskType: 'Bash', description: 'Run build' }),
    }
    render(<BackgroundTasksPanel {...defaultProps} bgTasks={bgTasks} />)
    // The group heading should appear
    expect(screen.getByText('Background bash')).toBeTruthy()
    // The task row should appear using description
    expect(screen.getByText('Run build')).toBeTruthy()
  })

  it('60s fade uses Date diff, not setTimeout', () => {
    // Set a fixed "now" at T=0
    const now = 1_700_000_000_000
    vi.useFakeTimers()
    vi.setSystemTime(now)

    const finishedAt = new Date(now - 59_000).toISOString() // 59s ago → within 60s window

    const bgTasks = {
      done1: makeTask('done1', {
        status: 'completed',
        finishedAt,
      }),
    }

    const { rerender } = render(
      <BackgroundTasksPanel {...defaultProps} bgTasks={bgTasks} />
    )
    // Within 60s → should render the recently-finished row
    expect(screen.getByText('Recently finished')).toBeTruthy()

    // Advance time to 61s after finishedAt → now the task should be gone
    vi.setSystemTime(now + 2_001) // now - 59000 + 2001 = 61001ms after finishedAt

    // Re-render to pick up new Date.now()
    rerender(<BackgroundTasksPanel {...defaultProps} bgTasks={bgTasks} />)

    // Task has faded (> 60s old)
    expect(screen.queryByText('Recently finished')).toBeNull()
  })

  it('panel hidden when !turnsLoaded shows skeleton', () => {
    render(<BackgroundTasksPanel {...defaultProps} turnsLoaded={false} />)
    // Should show loading skeleton
    expect(screen.getByText(/loading/i)).toBeTruthy()
    // Should NOT show bg tasks list
    expect(screen.queryByText(/0 running/i)).toBeNull()
  })

  it('View logs button triggers onFetchLogTail then onSubscribeLog', async () => {
    const onFetchLogTail = vi.fn().mockResolvedValue({ bytes: btoa('log output'), size: 10, truncated: false })
    const onSubscribeLog = vi.fn()
    const bgTasks = {
      t1: makeTask('t1', { status: 'in_progress' }),
    }
    render(
      <BackgroundTasksPanel
        {...defaultProps}
        bgTasks={bgTasks}
        onFetchLogTail={onFetchLogTail}
        onSubscribeLog={onSubscribeLog}
      />
    )
    const viewLogsBtn = screen.getByRole('button', { name: /view logs/i })
    fireEvent.click(viewLogsBtn)

    // fetchLogTail should be called first
    expect(onFetchLogTail).toHaveBeenCalledWith('t1')

    // After the promise resolves, subscribeLog should be called
    await waitFor(() => expect(onSubscribeLog).toHaveBeenCalledWith('t1'))

    // Assert order: fetchLogTail before subscribeLog
    const fetchCallOrder = onFetchLogTail.mock.invocationCallOrder[0]
    const subscribeCallOrder = onSubscribeLog.mock.invocationCallOrder[0]
    expect(fetchCallOrder).toBeLessThan(subscribeCallOrder)
  })

  it('onFetchLogTail returning log_unavailable does not call onSubscribeLog', async () => {
    const onFetchLogTail = vi.fn().mockResolvedValue({ status: 'log_unavailable' })
    const onSubscribeLog = vi.fn()
    const bgTasks = {
      t1: makeTask('t1', { status: 'in_progress' }),
    }
    render(
      <BackgroundTasksPanel
        {...defaultProps}
        bgTasks={bgTasks}
        onFetchLogTail={onFetchLogTail}
        onSubscribeLog={onSubscribeLog}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /view logs/i }))
    await waitFor(() => expect(onFetchLogTail).toHaveBeenCalled())
    // Give time for any potential subscribe call
    await new Promise((r) => setTimeout(r, 0))
    expect(onSubscribeLog).not.toHaveBeenCalled()
    // Should show unavailable message
    expect(screen.getByText(/no log captured/i)).toBeTruthy()
  })

  it('unmount triggers onUnsubscribeLog for every active log subscription', async () => {
    const onFetchLogTail = vi.fn().mockResolvedValue({ bytes: btoa('log'), size: 3, truncated: false })
    const onSubscribeLog = vi.fn()
    const onUnsubscribeLog = vi.fn()
    const bgTasks = {
      t1: makeTask('t1', { status: 'in_progress' }),
      t2: makeTask('t2', { status: 'in_progress', description: 'Task t2' }),
    }
    const { unmount } = render(
      <BackgroundTasksPanel
        {...defaultProps}
        bgTasks={bgTasks}
        onFetchLogTail={onFetchLogTail}
        onSubscribeLog={onSubscribeLog}
        onUnsubscribeLog={onUnsubscribeLog}
      />
    )

    // Open View logs for both tasks
    const buttons = screen.getAllByRole('button', { name: /view logs/i })
    expect(buttons).toHaveLength(2)
    fireEvent.click(buttons[0])
    await waitFor(() => expect(onSubscribeLog).toHaveBeenCalledTimes(1))
    fireEvent.click(buttons[1])
    await waitFor(() => expect(onSubscribeLog).toHaveBeenCalledTimes(2))

    // Unmount — cleanup should unsubscribe both
    unmount()
    expect(onUnsubscribeLog).toHaveBeenCalledTimes(2)
    expect(onUnsubscribeLog).toHaveBeenCalledWith('t1')
    expect(onUnsubscribeLog).toHaveBeenCalledWith('t2')
  })

  it('returns null when open === false', () => {
    const { container } = render(<BackgroundTasksPanel {...defaultProps} open={false} />)
    expect(container.firstChild).toBeNull()
  })
})
