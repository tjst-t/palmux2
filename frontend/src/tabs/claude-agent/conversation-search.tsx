// Conversation in-tab search (S018).
//
// Cmd+F / Ctrl+F inside the Claude tab opens a search bar that matches
// against the textual content of every block in `state.turns`. Matches
// are highlighted via a regex split inside a `MatchHighlight` helper
// (callers wrap their text content in this when `searchQuery` is
// non-empty).
//
// Navigation: Enter / Shift+Enter cycles next/prev; the active match
// triggers a scroll-to-row on the virtualised list (S017) so the row
// is centred. Folded blocks that contain a match are auto-expanded by
// the renderer when their `id` is in the `searchOpenedBlocks` set.
//
// The implementation lives in this module to keep the existing
// `claude-agent-view.tsx` / `blocks.tsx` files focused on rendering,
// even though the search hook is consumed there.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import type { Block, Turn } from './types'

/** One matchable text span in the conversation. */
export interface SearchHit {
  /** Index into the (top-level) turns list — same indexing the
   *  virtualised List uses, so scrollToRow takes this directly. */
  turnIndex: number
  /** Block id within the turn. Used to populate the auto-expand set. */
  blockId: string
  /** The raw text the hit occurs in (untruncated). Useful for
   *  diagnostics; not rendered. */
  text: string
}

/** Build a flat searchable index over the visible (top-level) turns.
 *  Keeping it flat rather than nested mirrors how the virtualised List
 *  scrolls — every match maps directly to a single turnIndex.
 *
 *  Sub-agent (Task) child turns are intentionally NOT indexed here.
 *  Searching inside them would require rendering them outside their
 *  parent Task tree which fights the existing UX; deferring this to
 *  a future sprint as a backlog item.
 */
export function buildSearchIndex(turns: Turn[]): SearchEntry[] {
  const entries: SearchEntry[] = []
  for (let i = 0; i < turns.length; i++) {
    const t = turns[i]
    for (const b of t.blocks) {
      const text = blockSearchText(b)
      if (!text) continue
      entries.push({ turnIndex: i, blockId: b.id, text })
    }
  }
  return entries
}

interface SearchEntry {
  turnIndex: number
  blockId: string
  text: string
}

/** Extract the searchable text for a block. We synthesise a single
 *  string so a regex match counts whether it's in input or output. */
function blockSearchText(b: Block): string {
  // Each kind contributes whatever it can plausibly render textually.
  // Code-block bodies (tool input as JSON) are flattened to a string so
  // queries like "ReadFile" match the tool name and "src/foo.go" matches
  // an Edit's path argument.
  const parts: string[] = []
  if (b.text) parts.push(b.text)
  if (b.output) parts.push(b.output)
  if (b.name) parts.push(b.name)
  if (b.toolName) parts.push(b.toolName)
  if (b.input != null) {
    try {
      parts.push(JSON.stringify(b.input))
    } catch {
      // ignore non-serialisable
    }
  }
  if (b.hookName) parts.push(b.hookName)
  if (b.hookEvent) parts.push(b.hookEvent)
  if (b.hookOutput) parts.push(b.hookOutput)
  if (b.hookStdout) parts.push(b.hookStdout)
  if (b.hookStderr) parts.push(b.hookStderr)
  return parts.join('\n')
}

/** Run the active query against the index. Case-insensitive substring
 *  match — "regex" / "whole word" / "case sensitive" are deferred to a
 *  future sprint. Returns the list of matching entries (NOT collapsed
 *  per-block, so a single block carrying multiple occurrences only
 *  counts once — feels right for "next match" navigation since each
 *  Enter advances to a different block, not a different word in the
 *  same block).
 */
export function runSearch(index: SearchEntry[], query: string): SearchHit[] {
  if (!query) return []
  const needle = query.toLowerCase()
  const out: SearchHit[] = []
  for (const e of index) {
    if (e.text.toLowerCase().includes(needle)) {
      out.push({ turnIndex: e.turnIndex, blockId: e.blockId, text: e.text })
    }
  }
  return out
}

/** Public state surface for the search bar. */
export interface SearchState {
  open: boolean
  query: string
  matches: SearchHit[]
  /** 0-based index into matches[] for the "current" hit. -1 if no
   *  matches. */
  active: number
  /** Set of block ids the renderer should force-expand because they
   *  carry a match. Cleared when the bar closes. */
  openedBlocks: Set<string>
}

/** Result of useConversationSearch — the pieces the parent view needs
 *  to render the bar, hand the highlight regex down to renderers, and
 *  drive the virtualised List's scroll. */
export interface UseConversationSearchResult {
  state: SearchState
  setQuery: (q: string) => void
  next: () => void
  prev: () => void
  /** Open the bar and focus the input. The caller is the keyboard
   *  shortcut handler in claude-agent-view. */
  open: () => void
  /** Close, clear query, drop expanded set. Re-set focus to the
   *  conversation so subsequent keystrokes don't drop into a stale
   *  input. */
  close: () => void
  /** Bind to the search input ref so `open()` can focus it. */
  inputRef: React.RefObject<HTMLInputElement | null>
}

