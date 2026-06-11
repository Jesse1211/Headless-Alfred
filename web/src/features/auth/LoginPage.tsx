import { FormEvent, useState } from 'react'
import { ApiError } from '../../lib/api'
import './LoginPage.css'

interface Props {
  onLogin: (user: string, password: string) => Promise<void>
}

export default function LoginPage({ onLogin }: Props) {
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await onLogin(user, password)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.status === 429 ? 'Too many attempts, please wait a minute.' : 'Wrong username or password.')
      } else {
        setError('Network error.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={submit}>
        <h1>Headless Alfred</h1>
        <label>
          <span>Username</span>
          <input
            autoFocus
            value={user}
            onChange={(e) => setUser(e.target.value)}
            disabled={submitting}
            autoComplete="username"
          />
        </label>
        <label>
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
            autoComplete="current-password"
          />
        </label>
        {error && <div className="login-error">{error}</div>}
        <button type="submit" disabled={submitting || !user || !password}>
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
