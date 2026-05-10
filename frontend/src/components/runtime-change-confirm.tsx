// Sdd4ce1-5-6: confirmation dialog when switching runtime kind on a
// Workspace that has active Bash/Claude tabs. Lists each affected tab and
// surfaces a danger button "Stop tabs & switch" + "Cancel". When the
// Workspace has no active terminal-backed tabs, the caller should skip
// this dialog and switch directly.
//
// data-testid contract: change-runtime-confirm, confirm-tab-list,
// confirm-cancel, confirm-switch.

import type { Tab } from '../lib/api'

import styles from './repo-picker.module.css'

interface Props {
  open: boolean
  fromKind: string
  toKind: string
  /** Tabs that will be killed by the runtime switch — Bash + Claude
   *  instances. Files/Git/Sprint do not need to be listed since they
   *  carry no terminal state. */
  affectedTabs: Tab[]
  onCancel: () => void
  onConfirm: () => void
}

export function RuntimeChangeConfirm({ open, fromKind, toKind, affectedTabs, onCancel, onConfirm }: Props) {
  if (!open) return null
  return (
    <div className={styles.overlay} onClick={onCancel} data-testid="change-runtime-confirm">
      <div
        className={styles.card}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="change-runtime-title"
        style={{ width: 'min(480px, 92vw)' }}
      >
        <div className={styles.header}>
          <h2 className={styles.title} id="change-runtime-title">Change runtime?</h2>
          <p className={styles.sub}>
            Switching from <code style={{ fontFamily: 'var(--font-mono)' }}>{fromKind}</code> to{' '}
            <code style={{ fontFamily: 'var(--font-mono)' }}>{toKind}</code> will tear down the
            current runtime. The following tabs will be stopped:
          </p>
        </div>
        <ul className={styles.list} data-testid="confirm-tab-list" style={{ maxHeight: '40vh' }}>
          {affectedTabs.map((t) => (
            <li key={t.id}>
              <div className={styles.row} style={{ cursor: 'default' }}>
                <span className={styles.rowName}>{t.name || t.id}</span>
                <span className={styles.rowState}>{t.type}</span>
              </div>
            </li>
          ))}
          {affectedTabs.length === 0 && (
            <li className={styles.empty}>No active terminal tabs — switching is safe.</li>
          )}
        </ul>
        <div className={styles.footer}>
          <button className={styles.btnGhost} data-testid="confirm-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            className={styles.btnPrimary}
            data-testid="confirm-switch"
            onClick={onConfirm}
            style={{ background: 'var(--color-error, #ef4444)' }}
          >
            Stop tabs &amp; switch
          </button>
        </div>
      </div>
    </div>
  )
}
