// Promise-based confirmation modal — replaces window.confirm() so we can
// style it like the rest of the app and route it through React state.
//
// Usage:
//   const ok = await confirmDialog.ask({
//     title: 'Close branch?',
//     message: 'Tmux session and worktree will be removed.',
//     confirmLabel: 'Close',
//     danger: true,
//   })
//   if (ok) doIt()

import { create } from 'zustand'

import { Modal } from '../modal'
import styles from './confirm-dialog.module.css'

export interface ConfirmOptions {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
}

interface State {
  open: boolean
  options: ConfirmOptions | null
  resolver: ((yes: boolean) => void) | null
  ask: (opts: ConfirmOptions) => Promise<boolean>
  resolve: (yes: boolean) => void
}

const useConfirmStore = create<State>((set, get) => ({
  open: false,
  options: null,
  resolver: null,
  ask: (opts) =>
    new Promise<boolean>((resolve) => {
      // If a previous prompt is still up, resolve it as cancel before
      // overlaying the new one.
      const prev = get().resolver
      if (prev) prev(false)
      set({ open: true, options: opts, resolver: resolve })
    }),
  resolve: (yes) => {
    const r = get().resolver
    if (r) r(yes)
    set({ open: false, options: null, resolver: null })
  },
}))

// Convenience module-level façade so callers don't have to use the hook.
export const confirmDialog = {
  ask: (opts: ConfirmOptions) => useConfirmStore.getState().ask(opts),
}

export function ConfirmDialogRenderer() {
  const open = useConfirmStore((s) => s.open)
  const options = useConfirmStore((s) => s.options)
  const resolve = useConfirmStore((s) => s.resolve)

  return (
    <Modal open={open} onClose={() => resolve(false)} title={options?.title} width={420}>
      {options && (
        <div className={styles.body}>
          <p className={styles.message}>{options.message}</p>
          <div className={styles.actions}>
            <button
              type="button"
              className={styles.cancel}
              onClick={() => resolve(false)}
              autoFocus={!options.danger}
            >
              {options.cancelLabel ?? 'Cancel'}
            </button>
            <button
              type="button"
              className={options.danger ? `${styles.confirm} ${styles.danger}` : styles.confirm}
              onClick={() => resolve(true)}
              autoFocus={options.danger ? false : undefined}
            >
              {options.confirmLabel ?? 'OK'}
            </button>
          </div>
        </div>
      )}
    </Modal>
  )
}
