import { useRef, useState } from 'react'

import { useFocusedTerminal } from '../hooks/use-focused-terminal'
import { terminalManager } from '../lib/terminal-manager'

import styles from './ime-bar.module.css'

// IMEBar lives at the top of MainArea while deviceSettings.imeMode === 'ime'.
// The user types into a normal <input> (so the OS IME can compose Japanese /
// Chinese / Korean text), and we forward the committed string + Enter to the
// focused terminal. When mode is 'none' the bar isn't rendered; 'direct' is
// passed to the bar via inputmode so the soft keyboard stays open without
// IME composition.
interface Props {
  mode: 'direct' | 'ime'
}

export function IMEBar({ mode }: Props) {
  const [value, setValue] = useState('')
  const composing = useRef(false)
  const focused = useFocusedTerminal()

  const send = (text: string, withReturn: boolean) => {
    if (!focused.termKey || !text) return
    terminalManager.sendInput(focused.termKey, text + (withReturn ? '\r' : ''))
    terminalManager.focus(focused.termKey)
  }

  return (
    <div className={styles.bar} role="region" aria-label="IME bar">
      <span className={styles.label}>{mode === 'ime' ? 'IME' : 'Direct'}</span>
      <input
        className={styles.input}
        type="text"
        value={value}
        inputMode={mode === 'direct' ? 'text' : undefined}
        onCompositionStart={() => {
          composing.current = true
        }}
        onCompositionEnd={() => {
          composing.current = false
        }}
        onKeyDown={(e) => {
          if (e.key !== 'Enter') return
          if (composing.current) return // let the IME finalise first
          e.preventDefault()
          send(value, true)
          setValue('')
        }}
        onChange={(e) => setValue(e.target.value)}
        placeholder={mode === 'ime' ? '日本語入力 → Enter で送信' : 'Type here, Enter to send'}
      />
      <button
        type="button"
        className={styles.send}
        onClick={() => {
          send(value, true)
          setValue('')
        }}
      >
        Send ↵
      </button>
    </div>
  )
}
