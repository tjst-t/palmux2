// incus-group-recover.tsx — Sfef725-2/3: the incus-admin stale-group recover
// surface. Shown when the running palmux process does not yet have the
// incus-admin group (so incus-container switches silently fall back to host).
//
// Three states (Story 1):
//   stale + fixAvailable   → one-click recover button (restarts the user
//                            manager via the privileged `palmux fix-incus-group`
//                            verb), with a warning that running tmux/Claude
//                            sessions end (claude resumes with --resume).
//   stale + !fixAvailable  → the manual `sudo systemctl restart user@<uid>`
//                            command (no privileged verb installed).
//   not-member             → the `sudo usermod -aG incus-admin <user>` /
//                            re-run install.sh guidance (root action).
//
// On click it triggers POST /api/incus-group/fix and rides the S6ab0ed WS-drop
// → /health reconnect handshake to completion (handled in use-event-stream.ts).

import { useState } from 'react'

import { usePalmuxStore } from '../stores/palmux-store'

import { type IncusGroupStatus } from '../lib/api'
import styles from './incus-group-recover.module.css'

interface Props {
  status: IncusGroupStatus
  /** Optional context line (e.g. "Runtime switch failed — …") shown above. */
  context?: string
  /** Called to dismiss the surface (e.g. close an inline panel). */
  onDismiss?: () => void
}

export function IncusGroupRecover({ status, context, onDismiss }: Props) {
  const fixIncusGroup = usePalmuxStore((s) => s.fixIncusGroup)
  const inProgress = usePalmuxStore((s) => s.incusGroupFixInProgress)
  const [error, setError] = useState<string | null>(null)
  const [manualReveal, setManualReveal] = useState(false)

  const restartCmd = status.restartCommand ?? 'sudo systemctl restart user@$(id -u)'

  const onRecover = async () => {
    setError(null)
    try {
      await fixIncusGroup()
      // Success is observed by the reconnect handshake (toast). Nothing else here.
    } catch (err) {
      // 409 (no verb) or other failure — reveal the manual command.
      setError(err instanceof Error ? err.message : String(err))
      setManualReveal(true)
    }
  }

  return (
    <div className={styles.panel} data-testid="incus-group-recover" role="alert">
      {context && <div className={styles.context}>{context}</div>}

      {status.state === 'not-member' ? (
        <>
          <div className={styles.title}>incus-admin への追加が必要です</div>
          <p className={styles.body}>{status.detail}</p>
          <pre className={styles.cmd} data-testid="incus-group-usermod-cmd">
            sudo usermod -aG incus-admin {status.user || '$USER'}
          </pre>
          <p className={styles.hint}>
            追加後、install.sh の再実行 または user マネージャ再起動で反映されます。
          </p>
        </>
      ) : (
        <>
          <div className={styles.title}>incus-admin がプロセスに未反映です</div>
          <p className={styles.body}>{status.detail}</p>

          {status.fixAvailable && !manualReveal ? (
            <>
              <button
                className={styles.recoverBtn}
                data-testid="incus-group-recover-btn"
                onClick={() => void onRecover()}
                disabled={inProgress}
              >
                {inProgress ? '適用中… 再接続を待っています' : 'incus-admin を適用 (サーバ再起動)'}
              </button>
              <p className={styles.warn} data-testid="incus-group-recover-warning">
                ⚠ 走行中の tmux / Claude セッションが一旦落ちます (claude は{' '}
                <code>--resume</code> で復帰)。user マネージャ再起動後、palmux は自動で再起動し
                この画面は数秒で再接続します。
              </p>
            </>
          ) : (
            <>
              <p className={styles.hint} data-testid="incus-group-manual-guidance">
                自動復旧ボタンは使えません (特権 verb 未導入)。手動で次を実行してください:
              </p>
              <pre className={styles.cmd} data-testid="incus-group-manual-cmd">
                {restartCmd}
              </pre>
              <p className={styles.hint}>
                <code>systemctl --user restart palmux2</code> では不十分です (user マネージャが
                古いグループを保持しているため)。
              </p>
            </>
          )}
          {error && <div className={styles.err}>{error}</div>}
        </>
      )}

      {onDismiss && (
        <button className={styles.dismiss} onClick={onDismiss} aria-label="閉じる">
          ×
        </button>
      )}
    </div>
  )
}
