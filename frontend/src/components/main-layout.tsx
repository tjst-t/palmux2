import { useEffect, useRef } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { selectBranchById, usePalmuxStore } from '../stores/palmux-store'

import { Drawer } from './drawer'
import { Header } from './header'
import { IMEBar } from './ime-bar'
import { MainArea } from './main-area'
import { Toolbar } from './toolbar/toolbar'
import styles from './main-layout.module.css'

export function MainLayout() {
  const { repoId, branchId, tabId } = useParams()
  const navigate = useNavigate()
  const drawerPinned = usePalmuxStore((s) => s.deviceSettings.drawerPinned)
  const imeMode = usePalmuxStore((s) => s.deviceSettings.imeMode)
  const mobileDrawerOpen = usePalmuxStore((s) => s.mobileDrawerOpen)
  const setMobileDrawerOpen = usePalmuxStore((s) => s.setMobileDrawerOpen)
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const repos = usePalmuxStore((s) => s.repos)
  const bootstrapped = usePalmuxStore((s) => s.bootstrapped)
  const viewport = useViewport()
  const mobile = viewport === 'mobile'

  // If the URL points at something that doesn't exist (e.g. branch was
  // closed externally), bounce back to /.
  useEffect(() => {
    if (!bootstrapped || !repoId || !branchId) return
    if (!branch) {
      navigate('/', { replace: true })
    }
  }, [bootstrapped, repoId, branchId, branch, navigate])

  // Persist last-active so /redirect can pick it back up next visit.
  // Also store the per-branch last-active tab so the ⌘K @workspace switcher
  // can return the user to the tab they were on, not always claude.
  useEffect(() => {
    if (!repoId || !branchId || !tabId) return
    try {
      localStorage.setItem('palmux:lastActive', `${repoId}/${branchId}/${tabId}`)
      localStorage.setItem(`palmux:lastTab:${repoId}/${branchId}`, tabId)
    } catch {
      // ignore
    }
  }, [repoId, branchId, tabId])

  // Auto-close the mobile drawer when the viewport widens past mobile.
  useEffect(() => {
    if (!mobile && mobileDrawerOpen) setMobileDrawerOpen(false)
  }, [mobile, mobileDrawerOpen, setMobileDrawerOpen])

  const showInlineDrawer = !mobile && drawerPinned
  const showMobileDrawer = mobile && mobileDrawerOpen

  if (!repoId || !branchId) {
    return (
      <div className={styles.shell}>
        {showInlineDrawer && <Drawer />}
        <div className={styles.body}>
          <Header />
          <div className={styles.empty}>
            <p>{repos.length === 0 ? 'Open a repository to get started.' : 'Pick a branch from the drawer.'}</p>
          </div>
        </div>
        {showMobileDrawer && <MobileDrawerOverlay onClose={() => setMobileDrawerOpen(false)} />}
      </div>
    )
  }

  // Show toolbar for Bash and Claude tabs; hide for REST-only tabs (Files /
  // Git / Sprint). The Toolbar itself auto-switches between "normal" and
  // "claude" button sets based on which tab is focused.
  // IME bar is kept Bash-only since the Claude tab has its own Composer.
  //
  // Derive from the store's live tab list for correctness — React Router's
  // tabId param may be URL-encoded (e.g. "bash%3Abash") so we decode it
  // before lookup. We also fall back to a type-prefix match so the toolbar
  // appears immediately before the branch finishes loading.
  const decodedTabId = tabId ? decodeURIComponent(tabId) : undefined
  const activeTab = branch?.tabSet.tabs.find((t) => t.id === decodedTabId)
  const activeTabType = activeTab?.type ?? (
    decodedTabId?.startsWith('bash') ? 'bash' :
    decodedTabId === 'claude' || decodedTabId?.startsWith('claude:') ? 'claude' :
    undefined
  )
  const isBashTab = activeTabType === 'bash'
  const isClaudeTab = activeTabType === 'claude'
  const showToolbar = isBashTab || isClaudeTab

  return (
    <div className={styles.shell}>
      {showInlineDrawer && <Drawer />}
      <div className={styles.body}>
        <Header />
        <MainArea />
        {/* IME bar sits directly above the toolbar — both are bottom-anchored
            mobile/touch UX so they belong together. */}
        {isBashTab && imeMode !== 'none' && <IMEBar mode={imeMode} />}
        {showToolbar && <Toolbar />}
      </div>
      {showMobileDrawer && <MobileDrawerOverlay onClose={() => setMobileDrawerOpen(false)} />}
    </div>
  )
}

function MobileDrawerOverlay({ onClose }: { onClose: () => void }) {
  const location = useLocation()
  const initialKey = useRef(location.key)

  // Close when the user taps outside or hits Esc.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // Auto-close when the user navigates (e.g. picks a branch in the drawer).
  useEffect(() => {
    if (location.key !== initialKey.current) onClose()
  }, [location.key, onClose])

  return (
    <div className={styles.mobileDrawer} role="dialog" aria-modal="true">
      <div className={styles.mobileBackdrop} onClick={onClose} />
      <div className={styles.mobileDrawerInner}>
        <Drawer />
      </div>
    </div>
  )
}
