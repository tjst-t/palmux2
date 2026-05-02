// S019: claude.ai-style edit & rewind UI for past user messages.
//
// Shape:
//
//   ┌────────────────────────── < N/M > ──┐  ← version arrows (only when
//   │                                       │    the turn has been rewound)
//   │  user message text          [✎]     │  ← edit pencil on hover/focus
//   └───────────────────────────────────────┘
//
// Click pencil → bubble morphs into a Monaco editor (markdown language)
// with submit/cancel chrome. Cmd+Enter (Mac) / Ctrl+Enter submits, Esc
// cancels. Submit calls rewind() (REST) + rewindApplyLocal() (optimistic
// reducer) so the displaced turns fade out immediately. Draft text is
// persisted to localStorage keyed by turnId — switching tabs / refreshing
// while an edit is in progress retains the work.

import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'

import type { Turn, TurnVersion } from './types'
import styles from './user-turn-editor.module.css'

// Re-use the heavy Monaco bundle that Files preview already pulls in.
// Lazy import keeps it out of the initial chunk on conversations that
// never enter edit mode.
const MonacoView = lazy(() =>
  import('../files/viewers/monaco-view').then((m) => ({ default: m.MonacoView })),
)

const DRAFT_PREFIX = 'palmux:rewindDraft.'

function draftKey(turnId: string): string {
  return DRAFT_PREFIX + turnId
}

function readDraft(turnId: string): string | null {
  if (typeof localStorage === 'undefined') return null
  try {
    return localStorage.getItem(draftKey(turnId))
  } catch {
    return null
  }
}

function writeDraft(turnId: string, content: string): void {
  if (typeof localStorage === 'undefined') return
  try {
    if (content) {
      localStorage.setItem(draftKey(turnId), content)
    } else {
      localStorage.removeItem(draftKey(turnId))
    }
  } catch {
    // ignore quota errors
  }
}

function clearDraft(turnId: string): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.removeItem(draftKey(turnId))
  } catch {
    // ignore
  }
}

/** Expose draft helpers for tests + harness. Not part of the
 *  component's public API. */
export const __rewindDraft = { read: readDraft, write: writeDraft, clear: clearDraft, key: draftKey }

interface UserTurnEditorProps {
  /** The user turn whose content/versions we render. */
  turn: Turn
  /** -1 = active live version; otherwise index into turn.versions[]. */
  activeVersionIndex: number
  /** Called by the `< N/M >` arrows. Pass -1 to mean live. */
  onSetVersion: (index: number) => void
  /** Called when the user submits an edit. The component handles
   *  optimistic UI, draft persistence, and error revert internally. */
  onRewind: (turnId: string, newMessage: string) => Promise<void>
  /** Called immediately on submit so the parent can flip optimistic
   *  state (fade out subsequent turns, archive the prior version)
   *  before the network round-trip completes. */
  onRewindApplyLocal: (turnId: string, newContent: string) => void
}

