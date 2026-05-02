// GitStatus — sectioned (Conflicts / Staged / Unstaged / Untracked) view
// over `GET /git/status`, plus S012 enhancements:
//
//   - Subscribes to the global event stream (`/api/events`) and refreshes
//     when a `git.statusChanged` event arrives for this branch. Source of
//     truth: the per-branch worktreewatch subscription on the server,
//     debounced 1s.
//
//   - Touch swipe handlers on each row (mobile only): left swipe → stage,
//     right swipe → discard. Desktop pointers are ignored.
//
//   - Magit-style single-key bindings while focus is *inside* the status
//     view but *not* in any text input: `s` stage, `u` unstage, `c`
//     focus-commit, `d` discard, `p` push, `f` fetch.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { useGitStatusEvents } from '../../hooks/use-git-status-events'
import { api } from '../../lib/api'
import { bindToTabType, useTabKeybindings } from '../../lib/keybindings'

import styles from './git-status.module.css'
import type { FileStatus, StatusReport } from './types'

interface Props {
  apiBase: string
  repoId: string
  branchId: string
  onJumpToDiff: (path: string) => void
  onReport: (rep: StatusReport | null) => void
  /** Called when the user presses Magit `c` so the parent can scroll
   *  / focus the commit message textarea. */
  onMagitCommit: () => void
  /** Called when the user presses Magit `p` (push) and `f` (fetch) so
   *  the parent's GitSync component runs the operation. */
  onMagitPush: () => void
  onMagitFetch: () => void
}

const isMobileViewport = () =>
  typeof window !== 'undefined' && window.matchMedia('(max-width: 899px)').matches

export function GitStatus({
  apiBase,
  repoId,
  branchId,
  onJumpToDiff,
  onReport,
  onMagitCommit,
  onMagitPush,
  onMagitFetch,
}: Props) {
  const [rep, setRep] = useState<StatusReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)

  const reload = useCallback(async () => {
    try {
      const r = await api.get<StatusReport>(`${apiBase}/status`)
      setRep(r)
      onReport(r)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase, onReport])

  useEffect(() => {
    void reload()
  }, [reload])

  // S012: auto-refresh on git.statusChanged events for this branch.
  useGitStatusEvents(repoId, branchId, () => {
    void reload()
  })

  const act = useCallback(
    async (path: string, op: 'stage' | 'unstage' | 'discard') => {
      setPending(`${op}:${path}`)
      try {
        await api.post(`${apiBase}/${op}`, { path })
        await reload()
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setPending(null)
      }
    },
    [apiBase, reload],
  )

  // Magit-style keyboard shortcuts via the shared focus-aware
  // keybinding helper (S020). Only fires when focus is inside `containerRef`
  // and the active element is not an input. When the user switches to a
  // Bash / Claude tab the GitStatus component unmounts; the hook's
  // effect cleanup detaches the listener so `s` / `u` / `c` flow into
  // xterm as ordinary shell input.
  const bindings = useMemo(
    () =>
      bindToTabType('git', {
        s: (e) => {
          if (!selectedPath) return
          e.preventDefault()
          void act(selectedPath, 'stage')
        },
        u: (e) => {
          if (!selectedPath) return
          e.preventDefault()
          void act(selectedPath, 'unstage')
        },
        d: (e) => {
          if (!selectedPath) return
          e.preventDefault()
          const path = selectedPath
          void (async () => {
            const ok = await confirmDialog.ask({
              title: 'Discard changes?',
              message: `Discard changes to ${path}? This cannot be undone.`,
              confirmLabel: 'Discard',
              danger: true,
            })
            if (ok) await act(path, 'discard')
          })()
        },
        c: (e) => {
          e.preventDefault()
          onMagitCommit()
        },
        p: (e) => {
          e.preventDefault()
          onMagitPush()
        },
        f: (e) => {
          e.preventDefault()
          onMagitFetch()
        },
      }),
    [act, onMagitCommit, onMagitFetch, onMagitPush, selectedPath],
  )
  useTabKeybindings(containerRef, bindings)

  if (error) return <p className={styles.error}>{error}</p>
  if (!rep) return <p className={styles.empty}>Loading…</p>

  const empty =
    !rep.staged?.length && !rep.unstaged?.length && !rep.untracked?.length && !rep.conflicts?.length
  if (empty) return <p className={styles.clean}>✔ Working tree clean</p>

  const renderRow = (
    f: FileStatus,
    kind: 'staged' | 'unstaged' | 'untracked' | 'conflict',
  ) => (
    <Row
      key={`${kind}:${f.path}`}
      f={f}
      pending={pending}
      kind={kind}
      selected={selectedPath === f.path}
      onSelect={() => setSelectedPath(f.path)}
      onAct={act}
      onJump={() => onJumpToDiff(f.path)}
    />
  )

  return (
    <div className={styles.wrap} ref={containerRef} tabIndex={-1} data-testid="git-status">
      {rep.conflicts && rep.conflicts.length > 0 && (
        <Section title={`Conflicts (${rep.conflicts.length})`}>
          {rep.conflicts.map((f) => renderRow(f, 'conflict'))}
        </Section>
      )}
      {rep.staged && rep.staged.length > 0 && (
        <Section title={`Staged (${rep.staged.length})`}>
          {rep.staged.map((f) => renderRow(f, 'staged'))}
        </Section>
      )}
      {rep.unstaged && rep.unstaged.length > 0 && (
        <Section title={`Unstaged (${rep.unstaged.length})`}>
          {rep.unstaged.map((f) => renderRow(f, 'unstaged'))}
        </Section>
      )}
      {rep.untracked && rep.untracked.length > 0 && (
        <Section title={`Untracked (${rep.untracked.length})`}>
          {rep.untracked.map((f) => renderRow(f, 'untracked'))}
        </Section>
      )}
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{title}</h3>
      <ul className={styles.list}>{children}</ul>
    </section>
  )
}

