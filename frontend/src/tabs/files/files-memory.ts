// Per-(repoId, branchId) Files-tab location memory. The Files tab's primary
// state (current dir, selected file, jump-to-line) is URL-encoded, but the
// TabBar's `goToTab` only navigates to `/{repoId}/{branchId}/{tabId}` — it
// drops the splat. So switching to Bash and back loses the path. We mirror
// the latest URL into localStorage and restore it on remount when the
// router lands on the bare `/files` route with no splat.
//
// We also persist Monaco's last-known cursor line independently of the URL
// so the user's scroll position survives even when they didn't arrive via
// an explicit `?line=N` jump (e.g. they scrolled freely while reading).
//
// Storage key: `palmux:filesState:{repoId}/{branchId}` per the convention
// in CLAUDE.md.

const KEY_PREFIX = 'palmux:filesState:'

export interface FilesMemory {
  // Full URL fragment relative to the SPA — pathname + search. Restoring
  // this verbatim through `navigate(..., { replace: true })` is enough to
  // bring back the dir / selected file / explicit jump-line.
  pathname: string
  search: string
  // Monaco's last-known 1-based cursor line, tracked separately so the
  // address bar doesn't jitter on every keystroke. Layered on top of the
  // saved URL by injecting `?line=N` at restore time when no explicit
  // line query is already present.
  cursorLine?: number
}

function storageKey(repoId: string, branchId: string): string {
  return `${KEY_PREFIX}${repoId}/${branchId}`
}

export function readFilesMemory(repoId: string, branchId: string): FilesMemory | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(storageKey(repoId, branchId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as FilesMemory
    // Defensive: a corrupted blob shouldn't crash the Files tab on mount.
    if (typeof parsed.pathname !== 'string' || typeof parsed.search !== 'string') {
      return null
    }
    return parsed
  } catch {
    return null
  }
}

export function writeFilesMemory(repoId: string, branchId: string, mem: FilesMemory): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(storageKey(repoId, branchId), JSON.stringify(mem))
  } catch {
    // QuotaExceeded / Safari private-mode etc. — silently drop.
  }
}

// Build the URL fragment to restore from. We prefer the tracked cursor
// line over any `?line=N` baked into the saved search — the saved search
// is whatever was in the URL when the user last landed on the file (often
// from grep / search / a deep link), but the cursor line tracks where
// they actually are *now*. Falls back to whichever exists.
export function buildRestoreUrl(mem: FilesMemory): string {
  const sp = new URLSearchParams(mem.search.startsWith('?') ? mem.search.slice(1) : mem.search)
  if (mem.cursorLine && mem.cursorLine > 1) {
    sp.set('line', String(mem.cursorLine))
  }
  // If cursorLine is unset or 1 (top of file) AND the saved search has no
  // line, drop any inherited `line=` to keep the URL clean.
  if ((!mem.cursorLine || mem.cursorLine <= 1) && !sp.has('line')) {
    sp.delete('line')
  }
  const search = sp.toString()
  return mem.pathname + (search ? '?' + search : '')
}
