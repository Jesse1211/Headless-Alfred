import { useEffect, useRef, useState } from 'react'
import { getClaudeCLIVersion, upgradeClaudeCLI } from '../../lib/api'
import './GitCredentialsDialog.css'
import './ClaudeVersionDialog.css'

interface Props {
  onClose: () => void
}

// Allowed version inputs. Mirrors the server-side regex; client
// validates first to give the disable-Update-button feedback live
// rather than via 400 round-trip. Both rules MUST stay in sync —
// if you loosen one, loosen the other.
const VERSION_RE = /^(latest|next|\d+\.\d+\.\d+)$/

export function ClaudeVersionDialog({ onClose }: Props) {
  const [current, setCurrent] = useState<string | null>(null)
  const [currentErr, setCurrentErr] = useState<string | null>(null)
  const [target, setTarget] = useState('latest')
  const [busy, setBusy] = useState(false)
  const [log, setLog] = useState('')
  const logRef = useRef<HTMLPreElement>(null)

  // Fetch current version once on open.
  useEffect(() => {
    let alive = true
    getClaudeCLIVersion()
      .then((v) => { if (alive) setCurrent(v) })
      .catch((e) => { if (alive) setCurrentErr(e instanceof Error ? e.message : String(e)) })
    return () => { alive = false }
  }, [])

  // Esc to close (but not while an upgrade is streaming — that would
  // leave npm running with no UI; force the user to wait for it).
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape' && !busy) onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose, busy])

  // Auto-scroll the log to the bottom as new chunks arrive.
  useEffect(() => {
    const el = logRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [log])

  const valid = VERSION_RE.test(target)
  const canSubmit = valid && !busy

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSubmit) return
    setBusy(true)
    setLog('')
    try {
      await upgradeClaudeCLI(target, (chunk) => setLog((prev) => prev + chunk))
      // After streaming ends, re-probe to refresh the "Current"
      // line — covers the case where target was 'latest' / 'next'
      // and we want to show the concrete resolved version.
      try {
        const v = await getClaudeCLIVersion()
        setCurrent(v)
        setCurrentErr(null)
      } catch (e) {
        setCurrentErr(e instanceof Error ? e.message : String(e))
      }
    } catch (e) {
      setLog((prev) => prev + '\nerr: ' + (e instanceof Error ? e.message : String(e)) + '\n')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="git-creds__backdrop" onClick={busy ? undefined : onClose}>
      <div
        className="git-creds claude-version"
        role="dialog"
        aria-labelledby="claude-version-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="git-creds__title" id="claude-version-title">Claude CLI version</h2>
        <p className="git-creds__body">
          Upgrade the <code>claude</code> CLI installed in the pod.
          Updates write to <code>~/.npm-global/</code> on the PVC, so
          they survive pod restarts. The next time a Claude UI prompt
          runs, it forks the new version automatically — no restart
          needed.
        </p>

        <div className="claude-version__current">
          <span className="claude-version__label">Current:</span>
          {currentErr && <span className="git-creds__error">{currentErr}</span>}
          {!currentErr && (current ?? '…')}
        </div>

        <form onSubmit={onSubmit} className="git-creds__form">
          <label>
            Target version
            <input
              type="text"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              disabled={busy}
              placeholder="latest, next, or X.Y.Z (e.g. 2.1.179)"
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          {!valid && target.length > 0 && (
            <div className="git-creds__error">
              must be "latest", "next", or a strict semver like 2.1.179
            </div>
          )}

          {log && (
            <pre
              ref={logRef}
              className="claude-version__log"
              aria-live="polite"
            >{log}</pre>
          )}

          <div className="git-creds__actions">
            <button type="button" onClick={onClose} disabled={busy}>
              {busy ? 'Wait…' : 'Close'}
            </button>
            <button type="submit" disabled={!canSubmit} className="git-creds__submit">
              {busy ? 'Updating…' : 'Update'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
