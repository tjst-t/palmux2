// Focus-aware keybinding helper (S020).
//
// The pre-S020 git tab embedded its Magit-style key handler directly in
// `git-status.tsx`, scoping firing to "focus is inside this DOM subtree
// AND the active element isn't a text input". That worked, but every new
// tab type wanting tab-scoped keys had to copy the same pattern. S020
// extracts it into one reusable hook so we have a single place to reason
// about how Bash receives `s` (it doesn't run any handler, so the keypress
// passes through xterm.js as normal shell input) and how Git focuses
// `s` to stage the selected file.
//
// Usage:
//
//   const ref = useRef<HTMLDivElement | null>(null)
//   useTabKeybindings(ref, {
//     s: (e) => { e.preventDefault(); doStage() },
//     u: (e) => { e.preventDefault(); doUnstage() },
//   })
//   return <div ref={ref}>…</div>
//
// Behaviour:
//   - Each binding fires only when:
//       (a) the bound DOM subtree is mounted AND
//       (b) the current `document.activeElement` is inside that subtree AND
//       (c) the focused element is not an INPUT / TEXTAREA / SELECT and
//           is not contentEditable.
//   - Modifier-bearing keypresses (Ctrl / Cmd / Alt) are ignored — let
//     the browser / xterm.js handle them.
//   - When a tab is unmounted (e.g. user switches from Git to Bash) the
//     `useEffect` cleanup detaches the listener entirely, so other tabs'
//     terminals receive `s` / `u` / `c` / etc. without interception.
//
// Mental model: bindings live with the tab that owns them. Switching tabs
// removes the binding outright; that's the "Bash isolation" guarantee.
//
// See `docs/sprint-logs/S020/decisions.md` for why we chose subtree-scoped
// listeners over a global event router.

import { useEffect } from 'react'

export type TabKeyHandler = (event: KeyboardEvent) => void

/**
 * Map from key (single character or special key name like `Escape`,
 * `ArrowUp`) to the handler that fires when that key is pressed while
 * the bound subtree owns focus.
 */
export type TabKeyBindings = Record<string, TabKeyHandler>

interface UseTabKeybindingsOptions {
  /**
   * When false the bindings are not attached. Useful for opt-out flags
   * (e.g. while a modal is open above the tab content).
   */
  enabled?: boolean
}

export function useTabKeybindings(
  containerRef: React.RefObject<HTMLElement | null>,
  bindings: TabKeyBindings,
  options: UseTabKeybindingsOptions = {},
): void {
  const { enabled = true } = options

  useEffect(() => {
    if (!enabled) return
    const onKey = (event: KeyboardEvent) => {
      if (event.metaKey || event.ctrlKey || event.altKey) return
      const handler = bindings[event.key]
      // Skip the metadata key bindToTabType injects.
      if (!handler || event.key === '__tabType') return
      const root = containerRef.current
      if (!root) return
      const active = document.activeElement as HTMLElement | null
      if (!active || !root.contains(active)) return
      // Skip when the user is typing into a text input.
      if (
        active.tagName === 'INPUT' ||
        active.tagName === 'TEXTAREA' ||
        active.tagName === 'SELECT' ||
        active.isContentEditable
      ) {
        return
      }
      handler(event)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // bindings is a plain object; we depend on the identity supplied by
    // the caller (use useMemo if your bindings change between renders).
  }, [containerRef, bindings, enabled])
}

// bindToTabType (S020) is a documentation-friendly wrapper that tags the
// bindings with the tab type they belong to. The tag is informational —
// nothing in the runtime cares about it — but it lets a reader see at a
// glance that these `s/u/c/d/p/f` bindings are Git-tab-scoped, not
// global. Useful when grepping for which tab owns which key.
export function bindToTabType(
  tabType: string,
  bindings: TabKeyBindings,
): TabKeyBindings & { __tabType: string } {
  return Object.assign({}, bindings, { __tabType: tabType }) as TabKeyBindings & {
    __tabType: string
  }
}
