/**
 * bash-target.ts — S031-2
 *
 * Resolves the Bash tab that a command should be sent to.
 *
 * Resolution order:
 *   1. Explicit targetTabId passed by the caller (e.g. bash picker)
 *   2. MRU tab from localStorage `palmux:lastBashTab:{repoId}/{branchId}`
 *   3. First bash tab in the tabSet
 *   4. Auto-create a new bash tab if none exist
 *
 * Special value `__new__` for targetTabId forces creation of a new Bash tab.
 */

// TermKey is the key passed to terminalManager.sendInput / focus.
export interface BashTarget {
  /** Tab ID (domain ID — used for navigation). */
  tabId: string
  /** Term key used by terminalManager. */
  termKey: string
}

type Tab = { id: string; type: string; name: string }

/** Storage key for the MRU Bash tab id. */
export function mruBashKey(repoId: string, branchId: string): string {
  return `palmux:lastBashTab:${repoId}/${branchId}`
}

/** Update the MRU Bash tab cache. Called by terminal-manager on pty input. */
export function updateMruBashTab(repoId: string, branchId: string, tabId: string): void {
  try {
    localStorage.setItem(mruBashKey(repoId, branchId), tabId)
  } catch {
    // localStorage may be unavailable in tests
  }
}

/**
 * Resolve the target Bash tab and return a BashTarget, or null if resolution
 * fails entirely (very unlikely — we auto-create when needed).
 *
 * @param repoId     - active repo id
 * @param branchId   - active branch id
 * @param tabs       - current tabSet.tabs snapshot from the store
 * @param addTab     - store.addTab function (used for auto-create)
 * @param targetTabId - optional explicit tab id ('__new__' forces creation)
 */
export async function resolveBashTarget(
  repoId: string,
  branchId: string,
  tabs: Tab[],
  addTab: (repoId: string, branchId: string, type: string, name?: string) => Promise<Tab>,
  targetTabId?: string,
): Promise<BashTarget | null> {
  // Force creation of a new tab
  if (targetTabId === '__new__') {
    const newTab = await addTab(repoId, branchId, 'bash')
    return { tabId: newTab.id, termKey: termKeyFor(repoId, branchId, newTab.id) }
  }

  // Explicit tab id
  if (targetTabId) {
    const tab = tabs.find((t) => t.id === targetTabId && t.type === 'bash')
    if (tab) return { tabId: tab.id, termKey: termKeyFor(repoId, branchId, tab.id) }
  }

  // MRU from localStorage
  try {
    const stored = localStorage.getItem(mruBashKey(repoId, branchId))
    if (stored) {
      const tab = tabs.find((t) => t.id === stored && t.type === 'bash')
      if (tab) return { tabId: tab.id, termKey: termKeyFor(repoId, branchId, tab.id) }
    }
  } catch {
    // ignore
  }

  // First bash tab
  const firstBash = tabs.find((t) => t.type === 'bash')
  if (firstBash) {
    return { tabId: firstBash.id, termKey: termKeyFor(repoId, branchId, firstBash.id) }
  }

  // Auto-create
  try {
    const newTab = await addTab(repoId, branchId, 'bash')
    // Re-fetch tabs to get the new tab — addTab calls reloadRepos internally
    return { tabId: newTab.id, termKey: termKeyFor(repoId, branchId, newTab.id) }
  } catch {
    return null
  }
}

/** Build the terminal manager key for a tab. */
function termKeyFor(repoId: string, branchId: string, tabId: string): string {
  return `${repoId}/${branchId}/${tabId}`
}

