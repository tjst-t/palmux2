import { useEffect, useRef } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { HOST_REPO_ID, selectBranchById, usePalmuxStore } from '../stores/palmux-store'

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
  const reloadRepo = usePalmuxStore((s) => s.reloadRepo)
  const viewport = useViewport()
  const mobile = viewport === 'mobile'

  // If the URL points at something that doesn't exist (e.g. branch was
  // closed externally), bounce back to /.
  // Only redirect when the REPO is also present in the store — if the repo
  // isn't there yet (e.g. being loaded asynchronously by reloadRepo), we
  // wait rather than bouncing.
  useEffect(() => {
    if (!bootstrapped || !repoId || !branchId) return
    const repoKnown = repos.some((r) => r.id === repoId)
    if (repoKnown && !branch) {
      navigate('/', { replace: true })
    }
  }, [bootstrapped, repos, repoId, branchId, branch, navigate])

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

  // S8478ca-5: Refresh runtime views when navigating to a new repo.
  // Runs after bootstrap so it doesn't get overwritten by the bulk repos load.
  // Skips the synthetic host scope (no runtime endpoint there).
  useEffect(() => {
    if (!bootstrapped || !repoId || repoId === HOST_REPO_ID) return
    void reloadRepo(repoId).catch(() => {/* ignore — background refresh */})
  }, [bootstrapped, repoId, reloadRepo])

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
            {repos.length === 0 ? <SetupEmptyState /> : <p>Pick a branch from the drawer.</p>}
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

// SetupEmptyState (S0c6a1b) is shown when no repository is open yet — the
// first thing a fresh install sees. It points the user at a Host terminal so
// they can authenticate the CLIs (gh / claude) before opening any repo.
function SetupEmptyState() {
  const navigate = useNavigate()
  const hostRepo = usePalmuxStore((s) => s.hostRepo)
  const branchId = hostRepo?.openBranches[0]?.id ?? 'host'
  const tabId = hostRepo?.openBranches[0]?.tabSet.tabs[0]?.id ?? 'bash:bash'
  const repoId = hostRepo?.id ?? HOST_REPO_ID
  const openHost = () =>
    navigate(`/${repoId}/${branchId}/${encodeURIComponent(tabId)}`)

  return (
    <div className={styles.setup}>
      <p className={styles.setupLead}>Open a repository to get started.</p>
      <p className={styles.setupSub}>
        First time here? Authenticate the CLIs from a host terminal first — no repository needed.
      </p>
      <button
        type="button"
        className={styles.setupCta}
        data-testid="empty-setup-cta"
        onClick={openHost}
      >
        🖥 Open setup terminal
      </button>
      <ul className={styles.setupHints}>
        <li>
          <code data-testid="empty-setup-hint-gh">gh auth login</code>
        </li>
        <li>
          <code data-testid="empty-setup-hint-claude">claude  (then /login)</code>
        </li>
      </ul>
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
