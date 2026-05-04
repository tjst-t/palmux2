// S033-2-3: Portal-rendered right-click context menu for the Files tab.
// Rendered at the viewport level (document.body) via ReactDOM.createPortal
// so it floats over the list pane overflow bounds.
//
// Single-item mode: Open / Rename… / Move… / Copy path / Open on GitHub / Delete
// Batch mode (N >= 2 selected): Move N items… / Copy N paths / Delete N items
// (Rename / Open are single-item only)

import { useCallback, useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'

import styles from './files-context-menu.module.css'
import type { Entry } from './types'

export interface ContextMenuAction {
  type:
    | 'open'
    | 'rename'
    | 'move'
    | 'copy-path'
    | 'open-on-github'
    | 'delete'
    | 'batch-move'
    | 'batch-copy'
    | 'batch-delete'
}

interface Props {
  x: number
  y: number
  /** The entry that was right-clicked. Always provided. */
  target: Entry
  /** All currently-selected paths (includes target if selected). */
  selectedPaths: Set<string>
  onAction: (action: ContextMenuAction) => void
  onClose: () => void
}

export function FilesContextMenu({ x, y, target, selectedPaths, onAction, onClose }: Props) {
  const menuRef = useRef<HTMLDivElement>(null)

  // Adjust position if menu would overflow the viewport.
  const menuStyle = useAdjustedPosition(x, y, menuRef)

  // Close on outside click or Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    const onPointerDown = (e: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose()
      }
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('pointerdown', onPointerDown, true)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('pointerdown', onPointerDown, true)
    }
  }, [onClose])

  // Determine mode: batch if ≥2 items selected AND target is in selection.
  const isBatch = selectedPaths.size >= 2 && selectedPaths.has(target.path)
  const count = isBatch ? selectedPaths.size : 1

  const handle = useCallback(
    (type: ContextMenuAction['type']) => {
      onAction({ type })
      onClose()
    },
    [onAction, onClose],
  )

  const menu = (
    <div
      ref={menuRef}
      className={styles.menu}
      role="menu"
      data-testid="files-context-menu"
      style={menuStyle}
      onContextMenu={(e) => e.preventDefault()}
    >
      {!isBatch && (
        <>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('open')}
            data-testid="files-ctx-open"
          >
            <span className={styles.icon}>📂</span>
            <span>Open</span>
            <span className={styles.kbd}>↵</span>
          </button>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('rename')}
            data-testid="files-ctx-rename"
          >
            <span className={styles.icon}>✎</span>
            <span>Rename…</span>
            <span className={styles.kbd}>F2</span>
          </button>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('move')}
            data-testid="files-ctx-move"
          >
            <span className={styles.icon}>→</span>
            <span>Move…</span>
          </button>
          <div className={styles.divider} />
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('copy-path')}
            data-testid="files-ctx-copy-path"
          >
            <span className={styles.icon}>📋</span>
            <span>Copy path</span>
          </button>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('open-on-github')}
            data-testid="files-ctx-github"
          >
            <span className={styles.icon}>🌐</span>
            <span>Open on GitHub</span>
          </button>
          <div className={styles.divider} />
          <button
            className={`${styles.item} ${styles.danger}`}
            role="menuitem"
            onClick={() => handle('delete')}
            data-testid="files-ctx-delete"
          >
            <span className={styles.icon}>🗑</span>
            <span>Delete</span>
            <span className={styles.kbd} style={{ color: 'rgba(239, 68, 68, 0.55)' }}>⌫</span>
          </button>
        </>
      )}

      {isBatch && (
        <>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('batch-move')}
            data-testid="files-ctx-batch-move"
          >
            <span className={styles.icon}>→</span>
            <span>Move {count} items…</span>
          </button>
          <button
            className={styles.item}
            role="menuitem"
            onClick={() => handle('batch-copy')}
            data-testid="files-ctx-batch-copy"
          >
            <span className={styles.icon}>📋</span>
            <span>Copy {count} paths</span>
          </button>
          <div className={styles.divider} />
          <button
            className={`${styles.item} ${styles.danger}`}
            role="menuitem"
            onClick={() => handle('batch-delete')}
            data-testid="files-ctx-batch-delete"
          >
            <span className={styles.icon}>🗑</span>
            <span>Delete {count} items</span>
          </button>
        </>
      )}
    </div>
  )

  return createPortal(menu, document.body)
}

// Nudge menu position if it would overflow the viewport.
function useAdjustedPosition(
  x: number,
  y: number,
  ref: React.RefObject<HTMLDivElement | null>,
): React.CSSProperties {
  // Start at cursor, then snap after paint if overflowing.
  // We use a simple heuristic: if x + 240 > window.innerWidth, flip left.
  const menuWidth = 240
  const menuHeight = 280 // rough estimate
  const adjX = x + menuWidth > window.innerWidth ? x - menuWidth : x
  const adjY = y + menuHeight > window.innerHeight ? y - menuHeight : y
  void ref // keep linter happy
  return {
    position: 'fixed',
    left: Math.max(4, adjX),
    top: Math.max(4, adjY),
    zIndex: 200,
  }
}
