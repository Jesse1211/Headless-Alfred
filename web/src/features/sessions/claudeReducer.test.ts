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

describe('applyClaudeEvent: subagent lifecycle', () => {
  it('hook_started for SubagentStart inserts subagent entry', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'use agent')
    s = applyClaudeEvent(s, 'hook_started', {
      hook_id: 'h1',
      hook_event: 'SubagentStart',
      hook_name: 'SubagentStart',
    })
    expect(s.subagents['h1']).toBeDefined()
    expect(s.subagents['h1'].finishedAt).toBeUndefined()
  })

  it('hook_response for SubagentStop patches finishedAt', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'use agent')
    s = applyClaudeEvent(s, 'hook_started', {
      hook_id: 'h1',
      hook_event: 'SubagentStart',
      hook_name: 'SubagentStart',
    })
    // SubagentStop arrives later as its own hook_started/response pair.
    // The pairing in production is via hook_id matching — SubagentStart
    // and SubagentStop have DIFFERENT hook_ids; we treat the existence
    // of ANY SubagentStop as evidence the *prior* SubagentStart's
    // subagent has ended. For v0.4 we keep it simple: on SubagentStop's
    // hook_response, mark the OLDEST in-progress subagent as finished.
    s = applyClaudeEvent(s, 'hook_started', {
      hook_id: 'h2',
      hook_event: 'SubagentStop',
      hook_name: 'SubagentStop',
    })
    s = applyClaudeEvent(s, 'hook_response', {
      hook_id: 'h2',
      hook_event: 'SubagentStop',
      exit_code: 0,
      outcome: 'success',
    })
    expect(s.subagents['h1'].finishedAt).toBeDefined()
  })

  it('hook_started for unrelated hook event is ignored', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'do stuff')
    s = applyClaudeEvent(s, 'hook_started', {
      hook_id: 'h_pre',
      hook_event: 'PreToolUse',
      hook_name: 'PreToolUse:Bash',
    })
    expect(Object.keys(s.subagents)).toHaveLength(0)
  })
})

import { reduceClaudeMsg } from './claudeReducer'

describe('claude_exited resets bgTasks + subagents', () => {
  it('clears bgTasks and subagents maps', () => {
    const sid = 'sess1'
    let perSession = new Map()
    let s = beginClaudeTurn(emptyClaudeState(), 'use monitor')
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'task_x',
      tool_use_id: 'tu_1',
      description: 'x',
      task_type: 'local_bash',
    })
    perSession.set(sid, {
      running: null, messages: [], messagesLoaded: false,
      mode: 'claude', renderer: 'ui',
      claude: s,
    })
    const next = reduceClaudeMsg(perSession, { type: 'claude_exited', sessionID: sid })!
    const after = next.get(sid)!.claude!
    expect(Object.keys(after.bgTasks)).toHaveLength(0)
    expect(Object.keys(after.subagents)).toHaveLength(0)
  })
})

describe('applyClaudeEvent: turn finishedAt', () => {
  it('result event stamps finishedAt on the live turn', () => {
    const t0 = Date.now()
    let s = beginClaudeTurn(emptyClaudeState(), 'go')
    s = applyClaudeEvent(s, 'result', { is_error: false, total_cost_usd: 0.01 })
    const fin = s.turns[0].finishedAt
    expect(fin).toBeDefined()
    const ms = Date.parse(fin!)
    expect(ms).toBeGreaterThanOrEqual(t0)
    expect(ms).toBeLessThan(t0 + 2_000)
    expect(s.turns[0].done).toBe(true)
  })
})

describe('finalizeInFlightTurn: backstop finishedAt', () => {
  it('stamps finishedAt when a runner death finalises an open turn', async () => {
    // Use reduceClaudeMsg via claude_run_ended so we exercise the same
    // codepath the WS backstop hits when the runner exits without a
    // result event.
    const sid = 'sess1'
    const perSession = new Map()
    const start = beginClaudeTurn(emptyClaudeState(), 'crash me')
    perSession.set(sid, {
      running: null, messages: [], messagesLoaded: false,
      mode: 'claude', renderer: 'ui',
      claude: start,
    })
    const t0 = Date.now()
    const next = reduceClaudeMsg(perSession, {
      type: 'claude_run_ended', sessionID: sid, message: 'killed',
    })!
    const turn = next.get(sid)!.claude!.turns[0]
    expect(turn.done).toBe(true)
    expect(turn.finishedAt).toBeDefined()
    expect(Date.parse(turn.finishedAt!)).toBeGreaterThanOrEqual(t0)
  })
})

describe('applyClaudeEvent: multi-message turn block ordering', () => {
  // Anthropic stream-json resets content-block index to 0 on each
  // assistant message_start. A single Alfred turn often spans several
  // assistant messages (text → tool_use → tool_result → new message).
  // Without resetting the per-turn index maps on message_start, the
  // second message's text_delta(index=0) gets folded back into the
  // first message's text block and its tools land at the array tail —
  // visually: all text on top, all tools sunk to the bottom, mismatching
  // the order Claude actually streamed them in. The jsonl-rebuilt history
  // does not have this bug, which is why a page refresh "fixes" it.
  it('keeps interleaved text/tool order across two assistant messages', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'do a thing')
    // --- assistant message 1 ---
    s = applyClaudeEvent(s, 'message_start', {})
    s = applyClaudeEvent(s, 'text_delta', { index: 0, text: 'first reply ' })
    s = applyClaudeEvent(s, 'tool_use_start', { index: 1, tool_use_id: 'tu_1', name: 'Bash' })
    s = applyClaudeEvent(s, 'tool_result', { tool_use_id: 'tu_1', content: 'ok', is_error: false })
    // --- assistant message 2 (index counter resets server-side) ---
    s = applyClaudeEvent(s, 'message_start', {})
    s = applyClaudeEvent(s, 'text_delta', { index: 0, text: 'second reply ' })
    s = applyClaudeEvent(s, 'tool_use_start', { index: 1, tool_use_id: 'tu_2', name: 'Read' })

    const blocks = s.turns[0].blocks
    const kinds = blocks.map((b) => (b.kind === 'tool' ? `tool:${b.tool.toolUseId}` : `text:${b.text}`))
    expect(kinds).toEqual([
      'text:first reply ',
      'tool:tu_1',
      'text:second reply ',
      'tool:tu_2',
    ])
  })

  it('keeps thinking blocks separate per assistant message', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'think hard')
    s = applyClaudeEvent(s, 'message_start', {})
    s = applyClaudeEvent(s, 'thinking_delta', { index: 0, text: 'thought A' })
    s = applyClaudeEvent(s, 'message_start', {})
    s = applyClaudeEvent(s, 'thinking_delta', { index: 0, text: 'thought B' })
    expect(s.turns[0].thinking).toEqual(['thought A', 'thought B'])
  })
})
