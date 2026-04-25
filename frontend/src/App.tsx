import { useEffect, useState } from 'react'

import styles from './App.module.css'
import { api, type Repository, type Tab } from './lib/api'
import { TabContent } from './tabs/tab-content'

interface Selection {
  repoId: string
  branchId: string
  tab: Tab
}

function readURL(): Partial<{ repoId: string; branchId: string; tabId: string }> {
  const p = new URLSearchParams(window.location.search)
  return {
    repoId: p.get('repo') ?? undefined,
    branchId: p.get('branch') ?? undefined,
    tabId: p.get('tab') ?? undefined,
  }
}

function autoSelect(repos: Repository[]): Selection | null {
  for (const repo of repos) {
    for (const branch of repo.openBranches) {
      const tab = branch.tabSet.tabs.find((t) => !!t.windowName)
      if (tab) return { repoId: repo.id, branchId: branch.id, tab }
    }
  }
  return null
}

function App() {
  const [selection, setSelection] = useState<Selection | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [hint, setHint] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const repos = await api.get<Repository[]>('/api/repos')
        if (cancelled) return
        const url = readURL()
        if (url.repoId && url.branchId && url.tabId) {
          const repo = repos.find((r) => r.id === url.repoId)
          const branch = repo?.openBranches.find((b) => b.id === url.branchId)
          const tab = branch?.tabSet.tabs.find((t) => t.id === url.tabId)
          if (repo && branch && tab) {
            setSelection({ repoId: repo.id, branchId: branch.id, tab })
            return
          }
          setHint(`Couldn't find ?repo=${url.repoId}&branch=${url.branchId}&tab=${url.tabId} in /api/repos.`)
        }
        const auto = autoSelect(repos)
        if (auto) {
          setSelection(auto)
        } else {
          setHint('No open branches yet. Open a repo + branch via the API; Phase 3 adds the Drawer UI.')
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <span className={styles.brand}>Palmux v2</span>
        {selection ? (
          <span className={styles.phase}>
            {selection.repoId} / {selection.branchId} / <strong>{selection.tab.name}</strong>
          </span>
        ) : (
          <span className={styles.phase}>Phase 2 · Terminal Attach</span>
        )}
      </header>
      <main className={styles.main}>
        {selection ? (
          <TabContent tab={selection.tab} repoId={selection.repoId} branchId={selection.branchId} />
        ) : (
          <div style={{ padding: 24 }}>
            {error ? (
              <p style={{ color: 'var(--color-error)' }}>API error: {error}</p>
            ) : (
              <p className={styles.muted}>{hint ?? 'Loading…'}</p>
            )}
          </div>
        )}
      </main>
    </div>
  )
}

export default App
