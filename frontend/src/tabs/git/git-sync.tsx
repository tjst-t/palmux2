// GitSync — Push / Pull / Fetch buttons + Force-push 2-step confirm dialog
// (S012-1-14, S012-1-15). Surfaces the credential dialog that the
// `git.credentialRequest` WS event triggers.

import { useEffect, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'

import styles from './git-sync.module.css'

interface Props {
  apiBase: string
  /** Re-fetch status after a successful pull/push so the FE updates. */
  onAfter: () => void
}

type Op = 'push' | 'pull' | 'fetch' | null

export function GitSync({ apiBase, onAfter }: Props) {
  const [busy, setBusy] = useState<Op>(null)
  const [toast, setToast] = useState<{ kind: 'info' | 'error' | 'success'; text: string } | null>(
    null,
  )

  // Auto-clear the toast after a few seconds.
  useEffect(() => {
    if (!toast) return
    const id = setTimeout(() => setToast(null), 4000)
    return () => clearTimeout(id)
  }, [toast])

  const run = async <T,>(op: Exclude<Op, null>, body: T) => {
    setBusy(op)
    setToast({ kind: 'info', text: `${op}…` })
    try {
      const res = await api.post<{ output: string }>(`${apiBase}/${op}`, body)
      const text = (res.output ?? '').trim()
      setToast({ kind: 'success', text: text || `${op} OK` })
      onAfter()
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setToast({ kind: 'error', text: msg })
    } finally {
      setBusy(null)
    }
  }

  const onPush = () => run('push', { forceWithLease: false })
  const onPull = () => run('pull', { ffOnly: true })
  const onFetch = () => run('fetch', { prune: true })

  // Force-push: 2-step confirm. Step 1 explains --force-with-lease;
  // step 2 is the final go/no-go.
  const onForcePush = async () => {
    const step1 = await confirmDialog.ask({
      title: 'Force-push?',
      message:
        'Force-push rewrites remote history. We strongly recommend --force-with-lease, which only updates the remote branch if its tip matches what you last fetched. Continue with --force-with-lease?',
      confirmLabel: 'Use --force-with-lease',
      danger: true,
    })
    if (!step1) return
    const step2 = await confirmDialog.ask({
      title: 'Final confirmation',
      message:
        'This will overwrite the remote branch. Anyone with this branch checked out will have to reset their local copy. Proceed?',
      confirmLabel: 'Force-push (with lease)',
      danger: true,
    })
    if (!step2) return
    await run('push', { forceWithLease: true })
  }

  return (
    <div className={styles.bar}>
      <button
        className={styles.btn}
        disabled={busy !== null}
        onClick={onFetch}
        data-testid="git-fetch-btn"
      >
        Fetch
      </button>
      <button
        className={styles.btn}
        disabled={busy !== null}
        onClick={onPull}
        data-testid="git-pull-btn"
      >
        Pull
      </button>
      <button
        className={styles.btn}
        disabled={busy !== null}
        onClick={onPush}
        data-testid="git-push-btn"
      >
        Push
      </button>
      <button
        className={`${styles.btn} ${styles.danger}`}
        disabled={busy !== null}
        onClick={onForcePush}
        data-testid="git-force-push-btn"
      >
        Force…
      </button>
      {toast && (
        <div
          className={`${styles.toast} ${
            toast.kind === 'error'
              ? styles.toastError
              : toast.kind === 'success'
                ? styles.toastSuccess
                : ''
          }`}
          data-testid="git-sync-toast"
        >
          {toast.text}
        </div>
      )}
    </div>
  )
}
