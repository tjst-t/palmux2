// update-container-confirm.tsx — S7364e3: Confirm modal for regenerating an
// incus container on a newer palmux-ws image. Warns that the running session
// (claude/tmux) will be restarted. Mirrors runtime-change-confirm.tsx.

import styles from './modal.module.css'

interface Props {
  onConfirm: () => void
  onCancel: () => void
}

export function UpdateContainerConfirm({ onConfirm, onCancel }: Props) {
  return (
    <div
      className={styles.overlay}
      onClick={onCancel}
      data-testid="update-container-confirm"
    >
      <div
        className={styles.card}
        style={{ maxWidth: 420 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="update-confirm-title"
      >
        <div style={{ padding: '20px 20px 0' }}>
          <h2
            id="update-confirm-title"
            style={{ margin: 0, fontSize: 15, fontWeight: 600, fontFamily: 'var(--font-ui)' }}
          >
            Update this Workspace&rsquo;s container?
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
            A newer <strong>palmux-ws</strong> image is available. Updating
            recreates this Workspace&rsquo;s container on the new image: the
            running <strong>tmux session is restarted</strong> and Claude resumes
            with <code>--resume</code>. Your code, <code>~/.claude</code> and
            dotfiles are bind-mounted and unaffected; in-container packages and
            running processes are reset. The update is verified before the old
            container is replaced — if it fails, the existing container is kept.
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
            data-testid="update-container-confirm-cancel"
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
            data-testid="update-container-confirm-ok"
            onClick={onConfirm}
          >
            Update container
          </button>
        </div>
      </div>
    </div>
  )
}
