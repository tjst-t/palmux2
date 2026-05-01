// GitCommit — message editor + amend / signoff / no-verify checkboxes +
// Commit button + AI commit message button (S012-1-12, S012-1-13).
//
// AI commit message integration: the button is enabled only when a Claude
// tab is mounted on the current branch (any tab.type === "claude"). When
// clicked it POSTs to /git/ai-commit-message to fetch the staged diff +
// branch context, writes the prompt to the Claude composer's localStorage
// draft key (`palmux:claude-draft:{repoId}/{branchId}`), dispatches a
// custom DOM event so a mounted composer hot-reloads its value, and
// finally navigates the user to the Claude tab so they can review &
// send.

import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { api } from '../../lib/api'
import { usePalmuxStore } from '../../stores/palmux-store'

import styles from './git-commit.module.css'
import type { CommitOptions, CommitResult } from './types'

interface Props {
  apiBase: string
  repoId: string
  branchId: string
  /** Number of staged paths — disables the Commit button when zero. */
  stagedCount: number
  onCommitted: () => void
}

// Pulled out so the hot-reload custom event uses a stable key.
export const COMPOSER_PREFILL_EVENT = 'palmux:composer-prefill'

export function GitCommit({ apiBase, repoId, branchId, stagedCount, onCommitted }: Props) {
  const [message, setMessage] = useState('')
  const [amend, setAmend] = useState(false)
  const [signoff, setSignoff] = useState(false)
  const [noVerify, setNoVerify] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<CommitResult | null>(null)
  const ref = useRef<HTMLTextAreaElement | null>(null)
  const navigate = useNavigate()

  // Prefill the textarea with the previous message when the user toggles
  // amend on (and only the first time per toggle so they don't lose their
  // edits).
  const lastAmendState = useRef(false)
  useEffect(() => {
    if (amend && !lastAmendState.current) {
      ;(async () => {
        try {
          const res = await api.get<{ message: string }>(`${apiBase}/head-message`)
          if (!message.trim()) {
            setMessage(res.message ?? '')
          }
        } catch {
          // best effort
        }
      })()
    }
    lastAmendState.current = amend
  }, [amend, apiBase, message])

  // S012: Claude tab presence detection. We read the branch out of the
  // store and check whether any tab on it has type === "claude".
  const claudeTabId = usePalmuxStore((s) => {
    const repo = s.repos.find((r) => r.id === repoId)
    const branch = repo?.openBranches.find((b) => b.id === branchId)
    const claude = branch?.tabSet.tabs.find((t) => t.type === 'claude')
    return claude?.id ?? null
  })

  const aiButtonDisabled = !claudeTabId || busy

  const onAICommit = async () => {
    if (!claudeTabId) return
    setBusy(true)
    setError(null)
    try {
      const res = await api.post<{ prompt: string }>(`${apiBase}/ai-commit-message`, {})
      const prompt = res.prompt
      const draftKey = `palmux:claude-draft:${repoId}/${branchId}`
      try {
        localStorage.setItem(draftKey, prompt)
      } catch {
        /* quota exceeded — still emit the event with payload */
      }
      // Wake any mounted composer.
      window.dispatchEvent(
        new CustomEvent(COMPOSER_PREFILL_EVENT, {
          detail: { repoId, branchId, prompt },
        }),
      )
      // Navigate to the Claude tab.
      navigate(`/${repoId}/${branchId}/${claudeTabId}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const onCommit = async () => {
    if (!message.trim() && !amend) {
      setError('Commit message required.')
      return
    }
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const opts: CommitOptions = {
        message,
        amend,
        signoff,
        noVerify,
      }
      const res = await api.post<CommitResult>(`${apiBase}/commit`, opts)
      setResult(res)
      setMessage('')
      setAmend(false)
      onCommitted()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.commit}>
      <div className={styles.header}>
        <span className={styles.label}>Commit message</span>
        <span className={styles.staged}>
          {stagedCount} file{stagedCount === 1 ? '' : 's'} staged
        </span>
      </div>
      <textarea
        ref={ref}
        className={styles.textarea}
        value={message}
        placeholder={amend ? '(amend) — leave blank to keep previous message' : 'subject\n\nbody'}
        onChange={(e) => setMessage(e.target.value)}
        rows={6}
        data-testid="git-commit-message"
      />
      <div className={styles.options}>
        <label className={styles.opt}>
          <input
            type="checkbox"
            checked={amend}
            onChange={(e) => setAmend(e.target.checked)}
            data-testid="git-commit-amend"
          />
          amend
        </label>
        <label className={styles.opt}>
          <input
            type="checkbox"
            checked={signoff}
            onChange={(e) => setSignoff(e.target.checked)}
            data-testid="git-commit-signoff"
          />
          signoff (-s)
        </label>
        <label className={styles.opt}>
          <input
            type="checkbox"
            checked={noVerify}
            onChange={(e) => setNoVerify(e.target.checked)}
            data-testid="git-commit-no-verify"
          />
          no-verify
        </label>
      </div>
      {error && <p className={styles.error}>{error}</p>}
      {result && (
        <p className={styles.success}>
          Committed {result.hash.slice(0, 7)} {result.subject}
        </p>
      )}
      <div className={styles.actions}>
        <button
          className={styles.aiBtn}
          disabled={aiButtonDisabled}
          onClick={onAICommit}
          title={
            claudeTabId
              ? 'Send staged diff to the Claude tab composer'
              : 'Open a Claude tab to use AI commit message'
          }
          data-testid="git-commit-ai-btn"
        >
          ✨ AI commit message
        </button>
        <button
          className={styles.commitBtn}
          disabled={busy || (stagedCount === 0 && !amend)}
          onClick={onCommit}
          data-testid="git-commit-btn"
        >
          {amend ? 'Amend' : 'Commit'}
        </button>
      </div>
    </div>
  )
}
