import { useEffect, useState } from 'react'
import type { ClaudeRenderer } from '../../lib/ws'
import './StartClaudeDialog.css'

interface Props {
  defaultRenderer?: ClaudeRenderer
  onStart: (renderer: ClaudeRenderer, bypassPermissions: boolean, templateId: string) => void
  onCancel: () => void
}

// StartClaudeDialog asks the user which renderer to use before entering Claude.
// Renderer is locked at entry; switching requires Exit Claude then re-entering.
export function StartClaudeDialog({ defaultRenderer = 'ui', onStart, onCancel }: Props) {
  const [renderer, setRenderer] = useState<ClaudeRenderer>(defaultRenderer)
  // bypass = pass --dangerously-skip-permissions to `claude -p`. Default
  // ON because in -p mode without a TTY, claude's default "ask the
  // user" path will deadlock waiting for stdin it cannot read. Our
  // PreToolUse hook still fires under bypass, so user control is
  // unaffected — Alfred remains the actual gate.
  const [bypass, setBypass] = useState(true)
  // Task-summary template default ON for Chat UI. The checkbox is
  // visually disabled (muted + non-interactive) when the renderer
  // is TUI — that mode has its own /memory machinery.
  const [summary, setSummary] = useState(true)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onCancel()
      if (e.key === 'Enter') onStart(renderer, bypass, summary && renderer === 'ui' ? 'summary-todo' : '')
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
  }, [onCancel, onStart, renderer, bypass, summary])

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
        <label className="start-claude__checkbox">
          <input
            type="checkbox"
            checked={bypass}
            onChange={(e) => setBypass(e.target.checked)}
          />
          <div>
            <div className="start-claude__checkbox-title">
              Skip Claude CLI's built-in permission prompts
            </div>
            <div className="start-claude__checkbox-desc">
              Adds <code>--dangerously-skip-permissions</code>. Alfred still asks
              you before running any tool (the card you see in the chat). Without
              this, headless Claude may deadlock waiting for keyboard input
              it can't read.
            </div>
          </div>
        </label>
        <label className={`start-claude__checkbox ${renderer === 'tui' ? 'is-disabled' : ''}`}>
          <input
            type="checkbox"
            checked={summary && renderer === 'ui'}
            onChange={(e) => setSummary(e.target.checked)}
            disabled={renderer === 'tui'}
          />
          <div>
            <div className="start-claude__checkbox-title">
              Maintain a task summary
            </div>
            <div className="start-claude__checkbox-desc">
              After every reply, Claude updates a short summary you
              can read in the right sidebar. Lets you pick up where
              you left off without re-explaining yourself.
              {renderer === 'tui' && ' (Chat UI only.)'}
            </div>
          </div>
        </label>
        <div className="start-claude__actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button
            type="button"
            className="start-claude__primary"
            onClick={() => onStart(renderer, bypass, summary && renderer === 'ui' ? 'summary-todo' : '')}
          >
            Start
          </button>
        </div>
      </div>
    </div>
  )
}
