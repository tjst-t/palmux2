// File history view (S013-1-15).
//
// A path-scoped log. Reachable from:
//   - the Files tab "Show history" action (which navigates to the Git
//     tab with `?fileHistory=<path>`)
//   - direct deep-link
//
// Renders a thin header (the path, with "back" button) plus the rich
// log filtered to commits that touched that path. Re-uses GitLog's
// internal infinite scroll & filter UI by passing `path`.

import { GitLog } from './git-log'
import styles from './git-file-history.module.css'
import type { LogEntryDetail } from './types'

interface Props {
  apiBase: string
  path: string
  onClose: () => void
  onPickCommit?: (entry: LogEntryDetail) => void
}

export function GitFileHistory({ apiBase, path, onClose, onPickCommit }: Props) {
  return (
    <div className={styles.wrap} data-testid="git-file-history">
      <header className={styles.header}>
        <button className={styles.backBtn} onClick={onClose} data-testid="file-history-back">
          ← Log
        </button>
        <span className={styles.pathLabel}>History of:</span>
        <code className={styles.path}>{path}</code>
      </header>
      <div className={styles.body}>
        <GitLog apiBase={apiBase} path={path} onSelect={onPickCommit} />
      </div>
    </div>
  )
}
