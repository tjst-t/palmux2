import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'

import { usePalmuxStore } from '../stores/palmux-store'

// Lands on `/`. Restores the last-active path from localStorage if it still
// exists; otherwise picks the first open branch's first terminal-backed tab;
// otherwise stays on `/` so the empty-state UI can show.
export function HomeRedirect() {
  const navigate = useNavigate()
  const repos = usePalmuxStore((s) => s.repos)
  const bootstrapped = usePalmuxStore((s) => s.bootstrapped)

  useEffect(() => {
    if (!bootstrapped) return
    const last = readLast()
    if (last) {
      const [repoId, branchId, tabId] = last.split('/')
      const repo = repos.find((r) => r.id === repoId)
      const branch = repo?.openBranches.find((b) => b.id === branchId)
      const tab = branch?.tabSet.tabs.find((t) => t.id === decodeURIComponent(tabId))
      if (tab) {
        navigate(`/${repoId}/${branchId}/${tabId}`, { replace: true })
        return
      }
    }
    for (const repo of repos) {
      for (const branch of repo.openBranches) {
        const tab = branch.tabSet.tabs[0]
        if (tab) {
          navigate(`/${repo.id}/${branch.id}/${encodeURIComponent(tab.id)}`, { replace: true })
          return
        }
      }
    }
  }, [bootstrapped, repos, navigate])

  return null
}

function readLast(): string | null {
  try {
    return localStorage.getItem('palmux:lastActive')
  } catch {
    return null
  }
}
