import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ClaudeCredentialsDialog } from './ClaudeCredentialsDialog'

vi.mock('../../lib/api', () => ({
  saveAnthropicCredentials: vi.fn(),
}))

import { saveAnthropicCredentials } from '../../lib/api'

const VALID_JSON =
  '{"claudeAiOauth":{"accessToken":"sk-ant-oat-x","refreshToken":"sk-ant-ort-x","expiresAt":9999999999000,"scopes":["user:inference"]}}'

describe('ClaudeCredentialsDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders a textarea and the Save button starts disabled', () => {
    render(<ClaudeCredentialsDialog onClose={() => {}} />)
    const textarea = screen.getByPlaceholderText(/claudeAiOauth/i)
    expect(textarea).toBeTruthy()
    const save = screen.getByRole('button', { name: /Save/i }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
  })

  it('enables Save once non-empty JSON is pasted', () => {
    render(<ClaudeCredentialsDialog onClose={() => {}} />)
    const textarea = screen.getByPlaceholderText(/claudeAiOauth/i)
    fireEvent.change(textarea, { target: { value: VALID_JSON } })
    const save = screen.getByRole('button', { name: /Save/i }) as HTMLButtonElement
    expect(save.disabled).toBe(false)
  })

  it('Save calls saveAnthropicCredentials with the trimmed body', async () => {
    ;(saveAnthropicCredentials as any).mockResolvedValue(undefined)
    render(<ClaudeCredentialsDialog onClose={() => {}} />)
    const textarea = screen.getByPlaceholderText(/claudeAiOauth/i)
    fireEvent.change(textarea, { target: { value: '  ' + VALID_JSON + '  ' } })
    fireEvent.click(screen.getByRole('button', { name: /Save/i }))
    await waitFor(() => expect(saveAnthropicCredentials).toHaveBeenCalledWith(VALID_JSON))
  })

  it('shows the inline error when the server rejects', async () => {
    ;(saveAnthropicCredentials as any).mockRejectedValue(new Error('bad_field'))
    render(<ClaudeCredentialsDialog onClose={() => {}} />)
    const textarea = screen.getByPlaceholderText(/claudeAiOauth/i)
    fireEvent.change(textarea, { target: { value: VALID_JSON } })
    fireEvent.click(screen.getByRole('button', { name: /Save/i }))
    await waitFor(() => expect(screen.getByText('bad_field')).toBeTruthy())
  })

  it('Cancel triggers onClose', () => {
    const onClose = vi.fn()
    render(<ClaudeCredentialsDialog onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('clears the textarea after a successful save (no secret lingering in DOM)', async () => {
    ;(saveAnthropicCredentials as any).mockResolvedValue(undefined)
    render(<ClaudeCredentialsDialog onClose={() => {}} />)
    const textarea = screen.getByPlaceholderText(/claudeAiOauth/i) as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: VALID_JSON } })
    fireEvent.click(screen.getByRole('button', { name: /Save/i }))
    await waitFor(() => expect(screen.getByText(/Saved/i)).toBeTruthy())
    expect(textarea.value).toBe('')
  })
})
