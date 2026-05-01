// ConflictDialog (S011-1-9 / S011-2-5)
//
// Surfaces 412-Precondition-Failed responses from `PUT /files/raw`. The
// flow is:
//
//   1. User saves; server reports a different ETag than our `If-Match`.
//   2. We capture {serverEtag, localContent} into the editor store.
//   3. This dialog mounts whenever the active file has a `conflict`
//      slice, offering three exits:
//        - Reload   → fetch the latest server content, drop our draft
//        - Overwrite → re-PUT with `If-Match: serverEtag`
//        - Cancel   → close the dialog, keep the local draft (user
//                      can decide later)
//
// We deliberately don't render a 3-way diff yet — that lands in the
// Backlog as a follow-up. For S011 the priority is "no silent data loss
// even if two clients race", which the three buttons satisfy.

import styles from './conflict-dialog.module.css'

interface Props {
  path: string
  open: boolean
  saving?: boolean
  onReload: () => void
  onOverwrite: () => void
  onCancel: () => void
}

export function ConflictDialog({ path, open, saving, onReload, onOverwrite, onCancel }: Props) {
  if (!open) return null
  return (
    <div className={styles.backdrop} role="dialog" aria-modal="true" data-testid="conflict-dialog">
      <div className={styles.panel}>
        <h2 className={styles.title}>File changed on disk</h2>
        <p className={styles.body}>
          <code>{path}</code> was modified outside of palmux2 since you opened it. Saving now
          would overwrite those changes. Choose how to proceed:
        </p>
        <ul className={styles.options}>
          <li>
            <strong>Reload</strong> — discard your unsaved edits and load the latest content from
            disk.
          </li>
          <li>
            <strong>Overwrite</strong> — keep your edits and clobber the on-disk file. The other
            writer&apos;s changes will be lost.
          </li>
          <li>
            <strong>Cancel</strong> — close this dialog without saving. You can decide later.
          </li>
        </ul>
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.cancel}
            onClick={onCancel}
            disabled={saving}
            data-testid="conflict-cancel"
          >
            Cancel
          </button>
          <button
            type="button"
            className={styles.reload}
            onClick={onReload}
            disabled={saving}
            data-testid="conflict-reload"
          >
            Reload from disk
          </button>
          <button
            type="button"
            className={styles.overwrite}
            onClick={onOverwrite}
            disabled={saving}
            data-testid="conflict-overwrite"
          >
            {saving ? 'Saving…' : 'Overwrite'}
          </button>
        </div>
      </div>
    </div>
  )
}
