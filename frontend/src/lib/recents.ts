/**
 * recents.ts — S031-4
 *
 * Persist recently-selected palette items in localStorage under
 * `palmux:recents`. Items are stored as a JSON array, capped at 50.
 * Newest item is always at index 0 (push-to-front).
 *
 * Item kinds: 'workspace' | 'tab' | 'file'
 */

const LS_KEY = 'palmux:recents'
const CAP = 50

export interface RecentItem {
  kind: 'workspace' | 'tab' | 'file'
  /** Unique deduplication key (e.g. `repoId/branchId` or file path). */
  key: string
  /** Human-readable label shown in the palette. */
  label: string
  /** Navigation URL to open when selected. */
  url?: string
  /** Timestamp (ms since epoch) when last selected. */
  ts: number
}

function load(): RecentItem[] {
  if (typeof localStorage === 'undefined') return []
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed as RecentItem[]
  } catch {
    return []
  }
}

function save(items: RecentItem[]): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(items))
  } catch {
    // ignore quota errors
  }
}

/** Push a recently-selected item to the front of the list. */
export function pushRecent(item: Omit<RecentItem, 'ts'>): void {
  const current = load()
  // Remove any existing entry with the same key
  const filtered = current.filter((r) => r.key !== item.key)
  // Prepend new entry
  const updated: RecentItem[] = [{ ...item, ts: Date.now() }, ...filtered].slice(0, CAP)
  save(updated)
}

/** Read the current list (newest first). */
export function listRecents(): RecentItem[] {
  return load()
}

/** Clear all recents (e.g. for testing). */
export function clearRecents(): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.removeItem(LS_KEY)
  } catch {
    // ignore
  }
}
