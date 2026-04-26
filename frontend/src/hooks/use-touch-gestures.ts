// Two-finger touch gestures for mobile users:
//   - pinch (zoom in/out) → onPinchStep(+1 / -1) for font-size adjustment
//   - horizontal swipe → onSwipe('left' | 'right') for tab switching
//
// Single-finger touches are passed through so xterm's own handling (and
// native scrolling) keeps working.

import { useCallback, useRef } from 'react'

interface Options {
  onPinchStep?: (direction: 1 | -1) => void
  onSwipe?: (direction: 'left' | 'right') => void
  /** Distance in px before a pinch step fires (per step). */
  pinchStepPx?: number
  /** Minimum X delta in px to count as a swipe. */
  swipeThresholdPx?: number
}

interface PointerSnapshot {
  x: number
  y: number
}

export function useTouchGestures({
  onPinchStep,
  onSwipe,
  pinchStepPx = 30,
  swipeThresholdPx = 60,
}: Options) {
  const start = useRef<{ a: PointerSnapshot; b: PointerSnapshot; midX: number } | null>(null)
  const lastDistance = useRef(0)
  const totalPinch = useRef(0)
  const swipeFired = useRef(false)
  const points = useRef<Map<number, PointerSnapshot>>(new Map())

  const reset = useCallback(() => {
    start.current = null
    lastDistance.current = 0
    totalPinch.current = 0
    swipeFired.current = false
    points.current.clear()
  }, [])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    if (e.pointerType !== 'touch') return
    points.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (points.current.size === 2) {
      const [a, b] = Array.from(points.current.values())
      start.current = { a, b, midX: (a.x + b.x) / 2 }
      lastDistance.current = distance(a, b)
      totalPinch.current = 0
      swipeFired.current = false
    }
  }, [])

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      if (e.pointerType !== 'touch') return
      const known = points.current.get(e.pointerId)
      if (!known) return
      points.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      if (points.current.size !== 2 || !start.current) return
      const [a, b] = Array.from(points.current.values())

      // Pinch step: cumulative distance change.
      const dist = distance(a, b)
      const delta = dist - lastDistance.current
      lastDistance.current = dist
      totalPinch.current += delta
      if (onPinchStep && Math.abs(totalPinch.current) >= pinchStepPx) {
        onPinchStep(totalPinch.current > 0 ? 1 : -1)
        totalPinch.current = 0
      }

      // Two-finger horizontal swipe: midpoint translation > threshold,
      // and distance hasn't changed much (otherwise it's a pinch).
      const midX = (a.x + b.x) / 2
      const dx = midX - start.current.midX
      const a0 = start.current.a
      const b0 = start.current.b
      const startDist = distance(a0, b0)
      const distChange = Math.abs(dist - startDist)
      if (
        onSwipe &&
        !swipeFired.current &&
        distChange < pinchStepPx &&
        Math.abs(dx) >= swipeThresholdPx
      ) {
        onSwipe(dx > 0 ? 'right' : 'left')
        swipeFired.current = true
      }
    },
    [onPinchStep, onSwipe, pinchStepPx, swipeThresholdPx],
  )

  const onPointerUp = useCallback(
    (e: React.PointerEvent) => {
      if (e.pointerType !== 'touch') return
      points.current.delete(e.pointerId)
      if (points.current.size < 2) {
        // End of multi-touch session.
        start.current = null
        lastDistance.current = 0
        totalPinch.current = 0
        swipeFired.current = false
      }
    },
    [],
  )

  const onPointerCancel = useCallback(() => reset(), [reset])

  return { onPointerDown, onPointerMove, onPointerUp, onPointerCancel }
}

function distance(a: PointerSnapshot, b: PointerSnapshot): number {
  const dx = a.x - b.x
  const dy = a.y - b.y
  return Math.sqrt(dx * dx + dy * dy)
}
