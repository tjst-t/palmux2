import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { HOST_REPO_ID, selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'

import { useCommandPaletteStore } from './command-palette/store'
import { ActivityInbox } from './inbox/activity-inbox'
import { RuntimeChangeConfirm } from './runtime-change-confirm'
import { UpdateContainerConfirm } from './update-container-confirm'
import { UploadsIndicator } from './uploads/uploads-indicator'
import styles from './header.module.css'

const SPLIT_MIN_WIDTH = 900

export function Header() {
  const { repoId, branchId } = useParams()
  const repo = usePalmuxStore((s) => (repoId ? selectRepoById(repoId)(s) : undefined))
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const status = usePalmuxStore((s) => s.connectionStatus)
  const drawerPinned = usePalmuxStore((s) => s.deviceSettings.drawerPinned)
  const splitEnabled = usePalmuxStore((s) => s.deviceSettings.splitEnabled)
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const mobileDrawerOpen = usePalmuxStore((s) => s.mobileDrawerOpen)
  const setMobileDrawerOpen = usePalmuxStore((s) => s.setMobileDrawerOpen)
  const runtimeCaps = usePalmuxStore((s) => s.runtimeCaps)
  const loadRuntimeCaps = usePalmuxStore((s) => s.loadRuntimeCaps)
  const patchWorkspaceRuntime = usePalmuxStore((s) => s.patchWorkspaceRuntime)
  const regenerateContainer = usePalmuxStore((s) => s.regenerateContainer)
  const showPalette = useCommandPaletteStore((s) => s.show)
  const wide = useWideViewport(SPLIT_MIN_WIDTH)
  const viewport = useViewport()
  const mobile = viewport === 'mobile'

  // S8478ca-refine: runtime chip menu state
  const [chipMenuOpen, setChipMenuOpen] = useState(false)
  const [confirmKind, setConfirmKind] = useState<'host' | 'incus-container' | null>(null)
  const [runtimeError, setRuntimeError] = useState<string | null>(null)
  const [runtimePending, setRuntimePending] = useState(false)
  // S7364e3: container image-update (regenerate) confirm + progress state.
  const [updateConfirmOpen, setUpdateConfirmOpen] = useState(false)
  const [updatePending, setUpdatePending] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const chipRef = useRef<HTMLButtonElement>(null)

  // S7364e3: an incus container older than the current palmux-ws image.
  const updateAvailable =
    branch?.runtime?.kind === 'incus-container' && !!branch?.runtime?.stale

  // Close the menu when clicking outside
  useEffect(() => {
    if (!chipMenuOpen) return
    const handler = (e: MouseEvent) => {
      if (chipRef.current && !chipRef.current.closest('[data-testid="runtime-chip-anchor"]')?.contains(e.target as Node)) {
        setChipMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [chipMenuOpen])

  const onToggleDrawer = () => {
    if (mobile) {
      setMobileDrawerOpen(!mobileDrawerOpen)
    } else {
      setDeviceSetting('drawerPinned', !drawerPinned)
    }
  }

  const handleChipClick = () => {
    if (!repoId || !branchId) return
    // Load caps lazily on first open
    void loadRuntimeCaps().catch(() => {})
    setChipMenuOpen((o) => !o)
    setRuntimeError(null)
  }

  const handleMenuSelect = (kind: 'host' | 'incus-container') => {
    setChipMenuOpen(false)
    if (branch?.runtime?.kind === kind) return // same kind — no-op
    setConfirmKind(kind)
  }

  const handleConfirm = async () => {
    const kind = confirmKind
    setConfirmKind(null)
    if (!kind || !repoId || !branchId) return
    setRuntimePending(true)
    setRuntimeError(null)
    try {
      await patchWorkspaceRuntime(repoId, branchId, kind)
    } catch (err) {
      setRuntimeError(err instanceof Error ? err.message : String(err))
      // Re-open the chip menu so the inline error is visible to the user.
      setChipMenuOpen(true)
    } finally {
      setRuntimePending(false)
    }
  }

  const handleUpdateClick = () => {
    setChipMenuOpen(false)
    setUpdateError(null)
    setUpdateConfirmOpen(true)
  }

  const handleUpdateConfirm = async () => {
    setUpdateConfirmOpen(false)
    if (!repoId || !branchId) return
    setUpdatePending(true)
    setUpdateError(null)
    try {
      await regenerateContainer(repoId, branchId)
    } catch (err) {
      setUpdateError(err instanceof Error ? err.message : String(err))
      setChipMenuOpen(true) // re-open the menu so the inline error is visible
    } finally {
      setUpdatePending(false)
    }
  }

  const incusEntry = runtimeCaps?.kinds.find((k) => k.kind === 'incus-container')
  const incusAvailable = incusEntry?.available ?? false
  const incusReason = incusEntry?.reason ?? 'Incus is not installed on this host'

  return (
    <>
      {/* S8478ca-refine: confirm modal for runtime change from header chip */}
      {confirmKind && (
        <RuntimeChangeConfirm
          newKind={confirmKind}
          onConfirm={() => void handleConfirm()}
          onCancel={() => setConfirmKind(null)}
        />
      )}
      {/* S7364e3: confirm modal for container image update (regenerate) */}
      {updateConfirmOpen && (
        <UpdateContainerConfirm
          onConfirm={() => void handleUpdateConfirm()}
          onCancel={() => setUpdateConfirmOpen(false)}
        />
      )}
    <header className={styles.header}>
      <div className={styles.left}>
        <button
          className={styles.iconBtn}
          onClick={onToggleDrawer}
          title="Toggle drawer"
          aria-label="Toggle drawer"
        >
          ☰
        </button>
        <span className={styles.brand}>Palmux v2</span>
        {/* S8478ca-5: Host login scope (host--0000) shows a distinct label
            and NO runtime chip. Regular repos show branch name + runtime chip. */}
        {repoId === HOST_REPO_ID ? (
          <span
            className={styles.hostScopeLabel}
            data-testid="host-scope-label"
            title="Repo-independent login terminal (gh auth / claude login)"
          >
            ⌂ Host · login terminal
          </span>
        ) : (
          <>
            {branch && repo && (
              <span className={styles.branch}>
                <span className={styles.repoName}>{repoLabel(repo.ghqPath)}</span>
                <span className={styles.sep}>/</span>
                <span className={styles.branchName}>{branch.name}</span>
              </span>
            )}
            {branch?.runtime && (
              <span
                className={styles.runtimeChipAnchor}
                data-testid="runtime-chip-anchor"
              >
                <button
                  ref={chipRef}
                  className={`${styles.runtimeChip} ${styles.runtimeChipClickable}`}
                  data-testid="runtime-chip"
                  data-runtime-state={runtimePending || updatePending ? 'starting' : branch.runtime.state}
                  data-update-available={updateAvailable ? 'true' : undefined}
                  title={updatePending
                    ? 'Updating container to the new image…'
                    : runtimePending
                      ? 'Restarting workspace in new runtime…'
                      : `Runtime: ${branch.runtime.kind} · ${branch.runtime.state}${branch.runtime.address ? ` (${branch.runtime.address})` : ''}${branch.runtime.error ? ` — ${branch.runtime.error}` : ''}${updateAvailable ? ' — update available' : ''} — click to change`}
                  onClick={handleChipClick}
                  disabled={runtimePending || updatePending}
                  aria-haspopup="true"
                  aria-expanded={chipMenuOpen}
                >
                  <span className={styles.rtDot} />
                  {branch.runtime.kind} ·{' '}
                  {updatePending ? 'updating…' : runtimePending ? 'restarting…' : branch.runtime.state}
                  {updateAvailable && !updatePending && (
                    <span
                      className={styles.runtimeUpdateBadge}
                      data-testid="workspace-update-badge"
                      title="A newer palmux-ws image is available — click to update"
                    >
                      ⬆ update
                    </span>
                  )}
                </button>
                {chipMenuOpen && (
                  <div
                    className={styles.runtimeChipMenu}
                    data-testid="runtime-chip-menu"
                    role="menu"
                  >
                    {/* S7364e3: image-update action, shown only when stale */}
                    {updateAvailable && (
                      <>
                        <button
                          className={`${styles.runtimeChipMenuOption} ${styles.runtimeUpdateOption}`}
                          data-testid="runtime-update-action"
                          role="menuitem"
                          onClick={handleUpdateClick}
                          disabled={updatePending}
                        >
                          <span className={styles.runtimeChipMenuDot} /> ⬆ Update container
                          <span className={styles.runtimeChipMenuUnavailable}>new image</span>
                        </button>
                        <div className={styles.runtimeChipMenuTitle}>Change runtime</div>
                      </>
                    )}
                    {!updateAvailable && <div className={styles.runtimeChipMenuTitle}>Change runtime</div>}
                    <button
                      className={`${styles.runtimeChipMenuOption} ${branch.runtime.kind === 'host' ? styles.runtimeChipMenuOptionActive : ''}`}
                      data-testid="runtime-option-host"
                      role="menuitem"
                      onClick={() => handleMenuSelect('host')}
                    >
                      <span className={styles.runtimeChipMenuDot} /> host
                      {branch.runtime.kind === 'host' && <span className={styles.runtimeChipMenuCurrent}>current</span>}
                    </button>
                    {incusAvailable ? (
                      <button
                        className={`${styles.runtimeChipMenuOption} ${branch.runtime.kind === 'incus-container' ? styles.runtimeChipMenuOptionActive : ''}`}
                        data-testid="runtime-option-incus-container"
                        role="menuitem"
                        onClick={() => handleMenuSelect('incus-container')}
                      >
                        <span className={styles.runtimeChipMenuDot} /> incus-container
                        {branch.runtime.kind === 'incus-container' && <span className={styles.runtimeChipMenuCurrent}>current</span>}
                      </button>
                    ) : (
                      <button
                        className={`${styles.runtimeChipMenuOption} ${styles.runtimeChipMenuOptionDisabled}`}
                        data-testid="runtime-option-incus-container"
                        role="menuitem"
                        disabled
                        title={incusReason}
                      >
                        <span className={styles.runtimeChipMenuDot} /> incus-container
                        <span className={styles.runtimeChipMenuUnavailable}>unavailable</span>
                      </button>
                    )}
                    {runtimeError && (
                      <div className={styles.runtimeChipMenuError} data-testid="runtime-selector-error">
                        {runtimeError}
                      </div>
                    )}
                  </div>
                )}
                {/* S7364e3: update error persists OUTSIDE the menu so it stays
                    visible if the user dismisses the menu (container kept). */}
                {updateError && (
                  <div
                    className={styles.runtimeUpdateError}
                    data-testid="update-container-error"
                    role="alert"
                    title="Click to dismiss"
                    onClick={() => setUpdateError(null)}
                  >
                    ⚠ {updateError}
                  </div>
                )}
              </span>
            )}
          </>
        )}
      </div>
      <div className={styles.right}>
        <UploadsIndicator />
        <ActivityInbox />
        <button
          className={styles.iconBtn}
          onClick={() => showPalette()}
          title="Command palette (⌘K / Ctrl+K)"
          aria-label="Command palette"
        >
          ⌘
        </button>
        <button
          className={styles.iconBtn}
          onClick={() => setDeviceSetting('theme', theme === 'dark' ? 'light' : 'dark')}
          title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
          aria-label="Toggle theme"
        >
          {theme === 'dark' ? '☾' : '☀'}
        </button>
        {wide && (
          <button
            className={
              splitEnabled ? `${styles.iconBtn} ${styles.iconBtnActive}` : styles.iconBtn
            }
            onClick={() => setDeviceSetting('splitEnabled', !splitEnabled)}
            title={splitEnabled ? 'Disable split' : 'Enable split'}
            aria-label="Toggle split panel"
            aria-pressed={splitEnabled}
          >
            ▥
          </button>
        )}
        <span className={`${styles.dot} ${styles[status]}`} title={status} />
      </div>
    </header>
    </>
  )
}

function useWideViewport(threshold: number): boolean {
  const [wide, setWide] = useState(() =>
    typeof window === 'undefined' ? true : window.innerWidth >= threshold,
  )
  useEffect(() => {
    if (typeof window === 'undefined') return
    const mql = window.matchMedia(`(min-width: ${threshold}px)`)
    const onChange = () => setWide(mql.matches)
    onChange()
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [threshold])
  return wide
}

function repoLabel(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}
