// runtime-change-confirm.tsx — S8478ca-5: Confirm modal for changing runtime
// on an already-open Workspace. Data-testids per gui-spec.

import styles from './modal.module.css'

interface Props {
  newKind: string
  onConfirm: () => void
  onCancel: () => void
}

export function RuntimeChangeConfirm({ newKind, onConfirm, onCancel }: Props) {
  return (
    <div
      className={styles.overlay}
      onClick={onCancel}
      data-testid="runtime-change-confirm"
    >
      <div
        className={styles.card}
        style={{ maxWidth: 400 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="runtime-confirm-title"
      >
        <div style={{ padding: '20px 20px 0' }}>
          <h2
            id="runtime-confirm-title"
            style={{ margin: 0, fontSize: 15, fontWeight: 600, fontFamily: 'var(--font-ui)' }}
          >
            Change runtime to {newKind}?
          </h2>
          <p
            style={{
              margin: '10px 0 0',
              fontSize: 13,
              lineHeight: 1.6,
              color: 'var(--color-fg-muted)',
              fontFamily: 'var(--font-ui)',
            }}
          >
            This workspace is currently open. Changing to{' '}
            <strong>{newKind}</strong> will immediately restart the workspace:
            the current tmux session will be closed and a new one will be
            created in the{' '}
            {newKind === 'incus-container' ? 'Incus container' : 'host environment'}.
            Unsaved terminal work will be lost.
          </p>
        </div>
        <div
          style={{
            display: 'flex',
            justifyContent: 'flex-end',
            gap: 8,
            padding: '16px 20px',
          }}
        >
          <button
            style={{
              padding: '6px 14px',
              borderRadius: 'var(--radius-sm, 4px)',
              border: '1px solid var(--color-border)',
              background: 'transparent',
              color: 'var(--color-fg-muted)',
              cursor: 'pointer',
              fontSize: 13,
              fontFamily: 'var(--font-ui)',
            }}
            data-testid="runtime-change-confirm-cancel"
            onClick={onCancel}
          >
            Cancel
          </button>
          <button
            style={{
              padding: '6px 14px',
              borderRadius: 'var(--radius-sm, 4px)',
              border: 'none',
              background: 'var(--color-accent, #7c8aff)',
              color: '#fff',
              cursor: 'pointer',
              fontSize: 13,
              fontFamily: 'var(--font-ui)',
              fontWeight: 500,
            }}
            data-testid="runtime-change-confirm-ok"
            onClick={onConfirm}
          >
            Restart in {newKind}
          </button>
        </div>
      </div>
    </div>
  )
}
