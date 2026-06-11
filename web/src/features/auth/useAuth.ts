import { useCallback, useEffect, useState } from 'react'
import { login as apiLogin, setOn401 } from '../../lib/api'

const KEY = 'alfred_token'

export function useAuth() {
  const [token, setToken] = useState<string>(() => localStorage.getItem(KEY) ?? '')

  useEffect(() => {
    setOn401(() => {
      localStorage.removeItem(KEY)
      setToken('')
    })
  }, [])

  const login = useCallback(async (user: string, password: string) => {
    const { token } = await apiLogin(user, password)
    localStorage.setItem(KEY, token)
    setToken(token)
  }, [])

  const logout = useCallback(() => {
    localStorage.removeItem(KEY)
    setToken('')
  }, [])

  return { token, isAuthenticated: token.length > 0, login, logout }
}
