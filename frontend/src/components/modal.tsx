import { type ReactNode, useEffect } from 'react'

import styles from './modal.module.css'

interface Props {
  open: boolean
  onClose: () => void
  title?: string
  children: ReactNode
  width?: number
}

export function Modal({ open, onClose, title, children, width = 480 }: Props) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null
  return (
    <div className={styles.overlay} onClick={onClose}>
      <div
        className={styles.card}
        style={{ width }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        {title && (
          <header className={styles.header}>
            <h2 className={styles.title}>{title}</h2>
            <button
              className={styles.close}
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
}
