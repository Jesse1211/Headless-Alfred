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
}

interface Result {
  width: number
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
export function useResizableWidth({ storageKey, initial, min, max, edge }: Options): Result {
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

  const dragRef = useRef<{ startX: number; startW: number } | null>(null)

  useEffect(() => {
    try { localStorage.setItem(storageKey, String(width)) } catch { /* ignore */ }
  }, [storageKey, width])

  useEffect(() => {
    function onMove(e: PointerEvent) {
      const d = dragRef.current
      if (!d) return
      const dx = e.clientX - d.startX
      const delta = edge === 'right' ? dx : -dx
      const next = Math.min(max, Math.max(min, d.startW + delta))
      setWidth(next)
    }
    function onUp() {
      if (dragRef.current) {
        dragRef.current = null
        document.body.style.cursor = ''
        document.body.style.userSelect = ''
      }
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [edge, max, min])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    dragRef.current = { startX: e.clientX, startW: width }
    // Visual feedback while dragging.
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    e.preventDefault()
  }, [width])

  return {
    width,
    dividerProps: {
      onPointerDown,
      role: 'separator',
      'aria-orientation': 'vertical',
      tabIndex: 0,
    },
  }
}
