// Cherry-pick / Revert / Reset modals (S013-1-13).
//
// Each modal follows the same shape:
//   1. Preview the impact (which commit, which mode, what message will
//      be auto-generated).
//   2. Optional sub-options (no-commit toggle for cherry-pick / revert,
//      mode picker for reset).
//   3. A confirm button that POSTs to the relevant endpoint.
//
// Reset hard is a special case: it requires *two* confirmations, with a
// reflog-recovery hint between them. The destructive path is gated
// behind a "I understand" checkbox so a single accidental click cannot
// trigger it.

import { useState } from 'react'

import { ApiError, api } from '../../lib/api'
import { Modal } from '../../components/modal'

import styles from './git-history-modals.module.css'
import type { CherryPickOptions, LogEntryDetail, ResetMode, RevertOptions } from './types'

interface BaseProps {
  apiBase: string
  target: LogEntryDetail
  onClose: () => void
  onDone: () => void
}

// === Cherry-pick ==========================================================

export function CherryPickModal({ apiBase, target, onClose, onDone }: BaseProps) {
  const [noCommit, setNoCommit] = useState(false)
  const [pending, setPending] = useState(false)
  const [output, setOutput] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setPending(true)
    setError(null)
    setOutput(null)
    try {
      const opts: CherryPickOptions = { commitSha: target.hash, noCommit }
      const res = await api.post<{ output?: string }>(`${apiBase}/cherry-pick`, opts)
      setOutput(res.output ?? '')
      onDone()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : String(e)
      setError(msg)
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Cherry-pick commit" width={520}>
      <div className={styles.preview}>
        <p className={styles.label}>Will apply this commit on top of the current branch:</p>
        <div className={styles.commitBlock}>
          <code className={styles.hash}>{target.hash.slice(0, 8)}</code>
          <span className={styles.subj}>{target.subject}</span>
          <span className={styles.author}>{target.author}</span>
        </div>
        <label className={styles.checkbox}>
          <input
            type="checkbox"
            checked={noCommit}
            onChange={(e) => setNoCommit(e.target.checked)}
            data-testid="cherry-pick-no-commit"
          />
          --no-commit (stage changes without committing)
        </label>
        {error && <p className={styles.error}>Error: {error}</p>}
        {output && <pre className={styles.output}>{output}</pre>}
      </div>
      <div className={styles.actionRow}>
        <button className={styles.btn} onClick={onClose} disabled={pending}>
          Cancel
        </button>
        <button
          className={`${styles.btn} ${styles.btnPrimary}`}
          onClick={submit}
          disabled={pending}
          data-testid="cherry-pick-confirm"
        >
          {pending ? 'Cherry-picking…' : 'Cherry-pick'}
        </button>
      </div>
    </Modal>
  )
}

// === Revert ===============================================================

export function RevertModal({ apiBase, target, onClose, onDone }: BaseProps) {
  const [noCommit, setNoCommit] = useState(false)
  const [pending, setPending] = useState(false)
  const [output, setOutput] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setPending(true)
    setError(null)
    setOutput(null)
    try {
      const opts: RevertOptions = { commitSha: target.hash, noCommit }
      const res = await api.post<{ output?: string }>(`${apiBase}/revert`, opts)
      setOutput(res.output ?? '')
      onDone()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : String(e)
      setError(msg)
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Revert commit" width={520}>
      <div className={styles.preview}>
        <p className={styles.label}>Will create a new commit that undoes:</p>
        <div className={styles.commitBlock}>
          <code className={styles.hash}>{target.hash.slice(0, 8)}</code>
          <span className={styles.subj}>{target.subject}</span>
        </div>
        <p className={styles.hint}>
          The new commit's message will be auto-generated as
          <code> Revert &quot;{truncate(target.subject, 40)}&quot;</code>.
        </p>
        <label className={styles.checkbox}>
          <input
            type="checkbox"
            checked={noCommit}
            onChange={(e) => setNoCommit(e.target.checked)}
            data-testid="revert-no-commit"
          />
          --no-commit (stage the revert without committing)
        </label>
        {error && <p className={styles.error}>Error: {error}</p>}
        {output && <pre className={styles.output}>{output}</pre>}
      </div>
      <div className={styles.actionRow}>
        <button className={styles.btn} onClick={onClose} disabled={pending}>
          Cancel
        </button>
        <button
          className={`${styles.btn} ${styles.btnDanger}`}
          onClick={submit}
          disabled={pending}
          data-testid="revert-confirm"
        >
          {pending ? 'Reverting…' : 'Revert'}
        </button>
      </div>
    </Modal>
  )
}

