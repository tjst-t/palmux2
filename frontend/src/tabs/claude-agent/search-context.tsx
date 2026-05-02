// Lightweight context for passing the active search query + opened-blocks
// set down to BlockView without prop-drilling through every component.
//
// Used only by S018 Cmd+F search. When no search is active the context
// returns an empty default — BlockView checks `query.length > 0` before
// running any highlight / expansion logic so the cost is zero off-path.

import { createContext, useContext } from 'react'

interface ClaudeSearchContextValue {
  /** Current case-insensitive query string. Empty when search is closed. */
  query: string
  /** Block ids that contain a match. Renderers force-expand these
   *  even if the user had them collapsed. */
  openedBlocks: Set<string>
  /** Block id of the currently-active match (the one that gets the
   *  bright highlight + scrolled into view). May be undefined when
   *  no matches or search is closed. */
  activeBlockId?: string
}

const ClaudeSearchContext = createContext<ClaudeSearchContextValue>({
  query: '',
  openedBlocks: new Set(),
})

export function ClaudeSearchProvider(
  props: ClaudeSearchContextValue & { children: React.ReactNode },
) {
  const { query, openedBlocks, activeBlockId, children } = props
  return (
    <ClaudeSearchContext.Provider value={{ query, openedBlocks, activeBlockId }}>
      {children}
    </ClaudeSearchContext.Provider>
  )
}

export function useClaudeSearch(): ClaudeSearchContextValue {
  return useContext(ClaudeSearchContext)
}
