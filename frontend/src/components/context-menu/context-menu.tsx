import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

import { useContextMenuStore } from './store'
import styles from './context-menu.module.css'

const EDGE_PADDING = 8

export function ContextMenuRenderer() {
  const open = useContextMenuStore((s) => s.open)
  const items = useContextMenuStore((s) => s.items)
  const requestedX = useContextMenuStore((s) => s.x)
  const requestedY = useContextMenuStore((s) => s.y)
  const hide = useContextMenuStore((s) => s.hide)
  const ref = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState({ x: 0, y: 0 })

  // Recompute clamped position once the menu has measured itself, so the
  // popover always fits inside the viewport (flips at the right/bottom edge).
  useLayoutEffect(() => {
    if (!open || !ref.current) return
    const r = ref.current.getBoundingClientRect()
    const vw = window.innerWidth
    const vh = window.innerHeight
    let x = requestedX
    let y = requestedY
    if (x + r.width + EDGE_PADDING > vw) x = vw - r.width - EDGE_PADDING
    if (y + r.height + EDGE_PADDING > vh) y = vh - r.height - EDGE_PADDING
    if (x < EDGE_PADDING) x = EDGE_PADDING
    if (y < EDGE_PADDING) y = EDGE_PADDING
    setPos({ x, y })
  }, [open, requestedX, requestedY, items])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') hide()
    }
    // Capture-phase pointerdown so a click outside dismisses *before* the
    // target's own handler fires. Right-clicks elsewhere also pass through
    // here and close the menu; the new emitter (if any) will re-open it.
    const onPointerDown = (e: PointerEvent) => {
      if (!ref.current) return
      if (!ref.current.contains(e.target as Node)) hide()
    }
    const dismiss = () => hide()
    window.addEventListener('keydown', onKey)
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('scroll', dismiss, true)
    window.addEventListener('resize', dismiss)
    window.addEventListener('blur', dismiss)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('scroll', dismiss, true)
      window.removeEventListener('resize', dismiss)
      window.removeEventListener('blur', dismiss)
    }
  }, [open, hide])

  if (!open) return null

  const node = (
    <div
      ref={ref}
      className={styles.menu}
      role="menu"
      style={{ left: pos.x, top: pos.y }}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((item, i) => {
        if (item.type === 'separator') {
          return <div key={i} className={styles.separator} role="separator" />
        }
        if (item.type === 'heading') {
          return (
            <div key={i} className={styles.heading}>
              {item.label}
            </div>
          )
        }
        return (
          <button
            key={i}
            type="button"
            role="menuitem"
            className={item.danger ? `${styles.item} ${styles.danger}` : styles.item}
            disabled={item.disabled}
            onClick={() => {
              hide()
              void item.onClick()
            }}
          >
            <span>{item.label}</span>
            {item.shortcut && <span className={styles.shortcut}>{item.shortcut}</span>}
          </button>
        )
      })}
    </div>
  )
  return createPortal(node, document.body)
}