// === Reset ================================================================

export function ResetModal({ apiBase, target, onClose, onDone }: BaseProps) {
  const [mode, setMode] = useState<ResetMode>('mixed')
  const [stage, setStage] = useState<1 | 2>(1)
  const [understood, setUnderstood] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [output, setOutput] = useState<string | null>(null)

  const isHard = mode === 'hard'

  const next = () => setStage(2)

  const submit = async () => {
    setPending(true)
    setError(null)
    setOutput(null)
    try {
      const res = await api.post<{ output?: string }>(`${apiBase}/reset`, {
        commitSha: target.hash,
        mode,
      })
      setOutput(res.output ?? '')
      onDone()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : String(e)
      setError(msg)
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Reset to ${target.hash.slice(0, 8)}`} width={560}>
      <div className={styles.preview}>
        <p className={styles.label}>Will move HEAD to:</p>
        <div className={styles.commitBlock}>
          <code className={styles.hash}>{target.hash.slice(0, 8)}</code>
          <span className={styles.subj}>{target.subject}</span>
        </div>

        {stage === 1 && (
          <>
            <fieldset className={styles.modeRow} data-testid="reset-mode-row">
              <legend className={styles.label}>Mode</legend>
              <ModeRadio
                value="soft"
                current={mode}
                onChange={setMode}
                label="--soft"
                desc="Move HEAD only. Index and working tree untouched. Safest."
              />
              <ModeRadio
                value="mixed"
                current={mode}
                onChange={setMode}
                label="--mixed (default)"
                desc="Move HEAD + reset index. Working tree files are untouched."
              />
              <ModeRadio
                value="hard"
                current={mode}
                onChange={setMode}
                label="--hard"
                desc="Move HEAD + reset index + reset working tree. DESTRUCTIVE."
                danger
              />
            </fieldset>
          </>
        )}

        {stage === 2 && isHard && (
          <div className={styles.danger} data-testid="reset-hard-stage-2">
            <p>
              <strong>Hard reset is destructive.</strong> Uncommitted local changes will be lost.
            </p>
            <p className={styles.hint}>
              Recoverable from the reflog for ~90 days using
              <code> git reflog</code> + <code>git reset --hard HEAD@{'{n}'}</code>.
            </p>
            <label className={styles.checkbox}>
              <input
                type="checkbox"
                checked={understood}
                onChange={(e) => setUnderstood(e.target.checked)}
                data-testid="reset-understood"
              />
              I understand the working tree will be overwritten.
            </label>
          </div>
        )}

        {error && <p className={styles.error}>Error: {error}</p>}
        {output && <pre className={styles.output}>{output}</pre>}
      </div>

      <div className={styles.actionRow}>
        <button className={styles.btn} onClick={onClose} disabled={pending}>
          Cancel
        </button>
        {stage === 1 && isHard && (
          <button
            className={`${styles.btn} ${styles.btnDanger}`}
            onClick={next}
            data-testid="reset-stage-1-next"
          >
            Continue…
          </button>
        )}
        {stage === 1 && !isHard && (
          <button
            className={`${styles.btn} ${styles.btnPrimary}`}
            onClick={submit}
            disabled={pending}
            data-testid="reset-confirm"
          >
            {pending ? 'Resetting…' : `Reset --${mode}`}
          </button>
        )}
        {stage === 2 && (
          <button
            className={`${styles.btn} ${styles.btnDanger}`}
            onClick={submit}
            disabled={pending || !understood}
            data-testid="reset-hard-confirm"
          >
            {pending ? 'Resetting --hard…' : 'Reset --hard'}
          </button>
        )}
      </div>
    </Modal>
  )
}

function ModeRadio({
  value,
  current,
  onChange,
  label,
  desc,
  danger,
}: {
  value: ResetMode
  current: ResetMode
  onChange: (v: ResetMode) => void
  label: string
  desc: string
  danger?: boolean
}) {
  return (
    <label className={`${styles.modeOption} ${danger ? styles.modeDanger : ''}`}>
      <input
        type="radio"
        name="reset-mode"
        value={value}
        checked={current === value}
        onChange={() => onChange(value)}
        data-testid={`reset-mode-${value}`}
      />
      <span className={styles.modeLabel}>{label}</span>
      <span className={styles.modeDesc}>{desc}</span>
    </label>
  )
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}
