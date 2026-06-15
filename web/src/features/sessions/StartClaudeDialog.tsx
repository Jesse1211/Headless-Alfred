import { useEffect, useState } from 'react'
import type { ClaudeRenderer } from '../../lib/ws'
import './StartClaudeDialog.css'

interface Props {
  defaultRenderer?: ClaudeRenderer
  onStart: (renderer: ClaudeRenderer) => void
  onCancel: () => void
}

// StartClaudeDialog asks the user which renderer to use before entering Claude.
// Renderer is locked at entry; switching requires Exit Claude then re-entering.
export function StartClaudeDialog({ defaultRenderer = 'ui', onStart, onCancel }: Props) {
  const [renderer, setRenderer] = useState<ClaudeRenderer>(defaultRenderer)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onCancel()
      if (e.key === 'Enter') onStart(renderer)
    }
    // Defer attaching by one frame. The dialog is typically opened by
    // an Enter keypress in the composer (`claude` -> setStartClaudeFor);
    // if we registered the listener synchronously, the same Enter
    // keydown would still be propagating to window, and the dialog
    // would close itself with the default renderer before the user
    // ever sees it.
    const t = setTimeout(() => window.addEventListener('keydown', onKey), 0)
    return () => {
      clearTimeout(t)
      window.removeEventListener('keydown', onKey)
    }
  }, [onCancel, onStart, renderer])

  return (
    <div className="start-claude__backdrop" onClick={onCancel}>
      <div
        className="start-claude"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="start-claude-title"
      >
        <h2 id="start-claude-title" className="start-claude__title">Start Claude</h2>
        <p className="start-claude__hint">
          Pick how Claude is presented in this session. You can change it later by exiting
          Claude and starting again.
        </p>
        <div className="start-claude__options">
          <label
            className={`start-claude__option ${renderer === 'ui' ? 'is-selected' : ''}`}
          >
            <input
              type="radio"
              name="renderer"
              value="ui"
              checked={renderer === 'ui'}
              onChange={() => setRenderer('ui')}
            />
            <div>
              <div className="start-claude__option-title">Chat UI</div>
              <div className="start-claude__option-desc">
                ChatGPT-style rendered messages with markdown, tool cards, and approval prompts.
              </div>
            </div>
          </label>
          <label
            className={`start-claude__option ${renderer === 'tui' ? 'is-selected' : ''}`}
          >
            <input
              type="radio"
              name="renderer"
              value="tui"
              checked={renderer === 'tui'}
              onChange={() => setRenderer('tui')}
            />
            <div>
              <div className="start-claude__option-title">Terminal</div>
              <div className="start-claude__option-desc">
                The full Claude TUI rendered into an embedded terminal (xterm.js).
              </div>
            </div>
          </label>
        </div>
        <div className="start-claude__actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button
            type="button"
            className="start-claude__primary"
            onClick={() => onStart(renderer)}
          >
            Start
          </button>
        </div>
      </div>
    </div>
  )
}
