import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { TurnStatsLine, toolStatus, turnPhase } from './ClaudeChatView'
import type { BgTask, ClaudeToolCall, ClaudeTurn, SubagentEntry } from './types'

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeTurn(overrides: Partial<ClaudeTurn> = {}): ClaudeTurn {
  return {
    id: 'turn1',
    prompt: 'prompt',
    startedAt: new Date(Date.now() - 10_000).toISOString(),
    blocks: [],
    thinking: [],
    done: true,
    ...overrides,
  }
}

function makeBgTask(id: string, overrides: Partial<BgTask> = {}): BgTask {
  return {
    taskId: id,
    toolUseId: `tu-${id}`,
    description: `Task ${id}`,
    taskType: 'local_bash',
    startedAt: new Date(Date.now() - 5_000).toISOString(),
    status: 'in_progress',
    notificationCount: 0,
    ...overrides,
  }
}

function toolBlock(toolName: string, toolUseId: string, bgTaskId?: string) {
  return {
    kind: 'tool' as const,
    tool: {
      toolUseId,
      name: toolName,
      input: {},
      decision: 'allow' as const,
      ...(bgTaskId ? { bgTaskId } : {}),
    },
  }
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('TurnStatsLine', () => {
  it('renders mid-turn (no longer gated on turn.done)', () => {
    const turn = makeTurn({
      done: false,
      blocks: [toolBlock('Bash', 'tu1', 'bg1')],
    })
    const bgTasks = { bg1: makeBgTask('bg1') }
    const { container } = render(
      <TurnStatsLine turn={turn} bgTasks={bgTasks} subagents={{}} />,
    )
    // The stats div should exist even though done=false.
    expect(container.querySelector('.turn-stats')).not.toBeNull()
    // It mentions the background task count.
    expect(screen.getByText(/background task/i)).toBeInTheDocument()
  })

  it('counts Bash background tasks alongside Monitor', () => {
    const turn = makeTurn({
      blocks: [
        toolBlock('Bash', 'tu1', 'bg1'),
        toolBlock('Monitor', 'tu2', 'bg2'),
      ],
    })
    const bgTasks = {
      bg1: makeBgTask('bg1'),
      bg2: makeBgTask('bg2'),
    }
    render(
      <TurnStatsLine turn={turn} bgTasks={bgTasks} subagents={{}} />,
    )
    // Both bgTaskId-linked tool blocks count, regardless of tool name.
    // "2 background tasks" should appear in the rendered output.
    expect(screen.getByText(/2 background tasks/i)).toBeInTheDocument()
  })

  it('reports interrupted (not a plain deny) when a tool was killed mid-turn', () => {
    // The loader finalizes a runner-killed pending tool as decision=deny
    // + isError + an "Interrupted:" result. The UI must distinguish this
    // from a user-initiated deny so the chip reads "interrupted", not
    // "denied" (which would wrongly imply the user rejected it).
    const killed: ClaudeToolCall = {
      toolUseId: 't1',
      name: 'Edit',
      decision: 'deny',
      isError: true,
      result: 'Interrupted: the runner was killed (server restart) before this tool finished.',
    }
    expect(toolStatus(killed)).toBe('interrupted')
  })

  it('reports a real user deny as denied', () => {
    const denied: ClaudeToolCall = {
      toolUseId: 't2',
      name: 'Edit',
      decision: 'deny',
    }
    expect(toolStatus(denied)).toBe('denied')
  })

  it('toolStatus reads outcome=aborted as interrupted', () => {
    const tool: ClaudeToolCall = {
      toolUseId: 't1', name: 'Edit', decision: 'deny', outcome: 'aborted',
    }
    expect(toolStatus(tool)).toBe('interrupted')
  })

  it('toolStatus reads outcome=errored as errored', () => {
    const tool: ClaudeToolCall = {
      toolUseId: 't2', name: 'Bash', decision: 'allow', outcome: 'errored',
    }
    expect(toolStatus(tool)).toBe('errored')
  })

  it('turnPhase shows Interrupted for an aborted turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'aborted' }))).toBe('Interrupted')
  })

  it('turnPhase shows Error for an errored turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'errored' }))).toBe('Error')
  })

  it('turnPhase shows Done for a completed turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'completed' }))).toBe('Done')
  })

  it('still uses subagents (blocking) label for the subagent group', () => {
    const turn = makeTurn({
      blocks: [
        toolBlock('Agent', 'tu1'),
      ],
    })
    const subagents: Record<string, SubagentEntry> = {
      h1: {
        hookId: 'h1',
        agentType: 'general-purpose',
        startedAt: new Date(Date.now() - 2_000).toISOString(),
      },
    }
    render(
      <TurnStatsLine turn={turn} bgTasks={{}} subagents={subagents} />,
    )
    // Subagents label should say "(blocking)" not "(running)".
    expect(screen.getByText(/subagent.*blocking/i)).toBeInTheDocument()
  })
})
