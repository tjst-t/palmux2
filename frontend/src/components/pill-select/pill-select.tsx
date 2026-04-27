// PillSelect is a compact button-with-popup replacement for the native
// <select>. The native control's dropdown panel is rendered by the OS and
// ignores CSS custom properties — that meant the model / mode / effort
// pickers in the Claude composer broke design tokens (light popup on a
// dark app). PillSelect is fully styled and behaves like a small menu.
//
// Behaviour:
//   - keyboard: ↑↓ moves selection, Enter / Space accepts, Esc closes.
//   - mouse: click the trigger to toggle, click an option to commit,
//     click outside to dismiss.
//
// Visual: the trigger looks like the existing modePill (transparent
// background, hover surface, small ▼ glyph). The popup floats above the
// trigger so it doesn't collide with the conversation area.

import { useEffect, useMemo, useRef, useState } from 'react'

import styles from './pill-select.module.css'

export interface PillSelectOption {
  value: string
  label: string
  detail?: string
}

interface Props {
  options: PillSelectOption[]
  value: string
  onChange: (value: string) => void
  ariaLabel?: string
  /** Optional small leading label, like "model" or "mode". */
  prefix?: string
}

export function PillSelect({ options, value, onChange, ariaLabel, prefix }: Props) {
  const [open, setOpen] = useState(false)
  const [hoverIndex, setHoverIndex] = useState(0)
  const ref = useRef<HTMLDivElement | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)

  const currentIndex = useMemo(
    () => Math.max(0, options.findIndex((o) => o.value === value)),
    [options, value],
  )

  // Reset hover to the current selection whenever the popup opens.
  useEffect(() => {
    if (open) setHoverIndex(currentIndex)
  }, [open, currentIndex])

  // Click-outside / Esc closes.
  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      if (!ref.current) return
      if (!ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        setOpen(false)
      }
    }
    window.addEventListener('pointerdown', onPointer, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointer, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const handleTriggerKey = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setOpen(true)
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      setOpen((v) => !v)
    }
  }

  const handlePopupKey = (e: React.KeyboardEvent<HTMLUListElement>) => {
    if (!open || options.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHoverIndex((i) => Math.min(options.length - 1, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHoverIndex((i) => Math.max(0, i - 1))
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      const opt = options[hoverIndex]
      if (opt) {
        onChange(opt.value)
        setOpen(false)
      }
    } else if (e.key === 'Tab') {
      setOpen(false)
    }
  }

  const current = options[currentIndex]

  return (
    <div ref={ref} className={styles.wrap}>
      <button
        type="button"
        className={styles.trigger}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={handleTriggerKey}
      >
        {prefix && <span className={styles.prefix}>{prefix}</span>}
        <span className={styles.value}>{current?.label ?? value ?? ''}</span>
        <span className={styles.caret} aria-hidden>▾</span>
      </button>
      {open && (
        <ul
          ref={listRef}
          className={styles.popup}
          role="listbox"
          aria-label={ariaLabel}
          tabIndex={-1}
          onKeyDown={handlePopupKey}
          // autoFocus lets handlePopupKey see ↑↓ immediately after click.
          autoFocus
        >
          {options.map((opt, i) => (
            <li
              key={opt.value}
              role="option"
              aria-selected={i === currentIndex}
              className={
                i === hoverIndex
                  ? `${styles.option} ${styles.active}`
                  : i === currentIndex
                    ? `${styles.option} ${styles.current}`
                    : styles.option
              }
              onMouseEnter={() => setHoverIndex(i)}
              onMouseDown={(e) => {
                // Prevent the trigger from losing focus before onClick.
                e.preventDefault()
                onChange(opt.value)
                setOpen(false)
              }}
            >
              <span className={styles.optionLabel}>{opt.label}</span>
              {opt.detail && <span className={styles.optionDetail}>{opt.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
