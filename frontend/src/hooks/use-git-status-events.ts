// useGitStatusEvents — subscribes to the global event WebSocket via a
// lightweight DOM event bus and fires `onChange` whenever a
// `git.statusChanged` event arrives for the given (repoId, branchId).
//
// We don't open a second WS connection — `useEventStream()` (mounted at
// the App root) re-broadcasts every server frame as a `palmux:event`
// CustomEvent so any component can opt-in to specific event types
// without coupling to the central Zustand reducer.

import { useEffect } from 'react'

import type { RemoteEvent } from '../stores/palmux-store'

export const PALMUX_EVENT = 'palmux:event'

export function useGitStatusEvents(repoId: string, branchId: string, onChange: () => void) {
  useEffect(() => {
    const handler = (e: Event) => {
      const ev = (e as CustomEvent<RemoteEvent>).detail
      if (!ev) return
      if (ev.type !== 'git.statusChanged') return
      if (ev.repoId !== repoId || ev.branchId !== branchId) return
      onChange()
    }
    window.addEventListener(PALMUX_EVENT, handler)
    return () => window.removeEventListener(PALMUX_EVENT, handler)
  }, [repoId, branchId, onChange])
}
