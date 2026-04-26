// useLongPress — fires `onLongPress(x, y)` when the user holds the pointer
// for `delay` ms without significant movement. Designed for surfacing the
// generic context-menu on touch devices where right-click isn't available.
//
// Returns props you spread onto the target element.

import { useCallback, useRef } from 'react'

interface Options {
  delay?: number
  /** Cancel if pointer moves more than this many px from the initial point. */
  moveTolerance?: number
}

export function useLongPress(
  onLongPress: (x: number, y: number) => void,
  { delay = 500, moveTolerance = 8 }: Options = {},
) {
  const timer = useRef<number | null>(null)
  const start = useRef<{ x: number; y: number } | null>(null)
  const fired = useRef(false)

  const cancel = useCallback(() => {
    if (timer.current !== null) {
      window.clearTimeout(timer.current)
      timer.current = null
    }
    start.current = null
  }, [])

  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      // Only react to touch / pen / left-button mouse — never right-click,
      // which already opens the context menu via onContextMenu.
      if (e.pointerType === 'mouse' && e.button !== 0) return
      fired.current = false
      start.current = { x: e.clientX, y: e.clientY }
      const x = e.clientX
      const y = e.clientY
      timer.current = window.setTimeout(() => {
        fired.current = true
        timer.current = null
        onLongPress(x, y)
      }, delay)
    },
    [onLongPress, delay],
  )

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      if (!start.current || timer.current === null) return
      const dx = e.clientX - start.current.x
      const dy = e.clientY - start.current.y
      if (dx * dx + dy * dy > moveTolerance * moveTolerance) cancel()
    },
    [cancel, moveTolerance],
  )

  const onPointerUp = useCallback(() => cancel(), [cancel])
  const onPointerCancel = useCallback(() => cancel(), [cancel])

  // Suppress the synthetic click that follows a long-press, so the underlying
  // button doesn't also activate.
  const onClickCapture = useCallback((e: React.MouseEvent) => {
    if (fired.current) {
      e.preventDefault()
      e.stopPropagation()
      fired.current = false
    }
  }, [])

  return {
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
    onClickCapture,
  }
}
