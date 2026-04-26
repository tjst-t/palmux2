// Promise-based input prompt — replaces window.prompt() so we can style it
// consistently with the rest of the app.
//
// Usage:
//   const next = await promptDialog.ask({
//     title: 'Rename tab',
//     defaultValue: current,
//     confirmLabel: 'Rename',
//   })
//   if (next != null && next !== current) doIt(next)
//
// Resolves with the entered string, or null if the user cancelled.

import { useEffect, useRef, useState } from 'react'
import { create } from 'zustand'

import { Modal } from '../modal'
import styles from './prompt-dialog.module.css'

export interface PromptOptions {
  title: string
  message?: string
  defaultValue?: string
  placeholder?: string
  confirmLabel?: string
  cancelLabel?: string
  /** Called with the typed value; return a string to display as an inline
   *  error and block submit, or null/undefined to allow it. */
  validate?: (value: string) => string | null | undefined
}

interface State {
  open: boolean
  options: PromptOptions | null
  resolver: ((value: string | null) => void) | null
  ask: (opts: PromptOptions) => Promise<string | null>
  resolve: (value: string | null) => void
}

const usePromptStore = create<State>((set, get) => ({
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

export const promptDialog = {
  ask: (opts: PromptOptions) => usePromptStore.getState().ask(opts),
}

export function PromptDialogRenderer() {
  const open = usePromptStore((s) => s.open)
  const options = usePromptStore((s) => s.options)
  const resolve = usePromptStore((s) => s.resolve)

  return (
    <Modal open={open} onClose={() => resolve(null)} title={options?.title} width={420}>
      {options && <PromptForm options={options} onSubmit={resolve} />}
    </Modal>
  )
}

function PromptForm({
  options,
  onSubmit,
}: {
  options: PromptOptions
  onSubmit: (value: string | null) => void
}) {
  const [value, setValue] = useState(options.defaultValue ?? '')
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  // On open: focus + select-all so typing replaces the previous value.
  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
  }, [])

  const submit = (e?: React.FormEvent) => {
    e?.preventDefault()
    const errMsg = options.validate?.(value)
    if (errMsg) {
      setError(errMsg)
      return
    }
    onSubmit(value)
  }

  return (
    <form className={styles.body} onSubmit={submit}>
      {options.message && <p className={styles.message}>{options.message}</p>}
      <input
        ref={inputRef}
        className={styles.input}
        type="text"
        value={value}
        placeholder={options.placeholder}
        onChange={(e) => {
          setValue(e.target.value)
          if (error) setError(null)
        }}
      />
      {error && <p className={styles.error}>{error}</p>}
      <div className={styles.actions}>
        <button
          type="button"
          className={styles.cancel}
          onClick={() => onSubmit(null)}
        >
          {options.cancelLabel ?? 'Cancel'}
        </button>
        <button type="submit" className={styles.confirm}>
          {options.confirmLabel ?? 'OK'}
        </button>
      </div>
    </form>
  )
}
