// repo-delete-modal.tsx — S030: Repository delete with unpushed-work warning.
//
// Flow:
//   1. Mount: call GET /api/repos/{repoId}/delete-preview
//   2. Preview loaded:
//      a. hasUnpushed=false → show 1-step confirm (prototype/delete-modal-clean.html)
//      b. hasUnpushed=true  → show worktree breakdown + type-the-name confirm
//                             (prototype/delete-modal-warning.html)
//   3. Confirm → DELETE /api/repos/{repoId} with optional confirmName
//   4. Success → call onDeleted (caller removes repo from UI)

import { useEffect, useState } from 'react'

import { api } from '../lib/api'

import styles from './repo-delete-modal.module.css'

interface WorktreeStatus {
  path: string
  branch: string
  aheadCommits: string[]
  upstreamMissing: boolean
  dirtyFiles: string[]
  untrackedFiles: string[]
  isPrimary: boolean
}

interface DeletePreview {
  hasUnpushed: boolean
  worktrees: WorktreeStatus[]
}

interface Props {
  open: boolean
  repoId: string
  repoName: string  // owner/repo display name
  ghqPath: string   // full ghqPath for short-name derivation
  onClose: () => void
  onDeleted: () => void
}

function repoShortName(ghqPath: string): string {
  const parts = ghqPath.split('/')
  if (parts.length >= 2) return parts.slice(-2).join('/')
  return ghqPath
}

function wtBadge(wt: WorktreeStatus): string {
  const ahead = wt.aheadCommits ?? []
  const dirty = wt.dirtyFiles ?? []
  const untracked = wt.untrackedFiles ?? []
  const parts: string[] = []
  if (ahead.length > 0) parts.push(`${ahead.length} ahead`)
  if (wt.upstreamMissing) parts.push('no upstream')
  if (dirty.length > 0) parts.push('dirty')
  if (untracked.length > 0) parts.push(`${untracked.length} untracked`)
  if (parts.length === 0) return 'clean'
  return parts.join(' · ')
}

function wtHasWarnings(wt: WorktreeStatus): boolean {
  return (wt.aheadCommits ?? []).length > 0 || wt.upstreamMissing || (wt.dirtyFiles ?? []).length > 0 || (wt.untrackedFiles ?? []).length > 0
}

function filePrefix(line: string): { prefix: string; file: string } {
  // lines like "M src/foo.ts" or "D bar.ts" from git status --porcelain
  const space = line.indexOf(' ')
  if (space === -1) return { prefix: '??', file: line }
  return { prefix: line.slice(0, space).trim(), file: line.slice(space + 1).trim() }
}

function FilePrefixSpan({ prefix }: { prefix: string }) {
  let cls = styles.fileOther
  if (prefix === 'M') cls = styles.fileMod
  else if (prefix === 'D') cls = styles.fileDel
  else if (prefix === 'A') cls = styles.fileAdd
  else if (prefix === '??') cls = styles.fileUntracked
  return <span className={cls}>{prefix}</span>
}