/** useConversationSearch wires the search bar lifecycle to the
 *  conversation list. Re-runs the query whenever turns or the query
 *  string change.
 */
export function useConversationSearch(
  turns: Turn[],
  scrollToTurn: (index: number) => void,
): UseConversationSearchResult {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement | null>(null)

  const index = useMemo(() => buildSearchIndex(turns), [turns])
  const matches = useMemo(() => runSearch(index, query), [index, query])

  // The opened-blocks set is recomputed each query change. Using a
  // memoised Set lets BlockView do `openedBlocks.has(block.id)` for
  // an O(1) auto-expand check.
  const openedBlocks = useMemo(() => {
    const s = new Set<string>()
    for (const m of matches) s.add(m.blockId)
    return s
  }, [matches])

  // Reset the active index when the result set changes, so navigation
  // always starts at match 0 after the user types.
  useEffect(() => {
    setActive(matches.length > 0 ? 0 : -1)
  }, [matches])

  // When `active` changes, scroll the corresponding row into view.
  useEffect(() => {
    if (!open) return
    if (active < 0) return
    const m = matches[active]
    if (!m) return
    scrollToTurn(m.turnIndex)
  }, [active, matches, open, scrollToTurn])

  const next = useCallback(() => {
    if (matches.length === 0) return
    setActive((cur) => (cur + 1) % matches.length)
  }, [matches])
  const prev = useCallback(() => {
    if (matches.length === 0) return
    setActive((cur) => (cur - 1 + matches.length) % matches.length)
  }, [matches])

  const openBar = useCallback(() => {
    setOpen(true)
    // rAF so the input has mounted before we focus.
    requestAnimationFrame(() => {
      inputRef.current?.focus()
      inputRef.current?.select()
    })
  }, [])
  const close = useCallback(() => {
    setOpen(false)
    setQuery('')
    setActive(-1)
  }, [])

  const state: SearchState = { open, query, matches, active, openedBlocks }
  return { state, setQuery, next, prev, open: openBar, close, inputRef }
}

interface ConversationSearchBarProps {
  state: SearchState
  setQuery: (q: string) => void
  onNext: () => void
  onPrev: () => void
  onClose: () => void
  inputRef: React.RefObject<HTMLInputElement | null>
}

/** The visible search input + "3 of 12 < >" navigation. */
export function ConversationSearchBar({
  state,
  setQuery,
  onNext,
  onPrev,
  onClose,
  inputRef,
}: ConversationSearchBarProps) {
  if (!state.open) return null
  const total = state.matches.length
  const current = total > 0 ? state.active + 1 : 0
  return (
    <div className="palmux-conv-search" role="search" data-testid="conversation-search">
      <input
        ref={inputRef}
        type="search"
        value={state.query}
        placeholder="Find in conversation"
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault()
            onClose()
          } else if (e.key === 'Enter') {
            e.preventDefault()
            if (e.shiftKey) onPrev()
            else onNext()
          }
        }}
        aria-label="Search conversation"
        data-testid="conversation-search-input"
      />
      <span
        className="palmux-conv-search-count"
        data-testid="conversation-search-count"
      >
        {state.query ? (total > 0 ? `${current}/${total}` : '0/0') : ''}
      </span>
      <button
        type="button"
        onClick={onPrev}
        disabled={total === 0}
        title="Previous match (Shift+Enter)"
        aria-label="Previous match"
        data-testid="conversation-search-prev"
      >
        ‹
      </button>
      <button
        type="button"
        onClick={onNext}
        disabled={total === 0}
        title="Next match (Enter)"
        aria-label="Next match"
        data-testid="conversation-search-next"
      >
        ›
      </button>
      <button
        type="button"
        onClick={onClose}
        title="Close (Esc)"
        aria-label="Close search"
        data-testid="conversation-search-close"
      >
        ×
      </button>
    </div>
  )
}

/** Compute a plain-text-with-marks splitter for highlighting. Given
 *  `text` and a (case-insensitive) `query`, returns alternating string
 *  / hit segments. Used by both the BlockView code path (so the user
 *  visually sees the matched substring) and any future "preview" UI.
 *  Returns the original string unchanged when `query` is empty. */
export function splitForHighlight(
  text: string,
  query: string,
): { segment: string; hit: boolean }[] {
  if (!query || !text) return [{ segment: text, hit: false }]
  const out: { segment: string; hit: boolean }[] = []
  const lower = text.toLowerCase()
  const needle = query.toLowerCase()
  let i = 0
  while (i < text.length) {
    const next = lower.indexOf(needle, i)
    if (next < 0) {
      out.push({ segment: text.slice(i), hit: false })
      break
    }
    if (next > i) out.push({ segment: text.slice(i, next), hit: false })
    out.push({ segment: text.slice(next, next + needle.length), hit: true })
    i = next + needle.length
  }
  return out
}
