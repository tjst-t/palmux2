// S4b9df4-3: unify the Claude tab's three keyboard shortcuts behind
// one hook so future shortcut additions go through one place — and
// the textarea-focus guard is implemented once instead of three times.
//
// Shortcuts owned by this hook (was three separate useEffects in
// claude-agent-view.tsx l.86-98 / 110-126 / 161-176):
//
//   1. ⌘H / Ctrl+H              → toggle the session history popup
//   2. ⌘F / Ctrl+F              → open the in-conversation search bar
//   3. y / n / Escape           → respond to a pending permission (only
//                                  when one is pending)
//
// Guards:
//   - textarea / input focus suppresses 1 and 3 (so the user can type
//     letters into the composer without triggering shortcuts)
//   - Cmd+F (#2) is suppressed when the wrap doesn't contain the
//     focused element — preserves the user's normal browser Find when
//     a different tab is active
//
// Internally we install ONE keydown listener (capture phase, like the
// previous Cmd+F handler) and dispatch by event shape. This avoids
// the three-listener stack the prior code maintained and makes it
// trivial to add a fourth shortcut later.
//
// User-facing behaviour: identical to the three useEffects this
// replaces. The guards / preventDefault calls are reproduced byte-for-
// byte.

import { useEffect, type RefObject } from 'react'

import type { useAgent } from '../use-agent'

type AgentSendApi = ReturnType<typeof useAgent>['send']

export interface PendingPermissionLike {
  permissionId: string
}

export interface UseClaudeShortcutsArgs {
  /** Toggles the session history popup. Called for ⌘H / Ctrl+H. */
  onToggleHistory: () => void
  /** Opens the in-conversation search bar. Called for ⌘F / Ctrl+F. */
  onOpenSearch: () => void
  /** Wraps the Claude tab. Used to gate ⌘F to the focused tab so the
   *  browser's normal Find still works on other panes. */
  wrapRef: RefObject<HTMLDivElement | null>
  /** Currently pending permission, or null. y / n / Escape only fire
   *  when this is non-null (no shortcut spam when nothing is asking). */
  pendingPermission: PendingPermissionLike | null | undefined
  /** Agent send API used to respond to the pending permission. */
  send: AgentSendApi
}

/** Returns true when the keyboard target is a text-entry control we
 *  must not steal keys from. */
function isInTextField(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el) return false
  const tag = el.tagName
  return tag === 'TEXTAREA' || tag === 'INPUT'
}

export function useClaudeShortcuts(args: UseClaudeShortcutsArgs): void {
  const { onToggleHistory, onOpenSearch, wrapRef, pendingPermission, send } = args

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // ── Shortcut 1: Cmd/Ctrl+H toggles the history popup.
      // Same guard the original useEffect used: never intercept while
      // typing into a text field.
      if ((e.metaKey || e.ctrlKey) && (e.key === 'h' || e.key === 'H')) {
        if (isInTextField(e.target)) return
        e.preventDefault()
        onToggleHistory()
        return
      }

      // ── Shortcut 2: Cmd/Ctrl+F opens the in-conversation search.
      // Same wrap-contains gate as the original useEffect — outside
      // the Claude tab, the browser's Find behaves normally.
      if ((e.metaKey || e.ctrlKey) && (e.key === 'f' || e.key === 'F')) {
        const wrap = wrapRef.current
        if (!wrap) return
        const active = document.activeElement
        const inside = wrap.contains(active) || active === document.body
        if (!inside) return
        e.preventDefault()
        onOpenSearch()
        return
      }

      // ── Shortcut 3: y / n / Escape respond to the pending permission.
      // Only when a permission is actually pending AND focus is not in
      // a text field (so the composer keeps swallowing letter keys).
      if (pendingPermission) {
        if (isInTextField(e.target)) return
        if (e.key === 'y' || e.key === 'Y') {
          e.preventDefault()
          send.permissionRespond(pendingPermission.permissionId, 'allow', 'once')
          return
        }
        if (e.key === 'n' || e.key === 'N' || e.key === 'Escape') {
          e.preventDefault()
          send.permissionRespond(pendingPermission.permissionId, 'deny', 'once')
          return
        }
      }
    }
    // Capture phase, matching the original Cmd+F handler so the search
    // shortcut wins over any nested handlers — and so the other
    // shortcuts share that priority.
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onToggleHistory, onOpenSearch, wrapRef, pendingPermission, send])
}
