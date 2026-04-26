import { useCallback, useState } from 'react'

import type { ModifierKey } from '../types/toolbar'

export type ModifierMode = 'off' | 'oneshot' | 'lock'

export interface ModifierState {
  ctrl: ModifierMode
  alt: ModifierMode
  shift: ModifierMode
  meta: ModifierMode
}

const INITIAL: ModifierState = { ctrl: 'off', alt: 'off', shift: 'off', meta: 'off' }

// useModifiers — taps cycle a modifier through off → oneshot → lock → off.
// Use `consume()` after a non-modifier action: it clears any oneshot keys
// while leaving locked keys intact.
export function useModifiers() {
  const [state, setState] = useState<ModifierState>(INITIAL)

  const tap = useCallback((m: ModifierKey) => {
    setState((prev) => ({
      ...prev,
      [m]: cycle(prev[m]),
    }))
  }, [])

  const consume = useCallback(() => {
    setState((prev) => ({
      ctrl: prev.ctrl === 'oneshot' ? 'off' : prev.ctrl,
      alt: prev.alt === 'oneshot' ? 'off' : prev.alt,
      shift: prev.shift === 'oneshot' ? 'off' : prev.shift,
      meta: prev.meta === 'oneshot' ? 'off' : prev.meta,
    }))
  }, [])

  const reset = useCallback(() => setState(INITIAL), [])

  return { state, tap, consume, reset }
}

function cycle(m: ModifierMode): ModifierMode {
  if (m === 'off') return 'oneshot'
  if (m === 'oneshot') return 'lock'
  return 'off'
}

// applyModifiers wraps a base sequence with whatever modifiers are active.
// Modifier handling for terminals:
//   - ctrl: replace ASCII letter with the corresponding control byte
//          (e.g. ctrl+c -> \x03). For non-letters, send as-is.
//   - alt:  prefix with \x1b (xterm convention).
//   - shift / meta: pass-through (the caller is expected to have produced
//          the right uppercase / cmd-prefixed sequence already).
export function applyModifiers(seq: string, mods: ModifierState): string {
  let out = seq
  if (mods.ctrl !== 'off' && out.length === 1) {
    const code = out.charCodeAt(0)
    if (code >= 0x40 && code <= 0x7e) {
      out = String.fromCharCode(code & 0x1f)
    }
  }
  if (mods.alt !== 'off') {
    out = '\x1b' + out
  }
  return out
}