export function RepoDeleteModal({ open, repoId, repoName: _repoName, ghqPath, onClose, onDeleted }: Props) {
  const [preview, setPreview] = useState<DeletePreview | null>(null)
  const [loading, setLoading] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [confirmName, setConfirmName] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const expectedName = repoShortName(ghqPath)

  useEffect(() => {
    if (!open) return
    setPreview(null)
    setPreviewError(null)
    setConfirmName('')
    setDeleteError(null)

    const load = async () => {
      setLoading(true)
      try {
        const data = await api.get<DeletePreview>(
          `/api/repos/${encodeURIComponent(repoId)}/delete-preview`,
        )
        setPreview(data)
      } catch (err) {
        setPreviewError(err instanceof Error ? err.message : String(err))
      } finally {
        setLoading(false)
      }
    }
    void load()
  }, [open, repoId])

  const handleDelete = async () => {
    setDeleting(true)
    setDeleteError(null)
    try {
      const body: Record<string, string> = {}
      if (preview?.hasUnpushed) body.confirmName = confirmName
      await api.delete(`/api/repos/${encodeURIComponent(repoId)}`)
      onDeleted()
      onClose()
    } catch (err) {
      setDeleteError(err instanceof Error ? err.message : String(err))
    } finally {
      setDeleting(false)
    }
  }

  const canConfirm = !preview?.hasUnpushed || confirmName === expectedName

  if (!open) return null

  return (
    <div className={styles.overlay} onClick={onClose} data-testid="delete-modal-overlay">
      <div
        className={`${styles.card} ${preview?.hasUnpushed ? styles.cardWide : ''}`}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-modal-title"
        data-testid="delete-modal"
      >
        {/* Header */}
        <div className={styles.header}>
          <h2 className={styles.title} id="delete-modal-title">
            Delete <code>{expectedName}</code>?
          </h2>
          <p className={styles.sub}>
            {preview?.hasUnpushed
              ? <>
                  {(preview.worktrees ?? []).length} worktree{(preview.worktrees ?? []).length !== 1 ? 's' : ''}, ghq directory, and tmux sessions.{' '}
                  <span className={styles.warnText}>Unpushed work detected — review below.</span>
                </>
              : 'All worktrees, the ghq directory, and any tmux sessions for this repo will be removed.'}
          </p>
        </div>

        {/* Body */}
        <div className={styles.body}>
          {loading && (
            <div className={styles.loadingRow}>
              <span className={styles.spinner} aria-hidden="true" />
              <span>Checking for unpushed work…</span>
            </div>
          )}

          {previewError && (
            <div className={styles.warnBanner}>
              <span className={styles.warnIcon}>⚠</span>
              <div>Could not check unpushed work: {previewError}</div>
            </div>
          )}

          {preview && !loading && (
            <>
              {preview.hasUnpushed ? (
                <div className={styles.warnBanner}>
                  <span className={styles.warnIcon}>⚠</span>
                  <div>
                    <strong>Unpushed work in {(preview.worktrees ?? []).filter(wtHasWarnings).length} of {(preview.worktrees ?? []).length} worktree{(preview.worktrees ?? []).length !== 1 ? 's' : ''}.</strong>{' '}
                    Pushing or stashing first is recommended. Anything listed here will be permanently lost when you confirm.
                  </div>
                </div>
              ) : (
                <div className={styles.cleanBanner}>
                  <span className={styles.cleanIcon}>✓</span>
                  <div>
                    <strong className={styles.cleanText}>Nothing unpushed.</strong><br />
                    All {(preview.worktrees ?? []).length} worktree{(preview.worktrees ?? []).length !== 1 ? 's are' : ' is'} clean and up to date with their upstream.
                  </div>
                </div>
              )}

              <div className={styles.worktrees}>
                {(preview.worktrees ?? []).map((wt, i) => {
                  const hasWarnings = wtHasWarnings(wt)
                  const badge = wtBadge(wt)
                  return (
                    <div className={styles.wtBlock} key={i}>
                      <div className={styles.wtHeader}>
                        <span className={styles.wtPath}>{wt.path}</span>
                        <span className={styles.wtBranch}>· {wt.branch}</span>
                        <span className={`${styles.wtBadge} ${!hasWarnings ? styles.wtBadgeClean : ''}`}>
                          {badge}
                        </span>
                      </div>
                      {hasWarnings && (
                        <div className={styles.wtBody}>
                          {(wt.aheadCommits ?? []).length > 0 && (
                            <>
                              <div className={styles.wtCategory}>
                                {wt.upstreamMissing
                                  ? 'Branch has never been pushed'
                                  : `${(wt.aheadCommits ?? []).length} commits ahead of upstream`}
                              </div>
                              <ul className={styles.wtList}>
                                {(wt.aheadCommits ?? []).map((c, j) => {
                                  const space = c.indexOf(' ')
                                  const hash = space === -1 ? c : c.slice(0, space)
                                  const msg = space === -1 ? '' : c.slice(space + 1)
                                  return (
                                    <li key={j}>
                                      <span className={styles.commitHash}>{hash}</span>{msg}
                                    </li>
                                  )
                                })}
                              </ul>
                            </>
                          )}
                          {(wt.dirtyFiles ?? []).length > 0 && (
                            <>
                              <div className={styles.wtCategory}>{(wt.dirtyFiles ?? []).length} modified/deleted</div>
                              <ul className={styles.wtList}>
                                {(wt.dirtyFiles ?? []).map((f, j) => {
                                  const { prefix, file } = filePrefix(f)
                                  return (
                                    <li key={j}><FilePrefixSpan prefix={prefix} /> {file}</li>
                                  )
                                })}
                              </ul>
                            </>
                          )}
                          {(wt.untrackedFiles ?? []).length > 0 && (
                            <>
                              <div className={styles.wtCategory}>{(wt.untrackedFiles ?? []).length} untracked</div>
                              <ul className={styles.wtList}>
                                {(wt.untrackedFiles ?? []).map((f, j) => (
                                  <li key={j}><span className={styles.fileUntracked}>??</span> {f}</li>
                                ))}
                              </ul>
                            </>
                          )}
                        </div>
                      )}
                      {!hasWarnings && (
                        <div className={styles.wtBodyClean}>
                          up to date with upstream, working tree clean
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>

              {preview.hasUnpushed && (
                <div className={styles.typeConfirm}>
                  <div className={styles.typeLabel}>Type the repository name to confirm</div>
                  <input
                    className={styles.typeInput}
                    type="text"
                    placeholder={expectedName}
                    value={confirmName}
                    data-testid="delete-confirm-input"
                    onChange={(e) => setConfirmName(e.target.value)}
                    autoFocus
                  />
                  <div className={styles.typeHelp}>
                    Required because unpushed work exists in {(preview.worktrees ?? []).filter(wtHasWarnings).length} worktree{(preview.worktrees ?? []).filter(wtHasWarnings).length !== 1 ? 's' : ''}.
                  </div>
                </div>
              )}

              {deleteError && (
                <p className={styles.deleteError}>{deleteError}</p>
              )}
            </>
          )}
        </div>

        {/* Footer */}
        <div className={styles.footer}>
          <button className={styles.btnGhost} onClick={onClose}>Cancel</button>
          <button
            className={styles.btnDanger}
            disabled={loading || !preview || deleting || !canConfirm}
            data-testid="delete-confirm"
            onClick={() => void handleDelete()}
          >
            {deleting ? 'Deleting…' : preview?.hasUnpushed ? 'Delete anyway' : 'Delete repository'}
          </button>
        </div>
      </div>
    </div>
  )
}
