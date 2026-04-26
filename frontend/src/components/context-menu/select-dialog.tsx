// Promise-based "pick one from a list" modal.
//
//   const choice = await selectDialog.ask({
//     title: 'Restart Claude',
//     options: [
//       { label: 'Default', value: '' },
//       { label: 'Opus 4.7', value: 'claude-opus-4-7' },
//     ],
//   })
//   if (choice != null) doRestart(choice)
//
// Resolves with the picked value, or null if cancelled.

import { create } from 'zustand'

import { Modal } from '../modal'
import styles from './select-dialog.module.css'

export interface SelectOption {
  label: string
  value: string
  detail?: string
}

export interface SelectOptions {
  title: string
  message?: string
  options: SelectOption[]
  cancelLabel?: string
}

interface State {
  open: boolean
  options: SelectOptions | null
  resolver: ((value: string | null) => void) | null
  ask: (opts: SelectOptions) => Promise<string | null>
  resolve: (value: string | null) => void
}

const useSelectStore = create<State>((set, get) => ({
  open: false,
  options: null,
  resolver: null,
  ask: (opts) =>
    new Promise<string | null>((resolve) => {
      const prev = get().resolver
      if (prev) prev(null)
      set({ open: true, options: opts, resolver: resolve })
    }),
  resolve: (value) => {
    const r = get().resolver
    if (r) r(value)
    set({ open: false, options: null, resolver: null })
  },
}))

export const selectDialog = {
  ask: (opts: SelectOptions) => useSelectStore.getState().ask(opts),
}

export function SelectDialogRenderer() {
  const open = useSelectStore((s) => s.open)
  const options = useSelectStore((s) => s.options)
  const resolve = useSelectStore((s) => s.resolve)

  return (
    <Modal open={open} onClose={() => resolve(null)} title={options?.title} width={420}>
      {options && (
        <div className={styles.body}>
          {options.message && <p className={styles.message}>{options.message}</p>}
          <ul className={styles.list}>
            {options.options.map((opt) => (
              <li key={opt.value}>
                <button
                  type="button"
                  className={styles.row}
                  onClick={() => resolve(opt.value)}
                >
                  <span className={styles.label}>{opt.label}</span>
                  {opt.detail && <span className={styles.detail}>{opt.detail}</span>}
                </button>
              </li>
            ))}
          </ul>
          <div className={styles.actions}>
            <button type="button" className={styles.cancel} onClick={() => resolve(null)}>
              {options.cancelLabel ?? 'Cancel'}
            </button>
          </div>
        </div>
      )}
    </Modal>
  )
}
