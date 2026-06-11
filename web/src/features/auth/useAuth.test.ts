import { renderHook, act } from '@testing-library/react'
import { useAuth } from './useAuth'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import * as api from '../../lib/api'

describe('useAuth', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('returns no token initially', () => {
    const { result } = renderHook(() => useAuth())
    expect(result.current.token).toBe('')
    expect(result.current.isAuthenticated).toBe(false)
  })

  it('login stores the token and flips isAuthenticated', async () => {
    vi.spyOn(api, 'login').mockResolvedValueOnce({ token: 'TOK' })
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await result.current.login('admin', 'pw')
    })
    expect(result.current.token).toBe('TOK')
    expect(result.current.isAuthenticated).toBe(true)
    expect(localStorage.getItem('alfred_token')).toBe('TOK')
  })

  it('logout clears the token', () => {
    localStorage.setItem('alfred_token', 'TOK')
    const { result } = renderHook(() => useAuth())
    act(() => {
      result.current.logout()
    })
    expect(result.current.token).toBe('')
    expect(localStorage.getItem('alfred_token')).toBeNull()
  })
})
