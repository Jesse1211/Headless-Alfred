import { useCallback, useEffect, useRef, useState } from 'react'

interface Options {
  storageKey: string
  initial: number
  min: number
  max: number
  // Direction the drag delta increases width:
  //   'right' — the divider is on the RIGHT edge of the panel (dragging right grows it)
  //   'left'  — the divider is on the LEFT  edge of the panel (dragging left grows it)
  edge: 'right' | 'left'
  // archiveThreshold (optional): if the user drags the panel below this
  // width and releases, onArchive fires and the width snaps back to
  // `min` (so re-opening uses a sane default, not a sliver). When
  // dragging, the panel may briefly shrink below `min` as feedback —
  // but is never persisted there.
  archiveThreshold?: number
  onArchive?: () => void
}

interface Result {
  width: number
  isDragging: boolean
  // Returns props to spread on the divider <div>. The caller is responsible
  // for the divider's visual styling and position.
  dividerProps: {
    onPointerDown: (e: React.PointerEvent) => void
    role: 'separator'
    'aria-orientation': 'vertical'
    tabIndex: 0
  }
}

// useResizableWidth manages a horizontally-resizable panel. The width
// persists to localStorage under `storageKey`. The returned `dividerProps`
// go onto the visible drag handle; pointer down captures it and a
// pointermove on window updates the width until pointerup.
//
// When archiveThreshold + onArchive are supplied, dragging the panel
// below the threshold and releasing fires onArchive and resets width
// to `min` (so subsequent reopen uses a sane default rather than the
// tiny dragged value).
export function useResizableWidth({
  storageKey,
  initial,
  min,
  max,
  edge,
  archiveThreshold,
  onArchive,
}: Options): Result {
  const [width, setWidth] = useState<number>(() => {
    try {
      const stored = localStorage.getItem(storageKey)
      const n = stored ? Number(stored) : NaN
      if (Number.isFinite(n) && n >= min && n <= max) return n
    } catch {
      // localStorage unavailable
    }
    return initial
  })
  const [isDragging, setIsDragging] = useState(false)

  const dragRef = useRef<{ startX: number; startW: number } | null>(null)
  // Latest values for the pointermove/up listeners (which are bound once
  // and shouldn't see stale closures).
  const stateRef = useRef({ width, archiveThreshold, onArchive, min })
  stateRef.current = { width, archiveThreshold, onArchive, min }

  useEffect(() => {
    // Don't persist intermediate drag widths if they're below `min`
    // (that's the "about to archive" zone — we want re-opens to start
    // from `min`, not from a sliver).
    if (width < min) return
    try { localStorage.setItem(storageKey, String(width)) } catch { /* ignore */ }
  }, [storageKey, width, min])

  useEffect(() => {
    function onMove(e: PointerEvent) {
      const d = dragRef.current
      if (!d) return
      const dx = e.clientX - d.startX
      const delta = edge === 'right' ? dx : -dx
      const raw = d.startW + delta
      // Floor for visual feedback: when archiveThreshold is set, allow
      // the panel to visually shrink down to 0; otherwise clamp at `min`.
      const floor = stateRef.current.archiveThreshold != null ? 0 : stateRef.current.min
      const next = Math.min(max, Math.max(floor, raw))
      setWidth(next)
    }
    function onUp() {
      if (!dragRef.current) return
      dragRef.current = null
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      setIsDragging(false)
      const s = stateRef.current
      if (s.archiveThreshold != null && s.onArchive && s.width < s.archiveThreshold) {
        // Snap width back to min BEFORE firing archive so the next open
        // uses a sane default. Note the persisted value is gated by
        // `width < min` above, so the sub-min intermediates aren't
        // saved.
        setWidth(s.min)
        s.onArchive()
      }
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [edge, max])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    dragRef.current = { startX: e.clientX, startW: width }
    setIsDragging(true)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    e.preventDefault()
  }, [width])

  return {
    width,
    isDragging,
    dividerProps: {
      onPointerDown,
      role: 'separator',
      'aria-orientation': 'vertical',
      tabIndex: 0,
    },
  }
}
