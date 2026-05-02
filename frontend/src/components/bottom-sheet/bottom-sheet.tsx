/**
 * BottomSheet — S022 mobile-first overlay container.
 *
 * On mobile widths (< 600px) the BottomSheet slides up from the bottom edge,
 * with a drag-down handle and backdrop tap to dismiss. On desktop widths it
 * falls back to a centered modal-style card so existing call sites do not
 * have to branch on viewport width themselves.
 *
 * Selectors / Drawer (mobile) / popup migration target — see
 * docs/sprint-logs/S022/decisions.md D-1 for the migration policy.
 */
import { type ReactNode, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

import styles from './bottom-sheet.module.css'

interface Props {
  open: boolean
  onClose: () => void
  /** Optional title rendered in the sheet header */
  title?: ReactNode
  /** Children rendered inside the sheet body */
  children: ReactNode
  /**
   * Maximum height as a viewport fraction (0.0 - 1.0). Defaults to 0.8.
   * Only applies on mobile.
   */
  maxHeightVh?: number
  /**
   * If true, prevents drag-down dismissal (useful for confirm dialogs that
   * must take an explicit action).
   */
  disableDrag?: boolean
  /** Extra class name for the panel */
  className?: string
  /** A11y test marker */
  'data-testid'?: string
}

export function BottomSheet({
  open,
  onClose,
  title,
  children,
  maxHeightVh = 0.8,
  disableDrag = false,
  className,
  'data-testid': testId,
}: Props) {
  const panelRef = useRef<HTMLDivElement | null>(null)
  const [dragOffset, setDragOffset] = useState(0)
  const startYRef = useRef<number | null>(null)

  // Esc to close.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  // Reset drag offset when re-opening.
  useEffect(() => {
    if (!open) setDragOffset(0)
  }, [open])

  if (!open) return null

  const onTouchStart = (e: React.TouchEvent) => {
    if (disableDrag) return
    if (e.touches.length !== 1) return
    startYRef.current = e.touches[0].clientY
  }
  const onTouchMove = (e: React.TouchEvent) => {
    if (disableDrag) return
    if (startYRef.current == null) return
    const dy = e.touches[0].clientY - startYRef.current
    if (dy > 0) setDragOffset(dy)
  }
  const onTouchEnd = () => {
    if (disableDrag) return
    if (dragOffset > 80) {
      onClose()
    }
    setDragOffset(0)
    startYRef.current = null
  }

  const styleVar: React.CSSProperties = {
    transform: dragOffset ? `translateY(${dragOffset}px)` : undefined,
    maxHeight: `${Math.round(maxHeightVh * 100)}vh`,
  }

  const sheet = (
    <div
      className={styles.overlay}
      onClick={onClose}
      role="presentation"
      data-testid={testId ? `${testId}-overlay` : undefined}
    >
      <div
        ref={panelRef}
        className={`${styles.panel} ${className ?? ''}`}
        style={styleVar}
        onClick={(e) => e.stopPropagation()}
        onTouchStart={onTouchStart}
        onTouchMove={onTouchMove}
        onTouchEnd={onTouchEnd}
        role="dialog"
        aria-modal="true"
        data-testid={testId}
      >
        {!disableDrag && <div className={styles.handle} aria-hidden="true" />}
        {title != null && (
          <header className={styles.header}>
            <h2 className={styles.title}>{title}</h2>
            <button
              className={styles.close}
              type="button"
              onClick={onClose}
              aria-label="Close"
              data-tap-mobile
            >
              ×
            </button>
          </header>
        )}
        <div className={styles.body}>{children}</div>
      </div>
    </div>
  )

  return createPortal(sheet, document.body)
}
