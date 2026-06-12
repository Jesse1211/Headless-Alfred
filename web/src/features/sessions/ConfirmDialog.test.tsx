import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

describe('ConfirmDialog', () => {
  it('renders the title and body', () => {
    render(<ConfirmDialog title="Delete?" body="Are you sure?" confirmLabel="Delete" onConfirm={() => {}} onCancel={() => {}} />)
    expect(screen.getByText('Delete?')).toBeTruthy()
    expect(screen.getByText('Are you sure?')).toBeTruthy()
  })

  it('calls onConfirm when confirm clicked', () => {
    const onConfirm = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={onConfirm} onCancel={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(onConfirm).toHaveBeenCalled()
  })

  it('calls onCancel when cancel clicked', () => {
    const onCancel = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })

  it('Esc triggers onCancel', () => {
    const onCancel = vi.fn()
    render(<ConfirmDialog title="t" body="b" confirmLabel="Delete" onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalled()
  })
})