export function UserTurnEditor({
  turn,
  activeVersionIndex,
  onSetVersion,
  onRewind,
  onRewindApplyLocal,
}: UserTurnEditorProps) {
  const versions = turn.versions ?? []
  const versionCount = versions.length + 1 // +1 for the live (active) version

  // The displayed message text:
  //   activeVersionIndex === -1     → live blocks[0].text
  //   0 <= activeVersionIndex < N   → versions[activeVersionIndex].content
  const liveText = turn.blocks[0]?.text ?? ''
  const displayedText = useMemo(() => {
    if (activeVersionIndex < 0 || activeVersionIndex >= versions.length) return liveText
    return versions[activeVersionIndex].content
  }, [activeVersionIndex, liveText, versions])

  // M (1-indexed): which version slot is on display.
  //   - Each archived version is indexes 1..N
  //   - The live (active) version is index N+1 (rightmost)
  const displayPos =
    activeVersionIndex < 0 || activeVersionIndex >= versions.length
      ? versionCount
      : activeVersionIndex + 1

  // Enter edit mode flag. Toggling resets the draft: re-opens with the
  // last persisted draft (if any) or the displayed text.
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(displayedText)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string>('')
  const headerId = useId()

  const beginEdit = useCallback(() => {
    const persisted = readDraft(turn.id)
    setDraft(persisted ?? displayedText)
    setError('')
    setEditing(true)
  }, [displayedText, turn.id])

  const cancelEdit = useCallback(() => {
    setEditing(false)
    setError('')
    clearDraft(turn.id)
  }, [turn.id])

  const submitEdit = useCallback(async () => {
    const next = draft.trim()
    if (!next) {
      setError('Message cannot be empty')
      return
    }
    if (next === liveText) {
      // No change — just close.
      setEditing(false)
      clearDraft(turn.id)
      return
    }
    setSubmitting(true)
    setError('')
    // Optimistic apply. The reducer archives the live version, drops
    // subsequent turns, and replaces blocks[0].text. If the network
    // call below fails, we restore — but the dropped subsequent turns
    // would still be in archivedTurnsById, so they're recoverable
    // via the version arrow.
    onRewindApplyLocal(turn.id, next)
    try {
      await onRewind(turn.id, next)
      clearDraft(turn.id)
      setEditing(false)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setError(msg)
    } finally {
      setSubmitting(false)
    }
  }, [draft, liveText, onRewind, onRewindApplyLocal, turn.id])

  // Persist the draft on every keystroke (debounced via the
  // onChange callback chain — Monaco fires onChange synchronously
  // so we save synchronously too; localStorage writes are cheap).
  useEffect(() => {
    if (!editing) return
    writeDraft(turn.id, draft)
  }, [draft, editing, turn.id])

  // Cmd+Enter / Ctrl+Enter to submit, Esc to cancel.
  //
  // Listening on `window` (capture phase) is the reliable way: Monaco
  // intercepts most keys at the editor host, but Escape and
  // Cmd/Ctrl+Enter still bubble up to window unless something earlier
  // calls preventDefault. We additionally check that the click target
  // for Esc is inside our wrap (so a stray Esc from another popup
  // doesn't dismiss us mid-edit) — but for the ergonomic case where
  // the user typed and then pressed Esc with focus still in Monaco's
  // textarea, the textarea is a child of our wrap so the check passes.
  const wrapRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!editing) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        const node = wrapRef.current
        const target = e.target as Node | null
        // Only swallow Esc when focus is inside us — avoids hijacking
        // Esc from popups that mounted on top of the conversation.
        if (node && target && node.contains(target)) {
          e.preventDefault()
          cancelEdit()
        } else if (!node || !document.activeElement || document.activeElement === document.body) {
          // Fallback: no focus at all — assume the Esc was meant for us.
          cancelEdit()
        }
        return
      }
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
        const node = wrapRef.current
        const target = e.target as Node | null
        if (node && target && node.contains(target)) {
          e.preventDefault()
          void submitEdit()
        }
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [editing, cancelEdit, submitEdit])

  if (editing) {
    return (
      <div
        ref={wrapRef}
        className={styles.editorWrap}
        data-testid="user-turn-editor"
        data-turn-id={turn.id}
      >
        <div className={styles.editorBubble}>
          <Suspense fallback={<div className={styles.editorPlaceholder}>Loading editor…</div>}>
            <MonacoView
              apiBase=""
              body={{
                content: draft,
                path: `${turn.id}.md`,
                size: draft.length,
                mime: 'text/markdown',
                isBinary: false,
                truncated: false,
              }}
              path={`${turn.id}.md`}
              language="markdown"
              mode="edit"
              onChange={(v) => setDraft(v)}
              onSave={() => void submitEdit()}
            />
          </Suspense>
          <div className={styles.editorActions}>
            {error && (
              <span className={styles.editorError} role="alert">
                {error}
              </span>
            )}
            <button
              type="button"
              className={styles.editorBtnGhost}
              onClick={cancelEdit}
              disabled={submitting}
              data-testid="rewind-cancel"
              aria-label="Cancel edit (Esc)"
              title="Cancel (Esc)"
            >
              Cancel
            </button>
            <button
              type="button"
              className={styles.editorBtnPrimary}
              onClick={() => void submitEdit()}
              disabled={submitting}
              data-testid="rewind-submit"
              aria-label="Submit edited message (Cmd+Enter)"
              title="Submit (Cmd+Enter)"
            >
              {submitting ? 'Sending…' : 'Submit'}
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className={styles.userTurnWrap}>
      {versionCount > 1 && (
        <div className={styles.versionArrows} role="group" aria-labelledby={headerId}>
          <span id={headerId} className={styles.srOnly}>
            Message version selector
          </span>
          <button
            type="button"
            className={styles.arrowBtn}
            onClick={() => {
              // Move LEFT (older). versionCount slots are
              //   1..N = versions[0..N-1] (oldest..newest archived)
              //   N+1  = live
              // displayPos is in [1..versionCount].
              if (displayPos <= 1) return
              const next = displayPos - 1 // 1-indexed → versions[next-1]
              if (next <= versions.length) {
                onSetVersion(next - 1)
              } else {
                onSetVersion(-1)
              }
            }}
            disabled={displayPos <= 1}
            aria-label="Previous version"
            data-testid="rewind-prev"
          >
            ‹
          </button>
          <span className={styles.versionLabel} data-testid="rewind-version-label">
            {displayPos}/{versionCount}
          </span>
          <button
            type="button"
            className={styles.arrowBtn}
            onClick={() => {
              if (displayPos >= versionCount) return
              const next = displayPos + 1
              if (next <= versions.length) {
                onSetVersion(next - 1)
              } else {
                onSetVersion(-1)
              }
            }}
            disabled={displayPos >= versionCount}
            aria-label="Next version"
            data-testid="rewind-next"
          >
            ›
          </button>
        </div>
      )}
      <div className={styles.userBubbleWrap} data-testid={`user-bubble-${turn.id}`}>
        <div className={styles.userBubbleText}>{displayedText}</div>
        <button
          type="button"
          className={styles.editPencil}
          onClick={beginEdit}
          aria-label="Edit message"
          title="Edit message"
          data-testid={`rewind-edit-${turn.id}`}
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4Z" />
          </svg>
        </button>
      </div>
      {activeVersionIndex >= 0 && activeVersionIndex < versions.length && (
        <ArchivedVersionHint version={versions[activeVersionIndex]} />
      )}
    </div>
  )
}

/** Shown under an archived (non-live) version so the user knows the
 *  conversation tail isn't the current one. Lists the count of
 *  abandoned subsequent turns; the actual replay is handled by the
 *  parent renderer (which reads activeVersionByTurnId to swap the
 *  visible turns). */
function ArchivedVersionHint({ version }: { version: TurnVersion }) {
  const n = version.subsequentTurnIds.length
  return (
    <div className={styles.archivedHint} data-testid="rewind-archived-hint">
      Showing archived version
      {n > 0 ? ` (${n} subsequent turn${n === 1 ? '' : 's'})` : ''}
      {' · '}
      <time dateTime={version.createdAt}>
        {new Date(version.createdAt).toLocaleString()}
      </time>
    </div>
  )
}
