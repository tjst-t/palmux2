import { useEffect, useMemo, useRef, useState } from 'react'

import { useFocusedTerminal } from '../../hooks/use-focused-terminal'
import { applyModifiers, useModifiers } from '../../hooks/use-modifiers'
import { api } from '../../lib/api'
import { terminalManager } from '../../lib/terminal-manager'
import { mergeToolbarConfig } from '../../lib/toolbar-defaults'
import { usePalmuxStore } from '../../stores/palmux-store'
import type { ToolbarButton, ToolbarConfig } from '../../types/toolbar'

import styles from './toolbar.module.css'

interface DetectedCommand {
  name: string
  source: string
  command: string
  line?: number
}

export function Toolbar() {
  const focused = useFocusedTerminal()
  const userToolbar = usePalmuxStore((s) => s.globalSettings.toolbar)
  const fontSize = usePalmuxStore((s) => s.deviceSettings.fontSize)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const modifiers = useModifiers()
  const [showCommands, setShowCommands] = useState(false)
  const [commands, setCommands] = useState<DetectedCommand[] | null>(null)

  const config: ToolbarConfig = useMemo(() => mergeToolbarConfig(userToolbar), [userToolbar])
  const mode: 'normal' | 'claude' = focused.tabType === 'claude' ? 'claude' : 'normal'
  const buttons = config[mode].rows

  // When the focused tab changes, reset modifiers + close popover.
  useEffect(() => {
    modifiers.reset()
    setShowCommands(false)
    // intentional: only depend on the focused tab id
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focused.termKey])

  // Lazy-load commands when the user opens the popover.
  useEffect(() => {
    if (!showCommands) return
    if (!focused.repoId || !focused.branchId) return
    let cancelled = false
    void api
      .get<DetectedCommand[]>(
        `/api/repos/${encodeURIComponent(focused.repoId)}/branches/${encodeURIComponent(focused.branchId)}/commands`,
      )
      .then((cs) => {
        if (!cancelled) setCommands(cs)
      })
      .catch(() => {
        if (!cancelled) setCommands([])
      })
    return () => {
      cancelled = true
    }
  }, [showCommands, focused.repoId, focused.branchId])

  const send = (data: string) => {
    if (!focused.termKey) return
    const out = applyModifiers(data, modifiers.state)
    terminalManager.sendInput(focused.termKey, out)
    modifiers.consume()
    terminalManager.focus(focused.termKey)
  }

  const onCommandPick = (c: DetectedCommand) => {
    if (!focused.termKey) return
    terminalManager.sendInput(focused.termKey, c.command + '\r')
    setShowCommands(false)
    terminalManager.focus(focused.termKey)
  }

  const onFontDelta = (delta: number) => {
    const next = Math.max(8, Math.min(28, fontSize + delta))
    if (next !== fontSize) setDeviceSetting('fontSize', next)
  }

  return (
    <div className={styles.toolbar} role="toolbar" aria-label="Terminal toolbar">
      <div
        className={`${styles.modeIndicator} ${mode === 'claude' ? styles.modeClaude : ''}`}
      >
        {mode === 'claude' ? 'Claude' : 'Normal'}
        {focused.termKey ? '' : ' (no terminal focused)'}
      </div>
      {buttons.map((row, i) => (
        <div key={i} className={styles.row}>
          {row.map((btn, j) => (
            <ButtonView
              key={j}
              btn={btn}
              modifiers={modifiers.state}
              onTapModifier={modifiers.tap}
              onSend={send}
              onFontDelta={onFontDelta}
              onCommandToggle={() => setShowCommands((v) => !v)}
              disabled={!focused.termKey && btn.type !== 'fontsize'}
            />
          ))}
        </div>
      ))}
      {showCommands && (
        <div className={styles.commandPopover} role="listbox" aria-label="Commands">
          {commands === null && <div className={styles.commandEmpty}>Loading…</div>}
          {commands && commands.length === 0 && (
            <div className={styles.commandEmpty}>
              No Makefile/package.json commands detected.
            </div>
          )}
          {commands?.map((c) => (
            <div
              key={`${c.source}:${c.name}`}
              className={styles.commandRow}
              role="option"
              tabIndex={0}
              onClick={() => onCommandPick(c)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') onCommandPick(c)
              }}
            >
              <span className={styles.commandTag}>{c.source}</span>
              <span>{c.command}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

interface ButtonViewProps {
  btn: ToolbarButton
  modifiers: ReturnType<typeof useModifiers>['state']
  onTapModifier: ReturnType<typeof useModifiers>['tap']
  onSend: (data: string) => void
  onFontDelta: (delta: number) => void
  onCommandToggle: () => void
  disabled: boolean
}

function ButtonView({
  btn,
  modifiers,
  onTapModifier,
  onSend,
  onFontDelta,
  onCommandToggle,
  disabled,
}: ButtonViewProps) {
  if (btn.type === 'modifier') {
    const mode = modifiers[btn.modifier]
    return (
      <button
        type="button"
        className={styles.btn}
        data-mode={mode}
        onClick={() => onTapModifier(btn.modifier)}
        disabled={disabled}
      >
        {btn.label ?? btn.modifier}
      </button>
    )
  }
  if (btn.type === 'fontsize') {
    return (
      <button
        type="button"
        className={styles.btn}
        onClick={() => onFontDelta(btn.delta)}
      >
        {btn.label ?? (btn.delta > 0 ? 'A+' : 'A−')}
      </button>
    )
  }
  if (btn.type === 'command') {
    return (
      <button
        type="button"
        className={styles.btn}
        onClick={onCommandToggle}
        disabled={disabled}
      >
        {btn.label ?? '⌘'}
      </button>
    )
  }
  if (btn.type === 'arrow') {
    return (
      <ArrowButtonView
        direction={btn.direction}
        label={btn.label ?? arrowGlyph(btn.direction)}
        disabled={disabled}
        onSend={onSend}
      />
    )
  }
  if (btn.type === 'ctrl-key') {
    return (
      <button
        type="button"
        className={styles.btn}
        onClick={() => onSend(ctrlByte(btn.key))}
        disabled={disabled}
      >
        {btn.label ?? `^${btn.key.toUpperCase()}`}
      </button>
    )
  }
  // type === 'key'
  return (
    <button
      type="button"
      className={styles.btn}
      onClick={() => onSend(btn.text ?? keyToBytes(btn.key))}
      disabled={disabled}
    >
      {btn.label ?? btn.key}
    </button>
  )
}

// ArrowButtonView — supports long-press auto-repeat (400ms initial, 80ms tick).
function ArrowButtonView({
  direction,
  label,
  disabled,
  onSend,
}: {
  direction: 'up' | 'down' | 'left' | 'right'
  label: string
  disabled: boolean
  onSend: (data: string) => void
}) {
  const seq = arrowSequence(direction)
  const intervalRef = useRef<number | null>(null)
  const timeoutRef = useRef<number | null>(null)

  const stop = () => {
    if (intervalRef.current !== null) {
      clearInterval(intervalRef.current)
      intervalRef.current = null
    }
    if (timeoutRef.current !== null) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
  }

  const start = () => {
    if (disabled) return
    onSend(seq)
    timeoutRef.current = window.setTimeout(() => {
      intervalRef.current = window.setInterval(() => onSend(seq), 80)
    }, 400)
  }

  useEffect(() => stop, [])

  return (
    <button
      type="button"
      className={styles.btn}
      onPointerDown={(e) => {
        e.preventDefault()
        start()
      }}
      onPointerUp={stop}
      onPointerLeave={stop}
      onPointerCancel={stop}
      disabled={disabled}
    >
      {label}
    </button>
  )
}

function arrowGlyph(direction: 'up' | 'down' | 'left' | 'right'): string {
  switch (direction) {
    case 'up':
      return '↑'
    case 'down':
      return '↓'
    case 'left':
      return '←'
    case 'right':
      return '→'
  }
}

function arrowSequence(direction: 'up' | 'down' | 'left' | 'right'): string {
  switch (direction) {
    case 'up':
      return '\x1b[A'
    case 'down':
      return '\x1b[B'
    case 'right':
      return '\x1b[C'
    case 'left':
      return '\x1b[D'
  }
}

function keyToBytes(name: string): string {
  switch (name) {
    case 'Esc':
    case 'Escape':
      return '\x1b'
    case 'Tab':
      return '\t'
    case 'Enter':
    case 'Return':
      return '\r'
    case 'Backspace':
      return '\x7f'
    case 'Delete':
      return '\x1b[3~'
    case 'Home':
      return '\x1b[H'
    case 'End':
      return '\x1b[F'
    case 'PageUp':
      return '\x1b[5~'
    case 'PageDown':
      return '\x1b[6~'
    default:
      return name
  }
}

function ctrlByte(letter: string): string {
  if (!letter) return ''
  const code = letter.toUpperCase().charCodeAt(0)
  if (code >= 0x40 && code <= 0x7e) return String.fromCharCode(code & 0x1f)
  return letter
}
