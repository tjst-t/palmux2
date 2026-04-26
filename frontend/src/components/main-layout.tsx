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
  useEffect(() => {
    if (!repoId || !branchId || !tabId) return
    try {
      localStorage.setItem('palmux:lastActive', `${repoId}/${branchId}/${tabId}`)
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

  return (
    <div className={styles.shell}>
      {showInlineDrawer && <Drawer />}
      <div className={styles.body}>
        <Header />
        {imeMode !== 'none' && <IMEBar mode={imeMode} />}
        <MainArea />
        <Toolbar />
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
