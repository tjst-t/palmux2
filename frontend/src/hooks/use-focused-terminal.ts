import { useMemo } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'

import { selectBranchById, usePalmuxStore } from '../stores/palmux-store'
import { useTabSettingsStore } from '../stores/tab-settings-store'

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

  // The focused claude tab's mode. In 'tui' mode the tab renders an xterm
  // (claude-tui) which IS a real terminal the Toolbar can drive — even though
  // it has no tmux windowName. In 'agent' mode the tab is the chat UI, not a
  // terminal, so it stays termKey=null.
  const decodedTabId = target.tabId ? decodeURIComponent(target.tabId) : undefined
  const claudeMode = useTabSettingsStore((s) => {
    if (!target.repoId || !target.branchId || !decodedTabId) return undefined
    return s.settings[`${target.repoId}/${target.branchId}/${decodedTabId}`]?.claude_mode
  })

  return useMemo<FocusedTerminalInfo>(() => {
    if (!target.repoId || !target.branchId || !target.tabId || !branch) {
      return { termKey: null, ...target }
    }
    const tabId = decodeURIComponent(target.tabId)
    const tab = branch.tabSet.tabs.find((t) => t.id === tabId)
    if (!tab) return { termKey: null, ...target, tabId }
    // A terminal is drivable when it has a tmux window (Bash) OR it's a
    // claude tab running in TUI mode (claude-tui xterm, no tmux window).
    const isTuiClaude = tab.type === 'claude' && claudeMode === 'tui'
    if (!tab.windowName && !isTuiClaude) {
      return { termKey: null, ...target, tabId, tabType: tab.type }
    }
    return {
      termKey: `${target.repoId}/${target.branchId}/${tabId}`,
      repoId: target.repoId,
      branchId: target.branchId,
      tabId,
      tabType: tab.type,
    }
  }, [target.repoId, target.branchId, target.tabId, branch, claudeMode])
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
