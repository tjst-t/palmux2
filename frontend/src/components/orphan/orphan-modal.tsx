import { useEffect } from 'react'
import { createPortal } from 'react-dom'

import { TerminalView } from '../../tabs/terminal-view'

import styles from './orphan-modal.module.css'

interface Props {
  sessionName: string
  windowIdx: number
  windowName?: string
  onClose: () => void
}

// Full-screen modal that mounts a terminal connected to an orphan tmux
// session (one Palmux didn't create). Closing the modal disposes the
// terminal — there's no caching across re-opens since orphan flows are
// expected to be one-off.
export function OrphanAttachModal({ sessionName, windowIdx, windowName, onClose }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return createPortal(
    <div className={styles.overlay} onClick={onClose}>
      <div
        className={styles.frame}
        role="dialog"
        aria-label={`Orphan session ${sessionName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <header className={styles.head}>
          <div className={styles.label}>
            <span className={styles.session}>{sessionName}</span>
            <span className={styles.window}>
              :{windowIdx}
              {windowName ? ` (${windowName})` : ''}
            </span>
          </div>
          <button className={styles.close} onClick={onClose} aria-label="Close">
            ×
          </button>
        </header>
        <div className={styles.term}>
          <TerminalView orphanName={sessionName} orphanIdx={windowIdx} />
        </div>
      </div>
    </div>,
    document.body,
  )
}
