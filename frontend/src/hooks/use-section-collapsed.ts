import { useCallback, useState } from 'react'

/**
 * S015: localStorage-backed boolean toggle for Drawer section
 * collapsed-state. The key namespace is `palmux:drawer.section.<key>.collapsed`
 * — `<key>` is one of `my`, `unmanaged`, `subagent`. Value is `'true'`
 * or `'false'`. Missing key → use `defaultCollapsed`.
 *
 * The hook is intentionally tiny and synchronous so the initial render
 * already reflects the persisted state (no flash of expanded → collapsed).
 */
function readCollapsed(storageKey: string, defaultCollapsed: boolean): boolean {
  if (typeof localStorage === 'undefined') return defaultCollapsed
  const raw = localStorage.getItem(storageKey)
  if (raw === 'true') return true
  if (raw === 'false') return false
  return defaultCollapsed
}

export function useSectionCollapsed(
  sectionKey: string,
  defaultCollapsed: boolean,
): [boolean, (next: boolean) => void] {
  const storageKey = `palmux:drawer.section.${sectionKey}.collapsed`

  // S13b16a-4: store the storageKey alongside the value so a key change
  // triggers an inline reset during render — React 19's official
  // "deriving state from props" pattern, which avoids the cascading
  // useEffect → setState that the previous implementation relied on.
  const [state, setState] = useState<{ key: string; value: boolean }>(() => ({
    key: storageKey,
    value: readCollapsed(storageKey, defaultCollapsed),
  }))

  let collapsed = state.value
  if (state.key !== storageKey) {
    collapsed = readCollapsed(storageKey, defaultCollapsed)
    setState({ key: storageKey, value: collapsed })
  }

  const setCollapsed = useCallback(
    (next: boolean) => {
      try {
        localStorage.setItem(storageKey, next ? 'true' : 'false')
      } catch {
        // ignore — localStorage may be disabled in private browsing
      }
      setState({ key: storageKey, value: next })
    },
    [storageKey],
  )

  return [collapsed, setCollapsed]
}
