import { useEffect, useState } from 'react'
import { saveGitCredentials } from '../../lib/api'
import './GitCredentialsDialog.css'

interface Props {
  onClose: () => void
}

export function GitCredentialsDialog({ onClose }: Props) {
  const [host, setHost] = useState('github.com')
  const [username, setUsername] = useState('')
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await saveGitCredentials({
        host: host.trim(),
        username: username.trim(),
        token: token.trim(),
      })
      setSaved(true)
      // Auto-close after a brief confirmation flash.
      setTimeout(onClose, 800)
    } catch (e: any) {
      setError(e?.message ?? 'Failed to save')
    } finally {
      setBusy(false)
    }
  }

  const canSubmit = !busy && host.trim() && username.trim() && token.trim()

  return (
    <div className="git-creds__backdrop" onClick={onClose}>
      <div
        className="git-creds"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="git-creds-title"
      >
        <h2 className="git-creds__title" id="git-creds-title">Git credentials</h2>
        <p className="git-creds__body">
          Paste a username and a personal access token (PAT). It's stored on the
          server in <code>~/.git-credentials</code> so <code>git clone / pull /
          push</code> against this host won't prompt or echo the token into the
          chat output.
        </p>
        <form onSubmit={onSubmit} className="git-creds__form">
          <label>
            Host
            <input
              type="text"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              placeholder="github.com"
              autoComplete="off"
              required
            />
          </label>
          <label>
            Username
            <input
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="your-github-username"
              autoComplete="off"
              required
            />
          </label>
          <label>
            Personal Access Token
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="ghp_…"
              autoComplete="off"
              required
            />
          </label>
          {error && <div className="git-creds__error">{error}</div>}
          {saved && <div className="git-creds__ok">Saved ✓</div>}
          <div className="git-creds__actions">
            <button type="button" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={!canSubmit} className="git-creds__submit">
              {busy ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
