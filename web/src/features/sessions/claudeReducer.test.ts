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
