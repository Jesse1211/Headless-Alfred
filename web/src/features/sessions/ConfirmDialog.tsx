import { useEffect } from 'react'
import './ConfirmDialog.css'

interface Props {
  title: string
  body: string
  confirmLabel: string
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({ title, body, confirmLabel, onConfirm, onCancel }: Props) {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  return (
    <div className="confirm-dialog__backdrop" onClick={onCancel}>
      <div className="confirm-dialog" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <h2 className="confirm-dialog__title">{title}</h2>
        <p className="confirm-dialog__body">{body}</p>
        <div className="confirm-dialog__actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" className="confirm-dialog__danger" onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
