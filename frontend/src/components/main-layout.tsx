import { useEffect } from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import { selectBranchById, usePalmuxStore } from '../stores/palmux-store'
import { TabContent } from '../tabs/tab-content'

import { Drawer } from './drawer'
import { Header } from './header'
import { TabBar } from './tab-bar'
import styles from './main-layout.module.css'

export function MainLayout() {
  const { repoId, branchId, tabId } = useParams()
  const navigate = useNavigate()
  const drawerPinned = usePalmuxStore((s) => s.deviceSettings.drawerPinned)
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const repos = usePalmuxStore((s) => s.repos)
  const bootstrapped = usePalmuxStore((s) => s.bootstrapped)

  // If the URL points at something that doesn't exist (e.g. branch was
  // closed externally), bounce back to /.
  useEffect(() => {
    if (!bootstrapped || !repoId || !branchId) return
    if (!branch) {
      navigate('/', { replace: true })
    }
  }, [bootstrapped, repoId, branchId, branch, navigate])

  // Persist last-active so /redirect can pick it back up next visit.
  useEffect(() => {
    if (!repoId || !branchId || !tabId) return
    try {
      localStorage.setItem('palmux:lastActive', `${repoId}/${branchId}/${tabId}`)
    } catch {
      // ignore
    }
  }, [repoId, branchId, tabId])

  const decodedTabId = tabId ? decodeURIComponent(tabId) : undefined
  const activeTab = branch?.tabSet.tabs.find((t) => t.id === decodedTabId)

  if (!repoId || !branchId) {
    return (
      <div className={styles.shell}>
        {drawerPinned && <Drawer />}
        <div className={styles.body}>
          <Header />
          <div className={styles.empty}>
            <p>{repos.length === 0 ? 'Open a repository to get started.' : 'Pick a branch from the drawer.'}</p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className={styles.shell}>
      {drawerPinned && <Drawer />}
      <div className={styles.body}>
        <Header />
        {branch && <TabBar branch={branch} />}
        <main className={styles.main}>
          {branch && activeTab && (
            <TabContent tab={activeTab} repoId={repoId} branchId={branchId} />
          )}
          {branch && !activeTab && (
            <div className={styles.empty}>
              <p>Tab not found. Pick another from the bar above.</p>
            </div>
          )}
        </main>
      </div>
    </div>
  )
}
