import { useCallback, useEffect, useState } from 'react'

/**
 * S015: localStorage-backed boolean toggle for Drawer section
 * collapsed-state. The key namespace is `palmux:drawer.section.<key>.collapsed`
 * — `<key>` is one of `my`, `unmanaged`, `subagent`. Value is `'true'`
 * or `'false'`. Missing key → use `defaultCollapsed`.
 *
 * The hook is intentionally tiny and synchronous so the initial render
 * already reflects the persisted state (no flash of expanded → collapsed).
 */
export function useSectionCollapsed(
  sectionKey: string,
  defaultCollapsed: boolean,
): [boolean, (next: boolean) => void] {
  const storageKey = `palmux:drawer.section.${sectionKey}.collapsed`

  const read = useCallback((): boolean => {
    if (typeof localStorage === 'undefined') return defaultCollapsed
    const raw = localStorage.getItem(storageKey)
    if (raw === 'true') return true
    if (raw === 'false') return false
    return defaultCollapsed
  }, [storageKey, defaultCollapsed])

  const [collapsed, setCollapsedState] = useState<boolean>(read)

  // Refresh state when the storageKey itself changes (e.g. mounting a
  // new section in a different repo). Cheap because the read is sync.
  useEffect(() => {
    setCollapsedState(read())
  }, [read])

  const setCollapsed = useCallback(
    (next: boolean) => {
      try {
        localStorage.setItem(storageKey, next ? 'true' : 'false')
      } catch {
        // ignore — localStorage may be disabled in private browsing
      }
      setCollapsedState(next)
    },
    [storageKey],
  )

  return [collapsed, setCollapsed]
}
