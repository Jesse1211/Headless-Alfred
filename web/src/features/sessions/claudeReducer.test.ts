import { describe, it, expect } from 'vitest'
import { applyClaudeEvent, beginClaudeTurn } from './claudeReducer'
import { emptyClaudeState } from './types'

describe('applyClaudeEvent: tool block timestamps', () => {
  it('records startedAt on tool_use_start', () => {
    const t0 = Date.now()
    let s = beginClaudeTurn(emptyClaudeState(), 'do stuff')
    s = applyClaudeEvent(s, 'tool_use_start', {
      index: 0,
      tool_use_id: 'tu_1',
      name: 'Bash',
    })
    const block = s.turns[0].blocks[0]
    if (block.kind !== 'tool') throw new Error('expected tool block')
    expect(block.tool.startedAt).toBeDefined()
    const startedMs = Date.parse(block.tool.startedAt!)
    expect(startedMs).toBeGreaterThanOrEqual(t0)
    expect(startedMs).toBeLessThan(t0 + 2_000)
  })

  it('records finishedAt on tool_result', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'do stuff')
    s = applyClaudeEvent(s, 'tool_use_start', {
      index: 0,
      tool_use_id: 'tu_1',
      name: 'Bash',
    })
    const startMs = Date.now()
    s = applyClaudeEvent(s, 'tool_result', {
      tool_use_id: 'tu_1',
      content: 'ok',
      is_error: false,
    })
    const block = s.turns[0].blocks[0]
    if (block.kind !== 'tool') throw new Error('expected tool block')
    expect(block.tool.finishedAt).toBeDefined()
    expect(Date.parse(block.tool.finishedAt!)).toBeGreaterThanOrEqual(startMs)
  })
})

describe('applyClaudeEvent: task lifecycle', () => {
  it('task_started inserts bgTask and links tool block via bgTaskId', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'use monitor')
    s = applyClaudeEvent(s, 'tool_use_start', {
      index: 0,
      tool_use_id: 'tu_mon',
      name: 'Monitor',
    })
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'task_abc',
      tool_use_id: 'tu_mon',
      description: 'poll CI',
      task_type: 'local_bash',
    })
    expect(s.bgTasks['task_abc']).toBeDefined()
    expect(s.bgTasks['task_abc'].description).toBe('poll CI')
    expect(s.bgTasks['task_abc'].status).toBe('in_progress')
    expect(s.bgTasks['task_abc'].notificationCount).toBe(0)
    const block = s.turns[0].blocks[0]
    if (block.kind !== 'tool') throw new Error('expected tool block')
    expect(block.tool.bgTaskId).toBe('task_abc')
  })

  it('task_notification bumps count and updates lastEventSummary', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'use monitor')
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'task_abc',
      tool_use_id: 'tu_mon',
      description: 'poll CI',
      task_type: 'local_bash',
    })
    s = applyClaudeEvent(s, 'task_notification', {
      task_id: 'task_abc',
      tool_use_id: 'tu_mon',
      status: 'in_progress',
      summary: 'line 1',
    })
    s = applyClaudeEvent(s, 'task_notification', {
      task_id: 'task_abc',
      tool_use_id: 'tu_mon',
      status: 'in_progress',
      summary: 'line 2',
    })
    expect(s.bgTasks['task_abc'].notificationCount).toBe(2)
    expect(s.bgTasks['task_abc'].lastEventSummary).toBe('line 2')
  })

  it('task_updated.completed freezes the bgTask', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'use monitor')
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'task_abc',
      tool_use_id: 'tu_mon',
      description: 'poll CI',
      task_type: 'local_bash',
    })
    s = applyClaudeEvent(s, 'task_updated', {
      task_id: 'task_abc',
      patch: { status: 'completed', end_time: 1781706910801 },
    })
    expect(s.bgTasks['task_abc'].status).toBe('completed')
    expect(s.bgTasks['task_abc'].finishedAt).toBeDefined()
  })

  it('task_started for unknown tool block still tracks task', () => {
    // No matching tool_use_start — bgTask is still tracked under bgTasks
    // map even though no block links to it.
    let s = beginClaudeTurn(emptyClaudeState(), 'use monitor')
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'task_xyz',
      tool_use_id: 'tu_missing',
      description: 'orphan task',
      task_type: 'local_bash',
    })
    expect(s.bgTasks['task_xyz']).toBeDefined()
  })
})
