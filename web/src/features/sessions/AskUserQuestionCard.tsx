import { useState } from 'react'
import type { ClaudeQuestionRequest } from './types'
import './AskUserQuestionCard.css'

interface Props {
  request: ClaudeQuestionRequest
  onSubmit: (toolUseId: string, formattedAnswer: string) => void
  onCancel: (toolUseId: string) => void
}

// AskUserQuestionCard renders Claude's AskUserQuestion tool call as a
// proper Q&A form. Each `question` in the tool input becomes a section
// with radio (multiSelect=false) or checkbox (multiSelect=true)
// options, plus an "Other" free-text field. On submit, the answers
// are stringified into a single line that gets returned to Claude as
// the tool_result via the PreToolUse hook's deny-reason channel.
export function AskUserQuestionCard({ request, onSubmit, onCancel }: Props) {
  // Per-question answer state: index -> selected labels (single-elem
  // array for radio, multi for checkbox), plus a "free text" slot
  // that's used when "Other" is picked.
  const [selected, setSelected] = useState<Record<number, string[]>>({})
  const [other, setOther] = useState<Record<number, string>>({})

  function toggle(qIdx: number, label: string, multi: boolean) {
    setSelected((prev) => {
      const cur = prev[qIdx] ?? []
      if (multi) {
        const has = cur.includes(label)
        return { ...prev, [qIdx]: has ? cur.filter((l) => l !== label) : [...cur, label] }
      }
      return { ...prev, [qIdx]: [label] }
    })
  }

  function submit() {
    const parts: string[] = []
    for (let i = 0; i < request.questions.length; i++) {
      const q = request.questions[i]
      const picks = (selected[i] ?? []).map((label) => {
        if (label === '__other__') {
          const txt = (other[i] ?? '').trim()
          return txt ? `Other: ${txt}` : null
        }
        return label
      }).filter((x): x is string => x != null)
      if (picks.length === 0) continue
      parts.push(`Q: ${q.question}\nA: ${picks.join(' | ')}`)
    }
    if (parts.length === 0) {
      // User clicked Submit with nothing selected — treat as a cancel
      // so Claude isn't told "the user answered with nothing".
      onCancel(request.toolUseId)
      return
    }
    onSubmit(request.toolUseId, parts.join('\n\n'))
  }

  const canSubmit = request.questions.some((_, i) => {
    const picks = selected[i] ?? []
    if (picks.length === 0) return false
    // If "Other" is picked, the free-text field must have content.
    if (picks.includes('__other__') && !(other[i] ?? '').trim()) return false
    return true
  })

  return (
    <div className="ask-question" role="group" aria-label="Claude is asking">
      <div className="ask-question__header">
        <span className="ask-question__icon" aria-hidden="true">?</span>
        <span className="ask-question__title">Claude has a question</span>
      </div>
      {request.questions.map((q, qIdx) => {
        const picks = selected[qIdx] ?? []
        const name = `q-${request.toolUseId}-${qIdx}`
        return (
          <div key={qIdx} className="ask-question__q">
            {q.header && <div className="ask-question__chip">{q.header}</div>}
            <div className="ask-question__prompt">{q.question}</div>
            <div className="ask-question__options">
              {q.options.map((opt, oi) => (
                <label key={oi} className={`ask-question__option ${picks.includes(opt.label) ? 'is-selected' : ''}`}>
                  <input
                    type={q.multiSelect ? 'checkbox' : 'radio'}
                    name={name}
                    checked={picks.includes(opt.label)}
                    onChange={() => toggle(qIdx, opt.label, q.multiSelect)}
                  />
                  <div>
                    <div className="ask-question__option-label">{opt.label}</div>
                    {opt.description && <div className="ask-question__option-desc">{opt.description}</div>}
                  </div>
                </label>
              ))}
              <label className={`ask-question__option ${picks.includes('__other__') ? 'is-selected' : ''}`}>
                <input
                  type={q.multiSelect ? 'checkbox' : 'radio'}
                  name={name}
                  checked={picks.includes('__other__')}
                  onChange={() => toggle(qIdx, '__other__', q.multiSelect)}
                />
                <div className="ask-question__other">
                  <div className="ask-question__option-label">Other</div>
                  {picks.includes('__other__') && (
                    <input
                      type="text"
                      className="ask-question__other-input"
                      placeholder="Type your answer…"
                      value={other[qIdx] ?? ''}
                      onChange={(e) => setOther((p) => ({ ...p, [qIdx]: e.target.value }))}
                      autoFocus
                    />
                  )}
                </div>
              </label>
            </div>
          </div>
        )
      })}
      <div className="ask-question__actions">
        <button
          type="button"
          className="ask-question__btn"
          onClick={() => onCancel(request.toolUseId)}
        >
          Cancel
        </button>
        <button
          type="button"
          className="ask-question__btn ask-question__btn--submit"
          onClick={submit}
          disabled={!canSubmit}
        >
          Submit
        </button>
      </div>
    </div>
  )
}
