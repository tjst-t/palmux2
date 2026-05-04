// S033-2-5: Delete confirm modal — shared between single-item and batch.
// Shows per-item path + size + last-modified; directory gets recursive warning.

import { useCallback, useState } from 'react'

import { Modal } from '../../components/modal'
import styles from './files-delete-modal.module.css'
import type { Entry } from './types'

interface Props {
  items: Entry[]
  onClose: () => void
  onConfirm: (paths: string[]) => Promise<void>
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`
}

function fmtDate(iso: string): string {
  try {
    const d = new Date(iso)
    if (Number.isNaN(+d)) return ''
    const diff = Date.now() - d.getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return 'just now'
    if (mins < 60) return `${mins} minute${mins === 1 ? '' : 's'} ago`
    const hrs = Math.floor(mins / 60)
    if (hrs < 24) return `${hrs} hour${hrs === 1 ? '' : 's'} ago`
    return d.toLocaleDateString()
  } catch {
    return ''
  }
}

export function FilesDeleteModal({ items, onClose, onConfirm }: Props) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleConfirm = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      await onConfirm(items.map((e) => e.path))
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }, [items, onConfirm, onClose])

  const count = items.length
  const title = count === 1 ? `Delete "${items[0].name}"?` : `Delete ${count} items?`

  return (
    <Modal open onClose={onClose} width={520}>
      <div data-testid="files-delete-modal">
        <div className={styles.modalHeader}>
          <h2 className={styles.title} id="files-delete-title">
            {title}
          </h2>
          <p className={styles.sub}>
            The selected files and directories will be removed from disk. Changes are uncommitted —
            Git can recover anything previously committed via <code>git restore</code>.
          </p>
        </div>

        <div className={styles.itemList}>
          {items.map((item) => (
            <div key={item.path} className={styles.itemBlock}>
              <div className={styles.itemHeader}>
                <span className={styles.icon}>{item.isDir ? '📁' : '📄'}</span>
                <span className={styles.itemPath}>{item.path}</span>
                <span className={styles.itemMeta}>{item.isDir ? 'directory' : fmtSize(item.size)}</span>
              </div>
              <div className={styles.itemSub}>
                {item.isDir ? (
                  <span className={styles.recursiveWarn}>
                    Recursive — directory and all contents will be removed
                  </span>
                ) : (
                  `Last modified ${fmtDate(item.modTime)}`
                )}
              </div>
            </div>
          ))}
        </div>

        <p className={styles.warning}>
          Deletions can only be recovered from Git history if the file was previously committed.{' '}
          Brand-new items (never staged) <strong className={styles.warningStrong}>cannot be recovered</strong>{' '}
          after delete.
        </p>

        {error && <div className={styles.error}>{error}</div>}

        <div className={styles.footer}>
          <button className={styles.btnGhost} onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button
            className={styles.btnDanger}
            onClick={() => void handleConfirm()}
            disabled={busy}
            data-testid="files-delete-confirm"
          >
            {busy ? 'Deleting…' : `Delete ${count === 1 ? '1 item' : `${count} items`}`}
          </button>
        </div>
      </div>
    </Modal>
  )
}
