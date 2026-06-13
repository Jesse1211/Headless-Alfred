import { describe, it, expect, beforeAll } from 'vitest'
import { render } from '@testing-library/react'
import ChatStream from './ChatStream'
import { CompletedMsg } from './types'

// jsdom doesn't implement scrollIntoView; ChatStream calls it inside a
// useEffect on mount.
beforeAll(() => {
  Element.prototype.scrollIntoView = function () {}
})

const msg = (id: string, command: string, output: string): CompletedMsg => ({
  id,
  command,
  output,
  startedAt: '2026-06-13T00:00:00Z',
  finishedAt: '2026-06-13T00:00:01Z',
  exitCode: 0,
  status: 'completed',
  truncated: false,
})

describe('ChatStream — ANSI rendering', () => {
  it('renders plain text unchanged', () => {
    const m = msg('a', 'ls', 'README.md\npackage.json\n')
    render(<ChatStream messages={[m]} running={null} />)
    const pre = document.querySelector('pre.msg__output')
    expect(pre?.textContent).toContain('README.md')
    expect(pre?.textContent).toContain('package.json')
  })

  it('renders ANSI color codes as styled spans', () => {
    // \x1b[31m is red, \x1b[0m resets
    const m = msg('a', 'git status', '\x1b[31mmodified:\x1b[0m foo.go\n')
    render(<ChatStream messages={[m]} running={null} />)
    const pre = document.querySelector('pre.msg__output')
    // The colored span should exist
    const span = pre?.querySelector('span[style*="color"]')
    expect(span).not.toBeNull()
    expect(span?.textContent).toBe('modified:')
    // The trailing un-colored text should still be present
    expect(pre?.textContent).toContain('foo.go')
  })

  it('HTML-escapes literal angle brackets in output (XSS guard)', () => {
    const m = msg('a', 'echo', '<script>alert(1)</script>\n')
    render(<ChatStream messages={[m]} running={null} />)
    const pre = document.querySelector('pre.msg__output')
    // The <script> must appear as visible text, not as an actual element.
    expect(pre?.querySelector('script')).toBeNull()
    expect(pre?.textContent).toContain('<script>alert(1)</script>')
  })

  it('handles live running output too', () => {
    render(
      <ChatStream
        messages={[]}
        running={{
          id: 'r1',
          command: 'git log',
          startedAt: '2026-06-13T00:00:00Z',
          output: '\x1b[33mcommit abc\x1b[0m\n',
          truncatedLossWarned: false,
        }}
      />,
    )
    const pre = document.querySelector('pre.msg__output')
    expect(pre?.textContent).toContain('commit abc')
    expect(pre?.querySelector('span[style*="color"]')).not.toBeNull()
  })

  it('removes stray CR characters introduced by PTY CRLF', () => {
    const m = msg('a', 'cmd', 'one\r\ntwo\r\nthree\n')
    render(<ChatStream messages={[m]} running={null} />)
    const pre = document.querySelector('pre.msg__output')
    // \r should be normalized; text content should not contain it.
    expect(pre?.textContent).not.toContain('\r')
    expect(pre?.textContent).toContain('one')
    expect(pre?.textContent).toContain('two')
    expect(pre?.textContent).toContain('three')
  })
})