interface RowProps {
  f: FileStatus
  pending: string | null
  kind: 'staged' | 'unstaged' | 'untracked' | 'conflict'
  selected: boolean
  onSelect: () => void
  onAct: (path: string, op: 'stage' | 'unstage' | 'discard') => void
  onJump: () => void
}

function Row({ f, pending, kind, selected, onSelect, onAct, onJump }: RowProps) {
  const code = (f.stagedCode + f.workingCode).trim()

  // S012: mobile swipe. Touch start records X; touch end measures the
  // delta. Threshold: 60px so accidental drags don't trigger an op.
  // Only active on mobile viewports — pointer-only desktops use the
  // explicit Stage / × buttons.
  const touchStartX = useRef<number | null>(null)
  const onTouchStart = (e: React.TouchEvent) => {
    if (!isMobileViewport()) return
    if (e.touches.length !== 1) return
    touchStartX.current = e.touches[0].clientX
  }
  const onTouchEnd = (e: React.TouchEvent) => {
    if (!isMobileViewport()) return
    if (touchStartX.current == null) return
    const startX = touchStartX.current
    touchStartX.current = null
    const endX = e.changedTouches[0]?.clientX ?? startX
    const dx = endX - startX
    if (Math.abs(dx) < 60) return
    if (dx < 0) {
      // Left swipe → stage (only valid for unstaged / untracked / conflict)
      if (kind !== 'staged') onAct(f.path, 'stage')
    } else {
      // Right swipe → discard (unstaged / untracked only)
      if (kind === 'unstaged' || kind === 'untracked') {
        void (async () => {
          const ok = await confirmDialog.ask({
            title: 'Discard changes?',
            message: `Discard changes to ${f.path}? This cannot be undone.`,
            confirmLabel: 'Discard',
            danger: true,
          })
          if (ok) onAct(f.path, 'discard')
        })()
      }
    }
  }

  return (
    <li
      className={`${styles.row} ${selected ? styles.rowSelected : ''}`}
      onTouchStart={onTouchStart}
      onTouchEnd={onTouchEnd}
      onClick={onSelect}
      data-testid={`git-status-row-${f.path}`}
      data-kind={kind}
    >
      <button
        className={styles.path}
        onClick={(e) => {
          e.stopPropagation()
          onSelect()
          onJump()
        }}
        title="Jump to diff"
      >
        <span className={styles.code}>{code}</span>
        <span className={styles.name}>{f.path}</span>
      </button>
      <div className={styles.actions}>
        {(kind === 'unstaged' || kind === 'untracked') && (
          <button
            className={styles.btn}
            disabled={pending === `stage:${f.path}`}
            onClick={(e) => {
              e.stopPropagation()
              onAct(f.path, 'stage')
            }}
          >
            Stage
          </button>
        )}
        {kind === 'staged' && (
          <button
            className={styles.btn}
            disabled={pending === `unstage:${f.path}`}
            onClick={(e) => {
              e.stopPropagation()
              onAct(f.path, 'unstage')
            }}
          >
            Unstage
          </button>
        )}
        {(kind === 'unstaged' || kind === 'untracked') && (
          <button
            className={`${styles.btn} ${styles.danger}`}
            disabled={pending === `discard:${f.path}`}
            onClick={async (e) => {
              e.stopPropagation()
              const ok = await confirmDialog.ask({
                title: 'Discard changes?',
                message: `Discard changes to ${f.path}? This cannot be undone.`,
                confirmLabel: 'Discard',
                danger: true,
              })
              if (ok) onAct(f.path, 'discard')
            }}
          >
            ×
          </button>
        )}
      </div>
    </li>
  )
}
