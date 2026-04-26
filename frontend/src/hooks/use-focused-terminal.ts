import { useMemo } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'

import { selectBranchById, usePalmuxStore } from '../stores/palmux-store'

export interface FocusedTerminalInfo {
  /** TerminalManager key, or null when the focused tab is not a terminal. */
  termKey: string | null
  /** repoId / branchId / tabId for the focused tab. */
  repoId?: string
  branchId?: string
  tabId?: string
  /** Tab type, useful for picking a toolbar mode (claude vs normal). */
  tabType?: string
}

// useFocusedTerminal returns the active panel's tab + a TerminalManager key.
// Non-terminal tabs (Files / Git) yield termKey=null but still report the
// type so the toolbar can pick a sensible mode.
export function useFocusedTerminal(): FocusedTerminalInfo {
  const params = useParams()
  const [searchParams] = useSearchParams()
  const focusedPanel = usePalmuxStore((s) => s.focusedPanel)

  const right = parseRight(searchParams.get('right'))
  const target =
    focusedPanel === 'right'
      ? right
      : { repoId: params.repoId, branchId: params.branchId, tabId: params.tabId }

  const branch = usePalmuxStore((s) =>
    target.repoId && target.branchId
      ? selectBranchById(target.repoId, target.branchId)(s)
      : undefined,
  )

  return useMemo<FocusedTerminalInfo>(() => {
    if (!target.repoId || !target.branchId || !target.tabId || !branch) {
      return { termKey: null, ...target }
    }
    const tabId = decodeURIComponent(target.tabId)
    const tab = branch.tabSet.tabs.find((t) => t.id === tabId)
    if (!tab) return { termKey: null, ...target, tabId }
    if (!tab.windowName) {
      return { termKey: null, ...target, tabId, tabType: tab.type }
    }
    return {
      termKey: `${target.repoId}/${target.branchId}/${tabId}`,
      repoId: target.repoId,
      branchId: target.branchId,
      tabId,
      tabType: tab.type,
    }
  }, [target.repoId, target.branchId, target.tabId, branch])
}

function parseRight(raw: string | null) {
  if (!raw) return { repoId: undefined, branchId: undefined, tabId: undefined }
  const parts = raw.split('/').map(decodeURIComponent)
  return {
    repoId: parts[0] || undefined,
    branchId: parts[1] || undefined,
    tabId: parts[2] || undefined,
  }
}
