import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { GitCredentialsDialog } from './GitCredentialsDialog'

vi.mock('../../lib/api', () => ({
  saveGitCredentials: vi.fn(),
}))

import { saveGitCredentials } from '../../lib/api'

describe('GitCredentialsDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders all three inputs with github.com defaulted as host', () => {
    render(<GitCredentialsDialog onClose={() => {}} />)
    const host = screen.getByLabelText(/Host/i) as HTMLInputElement
    expect(host.value).toBe('github.com')
    expect(screen.getByLabelText(/Username/i)).toBeTruthy()
    expect(screen.getByLabelText(/Personal Access Token/i)).toBeTruthy()
  })

  it('Save button is disabled until all fields filled', () => {
    render(<GitCredentialsDialog onClose={() => {}} />)
    const save = screen.getByRole('button', { name: /Save/i }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText(/Username/i), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText(/Personal Access Token/i), { target: { value: 'ghp_x' } })
    expect(save.disabled).toBe(false)
  })

  it('submits and calls saveGitCredentials with the trimmed payload', async () => {
    ;(saveGitCredentials as any).mockResolvedValue(undefined)
    const onClose = vi.fn()
    render(<GitCredentialsDialog onClose={onClose} />)
    fireEvent.change(screen.getByLabelText(/Username/i), { target: { value: '  alice  ' } })
    fireEvent.change(screen.getByLabelText(/Personal Access Token/i), { target: { value: 'ghp_secret' } })
    fireEvent.click(screen.getByRole('button', { name: /Save/i }))
    await waitFor(() =>
      expect(saveGitCredentials).toHaveBeenCalledWith({
        host: 'github.com',
        username: 'alice',
        token: 'ghp_secret',
      }),
    )
  })

  it('shows error message when save fails', async () => {
    ;(saveGitCredentials as any).mockRejectedValue(new Error('boom'))
    render(<GitCredentialsDialog onClose={() => {}} />)
    fireEvent.change(screen.getByLabelText(/Username/i), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText(/Personal Access Token/i), { target: { value: 'tok' } })
    fireEvent.click(screen.getByRole('button', { name: /Save/i }))
    await waitFor(() => expect(screen.getByText('boom')).toBeTruthy())
  })

  it('Cancel triggers onClose', () => {
    const onClose = vi.fn()
    render(<GitCredentialsDialog onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('Token input has type=password (not displayed in plaintext)', () => {
    render(<GitCredentialsDialog onClose={() => {}} />)
    const tok = screen.getByLabelText(/Personal Access Token/i) as HTMLInputElement
    expect(tok.type).toBe('password')
  })
})
