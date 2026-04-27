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
