import styles from './file-list.module.css'
import type { Entry } from './types'

interface Props {
  entries: Entry[]
  selected?: string
  onPick: (entry: Entry) => void
  /** S011-1-6: worktree-relative paths that have unsaved buffers in
   *  the editor store. We render a `●` badge next to the filename so
   *  the user sees their unsaved drafts at a glance. */
  dirtyPaths?: string[]
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n}`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}K`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}M`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)}G`
}

function fmtDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(+d)) return ''
  return d.toLocaleDateString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit' })
}

export function FileList({ entries, selected, onPick, dirtyPaths }: Props) {
  if (entries.length === 0) {
    return <p className={styles.empty}>(empty directory)</p>
  }
  const dirtySet = new Set(dirtyPaths ?? [])
  return (
    <ul className={styles.list}>
      {entries.map((e) => {
        const dirty = !e.isDir && dirtySet.has(e.path)
        return (
          <li key={e.path}>
            <button
              className={selected === e.path ? `${styles.row} ${styles.active}` : styles.row}
              onClick={() => onPick(e)}
              title={e.path}
              data-dirty={dirty ? 'true' : undefined}
            >
              <span className={styles.icon}>{e.isDir ? '📁' : iconFor(e.name)}</span>
              <span className={styles.name}>
                {e.name}
                {dirty && (
                  <span className={styles.dirtyDot} data-testid="file-dirty-dot" title="Unsaved changes">
                    {' '}
                    ●
                  </span>
                )}
              </span>
              <span className={styles.meta}>{e.isDir ? '' : fmtSize(e.size)}</span>
              <span className={styles.meta}>{fmtDate(e.modTime)}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}

function iconFor(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() ?? ''
  switch (ext) {
    case 'md':
    case 'markdown':
      return '📝'
    case 'png':
    case 'jpg':
    case 'jpeg':
    case 'gif':
    case 'svg':
    case 'webp':
      return '🖼'
    case 'go':
      return '🐹'
    case 'ts':
    case 'tsx':
    case 'js':
    case 'jsx':
      return '🟨'
    case 'json':
      return '📋'
    case 'css':
      return '🎨'
    default:
      return '📄'
  }
}
