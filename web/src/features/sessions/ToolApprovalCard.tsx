import { useState } from 'react'
import type { ClaudeToolApprovalRequest } from './types'
import './ToolApprovalCard.css'

interface Props {
  request: ClaudeToolApprovalRequest
  onDecide: (decision: 'allow' | 'deny', reason?: string) => void
}

// ToolApprovalCard is the synchronous Ask interaction surfaced when Claude
// requests a tool that requires user permission. There's exactly one card
// per pending request; allow/deny is final and routes through the bridge.
export function ToolApprovalCard({ request, onDecide }: Props) {
  const [showReason, setShowReason] = useState(false)
  const [reason, setReason] = useState('')

  return (
    <div className="tool-approval" role="alertdialog" aria-labelledby={`tool-${request.toolUseId}`}>
      <div className="tool-approval__header">
        <span className="tool-approval__icon" aria-hidden="true">⚙</span>
        <span id={`tool-${request.toolUseId}`} className="tool-approval__title">
          Claude wants to run <code>{request.tool}</code>
        </span>
      </div>
      <pre className="tool-approval__input">
        {formatInput(request.input)}
      </pre>
      {showReason && (
        <textarea
          className="tool-approval__reason"
          placeholder="Reason for denial (optional, shown to Claude)"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          rows={2}
        />
      )}
      <div className="tool-approval__actions">
        {!showReason && (
          <button
            type="button"
            className="tool-approval__btn"
            onClick={() => setShowReason(true)}
          >
            Deny with reason…
          </button>
        )}
        <button
          type="button"
          className="tool-approval__btn tool-approval__btn--deny"
          onClick={() => onDecide('deny', showReason ? reason.trim() || undefined : undefined)}
        >
          Deny
        </button>
        <button
          type="button"
          className="tool-approval__btn tool-approval__btn--allow"
          onClick={() => onDecide('allow')}
        >
          Allow once
        </button>
      </div>
    </div>
  )
}

function formatInput(input: unknown): string {
  if (input == null) return ''
  try {
    return JSON.stringify(input, null, 2)
  } catch {
    return String(input)
  }
}
