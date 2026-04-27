// DiffView is the pure-rendering counterpart to GitDiff. It accepts already-
// fetched DiffFile[] and an optional per-hunk action handler so consumers
// can wire stage/unstage/discard buttons (or none at all, in which case
// the hunks render read-only).
//
// The Claude tab uses it for Edit/Write tool result previews; the Git tab
// uses it as the rendering primitive behind its diff sub-view.

import type { DiffFile, DiffHunk } from './types'
import styles from './diff-view.module.css'

export type HunkAction = 'stage-hunk' | 'unstage-hunk' | 'discard-hunk'

export interface HunkActionButton {
  op: HunkAction
  label: string
  danger?: boolean
}

interface FileHeaderAction {
  label: string
  onClick: () => void
}

export interface DiffViewProps {
  files: DiffFile[]
  /** Per-hunk action buttons. Empty or omitted = read-only. */
  hunkActions?: HunkActionButton[]
  /** Disabled while a previous action is in flight. */
  pending?: string | null
  /**
   * Hunk-action keys collide on `${op}:${file.newPath}:${hunk.header}`.
   * Caller passes `pending` matching that template to disable the
   * particular button + show progress.
   */
  onHunkAction?: (file: DiffFile, hunk: DiffHunk, op: HunkAction) => void
  /** Optional per-file header actions (e.g. "Open in Files"). */
  fileActions?: (file: DiffFile) => FileHeaderAction[]
}

export function DiffView({ files, hunkActions, pending, onHunkAction, fileActions }: DiffViewProps) {
  if (!files || files.length === 0) return null
  return (
    <div>
      {files.map((file, i) => (
        <FileBlock
          key={file.newPath || file.oldPath || i}
          file={file}
          hunkActions={hunkActions}
          pending={pending}
          onHunkAction={onHunkAction}
          fileActions={fileActions}
        />
      ))}
    </div>
  )
}

function FileBlock({
  file,
  hunkActions,
  pending,
  onHunkAction,
  fileActions,
}: {
  file: DiffFile
  hunkActions?: HunkActionButton[]
  pending?: string | null
  onHunkAction?: (file: DiffFile, hunk: DiffHunk, op: HunkAction) => void
  fileActions?: (file: DiffFile) => FileHeaderAction[]
}) {
  const headerActions = fileActions ? fileActions(file) : []
  return (
    <section className={styles.file}>
      <header className={styles.fileHeader}>
        <span>{file.newPath || file.oldPath}</span>
        <span className={styles.fileHeaderActions}>
          {file.isBinary && <span className={styles.binTag}>binary</span>}
          {headerActions.map((a, i) => (
            <button key={i} type="button" className={styles.btn} onClick={a.onClick}>
              {a.label}
            </button>
          ))}
        </span>
      </header>
      {!file.isBinary &&
        file.hunks?.map((h, hi) => (
          <div key={hi} className={styles.hunk}>
            <div className={styles.hunkHeader}>
              <span>{h.header}</span>
              {hunkActions && hunkActions.length > 0 && (
                <div className={styles.hunkActions}>
                  {hunkActions.map((a) => {
                    const key = `${a.op}:${file.newPath}:${h.header}`
                    return (
                      <button
                        key={a.op}
                        type="button"
                        className={`${styles.btn} ${a.danger ? styles.danger : ''}`.trim()}
                        disabled={!!pending}
                        onClick={() => onHunkAction?.(file, h, a.op)}
                      >
                        {pending === key ? '…' : a.label}
                      </button>
                    )
                  })}
                </div>
              )}
            </div>
            <pre className={styles.lines}>
              {h.lines.map((ln, li) => (
                <span
                  key={li}
                  className={
                    ln.kind === 'add'
                      ? styles.add
                      : ln.kind === 'del'
                        ? styles.del
                        : ln.kind === 'meta'
                          ? styles.meta
                          : styles.context
                  }
                >
                  {ln.kind === 'add' ? '+' : ln.kind === 'del' ? '-' : ' '}
                  {ln.text}
                  {'\n'}
                </span>
              ))}
            </pre>
          </div>
        ))}
    </section>
  )
}

// buildSyntheticDiff converts an Edit (oldString → newString) or Write
// (full file content) tool input into a single-file DiffFile suitable for
// DiffView. It does NOT do real LCS diff — it just emits del lines for the
// old block and add lines for the new block, suitable for Claude's
// straight-line replacements.
export function buildSyntheticDiff(
  filePath: string,
  oldText: string,
  newText: string,
): DiffFile {
  const oldLines = oldText === '' ? [] : oldText.replace(/\n$/, '').split('\n')
  const newLines = newText === '' ? [] : newText.replace(/\n$/, '').split('\n')
  const hunk: DiffHunk = {
    header: `@@ -1,${oldLines.length || 1} +1,${newLines.length || 1} @@`,
    oldStart: 1,
    oldCount: oldLines.length || 1,
    newStart: 1,
    newCount: newLines.length || 1,
    lines: [
      ...oldLines.map((l) => ({ kind: 'del' as const, text: l })),
      ...newLines.map((l) => ({ kind: 'add' as const, text: l })),
    ],
  }
  return {
    oldPath: filePath,
    newPath: filePath,
    header: filePath,
    hunks: [hunk],
  }
}
