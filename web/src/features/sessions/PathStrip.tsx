import { useCallback, useEffect, useRef, useState } from 'react'
import './PathStrip.css'

interface Props {
  path: string | null
}

// PathStrip is the thin row that shows a file's absolute on-disk
// path with a copy button. Renders nothing when path is null
// (server didn't send X-File-Path, or hasn't responded yet).
//
// Clicking the copy button writes path to the clipboard via the
// async navigator.clipboard API and flips the button to a "Copied"
// label for 1.2s. We deliberately don't fall back to the legacy
// document.execCommand path: this is a local-only dev tool served
// over loopback, where navigator.clipboard is always available.
export function PathStrip({ path }: Props) {
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<number | null>(null)

  useEffect(() => () => {
    if (timerRef.current != null) window.clearTimeout(timerRef.current)
  }, [])

  const onCopy = useCallback(() => {
    if (!path) return
    void navigator.clipboard.writeText(path).then(() => {
      setCopied(true)
      if (timerRef.current != null) window.clearTimeout(timerRef.current)
      timerRef.current = window.setTimeout(() => setCopied(false), 1200)
    })
  }, [path])

  if (!path) return null

  return (
    <div className="path-strip" role="group" aria-label="File path">
      <code className="path-strip__path" title={path}>{path}</code>
      <button
        type="button"
        className="path-strip__copy"
        onClick={onCopy}
        aria-label={copied ? 'Copied' : 'Copy path'}
      >
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
