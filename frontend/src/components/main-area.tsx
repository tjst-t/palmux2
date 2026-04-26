import { useCallback, useEffect, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'

import { selectBranchById, usePalmuxStore } from '../stores/palmux-store'

import { Divider } from './divider'
import { Panel, type PanelTarget } from './panel'
import styles from './main-area.module.css'

const SPLIT_MIN_WIDTH = 900

export function MainArea() {
  const { repoId, branchId, tabId } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const splitEnabled = usePalmuxStore((s) => s.deviceSettings.splitEnabled)
  const splitRatio = usePalmuxStore((s) => s.deviceSettings.splitRatio)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const focusedPanel = usePalmuxStore((s) => s.focusedPanel)
  const setFocusedPanel = usePalmuxStore((s) => s.setFocusedPanel)
  const containerRef = useRef<HTMLDivElement>(null)

  const leftTarget: PanelTarget = { repoId, branchId, tabId }
  const rightTarget = parseRightParam(searchParams.get('right'))

  // Auto-collapse split below 900px. We track via window.innerWidth so changing
  // the layout doesn't require a re-render storm; matchMedia keeps it cheap.
  const narrow = useNarrowViewport(SPLIT_MIN_WIDTH)
  const showSplit = splitEnabled && !narrow

  const setRightTarget = useCallback(
    (next: PanelTarget) => {
      const encoded = encodeRightParam(next)
      setSearchParams(
        (prev) => {
          const out = new URLSearchParams(prev)
          if (encoded) {
            out.set('right', encoded)
          } else {
            out.delete('right')
          }
          return out
        },
        { replace: false },
      )
    },
    [setSearchParams],
  )

  // If the right target points at something that no longer exists, clear it.
  const repos = usePalmuxStore((s) => s.repos)
  useEffect(() => {
    if (!rightTarget.repoId) return
    const repo = repos.find((r) => r.id === rightTarget.repoId)
    if (!repo) {
      setRightTarget({})
      return
    }
    if (rightTarget.branchId && !repo.openBranches.some((b) => b.id === rightTarget.branchId)) {
      setRightTarget({ repoId: rightTarget.repoId })
    }
  }, [repos, rightTarget.repoId, rightTarget.branchId, setRightTarget])

  // Clear notifications whenever a Claude tab is the active tab on either
  // panel — visiting the agent counts as "I read it".
  const clearBranchNotifications = usePalmuxStore((s) => s.clearBranchNotifications)
  const leftBranch = usePalmuxStore((s) =>
    leftTarget.repoId && leftTarget.branchId
      ? selectBranchById(leftTarget.repoId, leftTarget.branchId)(s)
      : undefined,
  )
  const rightBranch = usePalmuxStore((s) =>
    rightTarget.repoId && rightTarget.branchId
      ? selectBranchById(rightTarget.repoId, rightTarget.branchId)(s)
      : undefined,
  )
  useEffect(() => {
    const checks: { repoId?: string; branchId?: string; tabId?: string; branch: typeof leftBranch }[] = [
      { ...leftTarget, branch: leftBranch },
    ]
    if (showSplit) checks.push({ ...rightTarget, branch: rightBranch })
    for (const c of checks) {
      if (!c.repoId || !c.branchId || !c.tabId || !c.branch) continue
      const tab = c.branch.tabSet.tabs.find((t) => t.id === decodeURIComponent(c.tabId!))
      if (tab?.type === 'claude') {
        void clearBranchNotifications(c.repoId, c.branchId)
      }
    }
  }, [
    leftTarget.repoId,
    leftTarget.branchId,
    leftTarget.tabId,
    leftBranch,
    rightTarget.repoId,
    rightTarget.branchId,
    rightTarget.tabId,
    rightBranch,
    showSplit,
    clearBranchNotifications,
  ])

  // Ctrl+Shift+Left/Right swaps focus between panels when split.
  useEffect(() => {
    if (!showSplit) return
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey && e.shiftKey)) return
      if (e.key === 'ArrowLeft') {
        setFocusedPanel('left')
      } else if (e.key === 'ArrowRight') {
        setFocusedPanel('right')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showSplit, setFocusedPanel])

  const setRatio = useCallback(
    (r: number) => setDeviceSetting('splitRatio', r),
    [setDeviceSetting],
  )

  return (
    <div className={styles.area} ref={containerRef}>
      {showSplit ? (
        <>
          <div style={{ flex: `0 0 ${splitRatio}%`, display: 'flex', minWidth: 0 }}>
            <Panel
              side="left"
              target={leftTarget}
              focused={focusedPanel === 'left'}
              onFocus={() => setFocusedPanel('left')}
            />
          </div>
          <Divider ratio={splitRatio} onChange={setRatio} containerRef={containerRef} />
          <div
            style={{
              flex: `0 0 calc(${100 - splitRatio}% - 4px)`,
              display: 'flex',
              minWidth: 0,
            }}
          >
            <Panel
              side="right"
              target={rightTarget}
              focused={focusedPanel === 'right'}
              onFocus={() => setFocusedPanel('right')}
              onRightTargetChange={setRightTarget}
            />
          </div>
        </>
      ) : (
        <Panel
          side="left"
          target={leftTarget}
          focused
          onFocus={() => setFocusedPanel('left')}
          full
        />
      )}
    </div>
  )
}

function parseRightParam(raw: string | null): PanelTarget {
  if (!raw) return {}
  const parts = raw.split('/').map(decodeURIComponent)
  return {
    repoId: parts[0] || undefined,
    branchId: parts[1] || undefined,
    tabId: parts[2] || undefined,
  }
}

function encodeRightParam(t: PanelTarget): string | null {
  if (!t.repoId) return null
  const segs = [t.repoId, t.branchId ?? '', t.tabId ?? ''].map(encodeURIComponent)
  // Trim trailing empties for a tidier URL.
  while (segs.length > 1 && segs[segs.length - 1] === '') segs.pop()
  return segs.join('/')
}

function useNarrowViewport(threshold: number): boolean {
  const [narrow, setNarrow] = useState(() =>
    typeof window === 'undefined' ? false : window.innerWidth < threshold,
  )
  useEffect(() => {
    if (typeof window === 'undefined') return
    const mql = window.matchMedia(`(max-width: ${threshold - 1}px)`)
    const onChange = () => setNarrow(mql.matches)
    onChange()
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [threshold])
  return narrow
}
