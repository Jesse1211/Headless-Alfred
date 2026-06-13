import { useEffect, useState } from 'react'
import { saveAnthropicCredentials } from '../../lib/api'
import './GitCredentialsDialog.css'

interface Props {
  onClose: () => void
}

// Where to find the file on the user's machine — shown in the dialog
// because the workflow is fiddly (Keychain on macOS, plain file on
// Linux). We give exact one-liner commands for both.
const KEYCHAIN_CMD =
  `security find-generic-password -s "Claude Code-credentials" -w`
const LINUX_PATH = `~/.claude/.credentials.json`

export function ClaudeCredentialsDialog({ onClose }: Props) {
  const [text, setText] = useState('')
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
      await saveAnthropicCredentials(text.trim())
      setSaved(true)
      // Clear the textarea so the secret doesn't linger in the DOM.
      setText('')
      setTimeout(onClose, 800)
    } catch (e: any) {
      setError(e?.message ?? 'Failed to save')
    } finally {
      setBusy(false)
    }
  }

  const canSubmit = !busy && text.trim().length > 0

  return (
    <div className="git-creds__backdrop" onClick={onClose}>
      <div
        className="git-creds"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="claude-creds-title"
      >
        <h2 className="git-creds__title" id="claude-creds-title">
          Claude (Anthropic) credentials
        </h2>
        <p className="git-creds__body">
          Paste the JSON contents of your <code>~/.claude/.credentials.json</code>
          {' '}from a machine where you've completed <code>claude /login</code>.
          The server installs it at the same path inside the pod (mode 0600),
          so any session can run <code>claude</code> against your subscription
          without going through OAuth here.
        </p>
        <details className="git-creds__details">
          <summary>How to get this JSON</summary>
          <p className="git-creds__hint">
            <strong>On macOS</strong> (the file isn't on disk — it's in the Keychain):
          </p>
          <pre className="git-creds__pre">{KEYCHAIN_CMD}</pre>
          <p className="git-creds__hint">
            <strong>On Linux</strong>:
          </p>
          <pre className="git-creds__pre">cat {LINUX_PATH}</pre>
          <p className="git-creds__hint">
            Copy the entire JSON object (starts with <code>{'{'}</code>, ends with <code>{'}'}</code>),
            paste it below.
          </p>
        </details>
        <form onSubmit={onSubmit} className="git-creds__form">
          <label>
            <span className="visually-hidden">Credentials JSON</span>
            <textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder='{"claudeAiOauth":{"accessToken":"...","refreshToken":"...","expiresAt":...,"scopes":[...]}}'
              rows={6}
              spellCheck={false}
              autoCorrect="off"
              autoCapitalize="off"
              required
              className="git-creds__textarea"
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
