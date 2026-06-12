import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { SessionsSidebar } from './SessionsSidebar'
import { Session } from '../../lib/api'

function sess(id: string, name: string): Session {
  return { id, name, created_at: '2026-06-11T00:00:00Z' }
}

const MAX = 8

describe('SessionsSidebar', () => {
  it('renders New chat button enabled when under limit', () => {
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    const btn = screen.getByRole('button', { name: /new chat/i })
    expect(btn).not.toBeDisabled()
  })

  it('disables New chat at limit', () => {
    const many: Session[] = Array.from({ length: MAX }, (_, i) => sess('S' + i, 'S' + i))
    render(
      <SessionsSidebar
        sessions={many}
        selectedSessionID="S0"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    expect(screen.getByRole('button', { name: /new chat/i })).toBeDisabled()
  })

  it('calls onCreate when New chat is clicked', () => {
    const onCreate = vi.fn()
    render(
      <SessionsSidebar
        sessions={[]}
        selectedSessionID={null}
        maxSessions={MAX}
        onCreate={onCreate}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /new chat/i }))
    expect(onCreate).toHaveBeenCalled()
  })

  it('highlights the selected session row', () => {
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A'), sess('B', 'B')]}
        selectedSessionID="B"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    const rowB = screen.getByText('B').closest('[data-testid="session-row"]')
    expect(rowB?.className).toMatch(/is-selected/)
  })

  it('calls onSelect on click', () => {
    const onSelect = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A'), sess('B', 'B')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={onSelect}
        onRename={() => {}}
        onClose={() => {}}
      />,
    )
    fireEvent.click(screen.getByText('B'))
    expect(onSelect).toHaveBeenCalledWith('B')
  })

  it('double-click swaps name to an input; Enter commits and calls onRename', () => {
    const onRename = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'Session 1')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={onRename}
        onClose={() => {}}
      />,
    )
    fireEvent.doubleClick(screen.getByText('Session 1'))
    const input = screen.getByDisplayValue('Session 1') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'training' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onRename).toHaveBeenCalledWith('A', 'training')
  })

  it('Esc cancels rename without calling onRename', () => {
    const onRename = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'Session 1')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={onRename}
        onClose={() => {}}
      />,
    )
    fireEvent.doubleClick(screen.getByText('Session 1'))
    const input = screen.getByDisplayValue('Session 1')
    fireEvent.change(input, { target: { value: 'changed' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onRename).not.toHaveBeenCalled()
    // Original name still shown.
    expect(screen.getByText('Session 1')).toBeTruthy()
  })

  it('× button calls onClose with session id', () => {
    const onClose = vi.fn()
    render(
      <SessionsSidebar
        sessions={[sess('A', 'A')]}
        selectedSessionID="A"
        maxSessions={MAX}
        onCreate={() => {}}
        onSelect={() => {}}
        onRename={() => {}}
        onClose={onClose}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /close session/i }))
    expect(onClose).toHaveBeenCalledWith('A')
  })
})
