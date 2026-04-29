// Cross-tab navigation helpers. Anything that wants to deep-link from one
// tab into another (e.g. "Open in Files tab" from the Claude tool block)
// goes through this module so the URL scheme is centralised.
//
// URL convention: /{repoId}/{branchId}/{tabType}/{...rest}
//                 + ?right=... preserved by the caller if needed.

export function urlForTab(repoId: string, branchId: string, tabId: string): string {
  return `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(tabId)}`
}

export function urlForFiles(repoId: string, branchId: string, path?: string): string {
  const base = urlForTab(repoId, branchId, 'files')
  if (!path) return base
  // Files routing reads the path from the URL after the tab segment.
  return `${base}/${path.replace(/^\/+/, '').split('/').map(encodeURIComponent).join('/')}`
}

// relativeToWorktree converts an absolute filesystem path that lives
// inside the branch's worktree into a worktree-relative path. Tool
// inputs from Claude (Read, Edit, etc.) carry absolute paths; the
// Files tab keys on relative paths, so we have to strip the prefix
// before navigating. Returns the original input unchanged if it isn't
// under worktreePath, since the user almost certainly didn't mean to
// open something outside the workspace.
export function relativeToWorktree(absPath: string, worktreePath?: string): string {
  if (!absPath) return absPath
  if (!worktreePath) return absPath
  const a = absPath.replace(/\/+$/, '')
  const w = worktreePath.replace(/\/+$/, '')
  if (a === w) return ''
  if (a.startsWith(w + '/')) return a.slice(w.length + 1)
  return absPath
}

export function urlForGit(
  repoId: string,
  branchId: string,
  view?: 'status' | 'diff' | 'log' | 'branches',
): string {
  const base = urlForTab(repoId, branchId, 'git')
  return view ? `${base}/${view}` : base
}

export function urlForClaude(repoId: string, branchId: string): string {
  return urlForTab(repoId, branchId, 'claude')
}

export function urlForBash(repoId: string, branchId: string, name = 'bash'): string {
  return urlForTab(repoId, branchId, name === 'bash' ? 'bash:bash' : `bash:${name}`)
}
