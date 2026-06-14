// Browser tab view — noVNC rework
//
// The remote Chromium instance (running headful on Xvfb inside the incus
// container) is presented to the user via noVNC, which speaks raw RFB binary
// over a WebSocket. palmux acts as a dumb byte-pipe (WS ↔ x11vnc TCP).
//
// All navigation, mouse, keyboard, and IME input is handled by:
//   - noVNC on the client side (maps browser events → RFB protocol)
//   - Chromium's own UI (address bar, back/forward, etc.)
//   - fcitx5 inside the container (server-side Japanese IME)
//
// The custom CDP screencast image + textarea overlay + navigate/back/forward/
// reload REST calls are entirely removed.
//
// States: host-notice | stopped | starting | running (noVNC viewport + hint)
//
// [AC-S62374c-1-1] [AC-S62374c-1-2] [AC-S62374c-1-4]

import { useCallback, useEffect, useRef, useState } from 'react'
import RFB from '@novnc/novnc'

import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'
import { usePalmuxStore, selectBranchById } from '../../stores/palmux-store'

import styles from './browser-view.module.css'

// ─── Types ───────────────────────────────────────────────────────────────────

type BrowserState = 'stopped' | 'starting' | 'running'

interface StateView {
  state: BrowserState
  cdpReachable: boolean
  available: boolean
}

interface StartResponse { state: BrowserState }
interface StopResponse  { state: BrowserState }

// ─── API helpers ─────────────────────────────────────────────────────────────

function browserBase(repoId: string, branchId: string): string {
  return `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/browser`
}

function buildAttachURL(repoId: string, branchId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}${browserBase(repoId, branchId)}/attach`
}

// ─── StateBadge ──────────────────────────────────────────────────────────────

function StateBadge({ state }: { state: BrowserState }) {
  const cls =
    state === 'running'  ? styles.stateBadgeRunning :
    state === 'starting' ? styles.stateBadgeStarting :
                           styles.stateBadgeStopped
  const dot =
    state === 'starting'
      ? <span className={styles.badgeDotPulse} />
      : <span className={styles.badgeDot} />

  return (
    <span className={`${styles.stateBadge} ${cls}`} data-testid="browser-state-badge">
      {dot} {state}
    </span>
  )
}

// ─── ControlBar ──────────────────────────────────────────────────────────────
// Simplified: no URL bar / back / forward / reload / Go (chromium's own UI).

interface ControlBarProps {
  state: BrowserState
  popoutHref: string
  onStop: () => void
}

function ControlBar({ state, popoutHref, onStop }: ControlBarProps) {
  return (
    <div className={styles.bar}>
      <StateBadge state={state} />
      <div className={styles.barRight}>
        {state === 'running' && (
          <a
            className={styles.popoutLink}
            data-testid="browser-popout"
            href={popoutHref}
            target="_blank"
            rel="noopener noreferrer"
            title="別タブで大きく開く（同じ共有ブラウザ）"
          >↗ Open</a>
        )}
        {state === 'running' && (
          <button
            className={styles.stopBtn}
            data-testid="browser-stop"
            onClick={onStop}
          >■ Stop</button>
        )}
      </div>
    </div>
  )
}

// ─── LiveViewport ─────────────────────────────────────────────────────────────
// Uses noVNC RFB to render the remote Chromium and handle all input.

interface LiveViewportProps {
  repoId: string
  branchId: string
}

function LiveViewport({ repoId, branchId }: LiveViewportProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const rfbRef = useRef<RFB | null>(null)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const wsURL = buildAttachURL(repoId, branchId)
    const rfb = new RFB(el, wsURL, { wsProtocols: ['binary'] })
    rfb.scaleViewport = true
    rfb.clipViewport = false
    rfb.resizeSession = false
    rfbRef.current = rfb

    return () => {
      rfb.disconnect()
      rfbRef.current = null
    }
  }, [repoId, branchId])

  return (
    <div className={styles.viewportWrap}>
      {/* noVNC renders its own <canvas> inside this div */}
      <div
        ref={containerRef}
        className={styles.viewport}
        data-testid="browser-viewport"
        style={{ flex: 1, minHeight: 0 }}
      />
      <div className={styles.claudeHint} data-testid="browser-claude-hint">
        <span className={styles.liveDot} />
        <span>
          <b style={{ color: 'var(--color-fg)' }}>LIVE</b>
          {' · '}Claude と同じブラウザを共有中。マウス/キーボードで直接操作できます（セッション・ログインは共有）。
        </span>
      </div>
    </div>
  )
}

// ─── BrowserView (root) ───────────────────────────────────────────────────────

export function BrowserView({ repoId, branchId }: TabViewProps) {
  const branch = usePalmuxStore(selectBranchById(repoId, branchId))

  const [browserState, setBrowserState] = useState<BrowserState>('stopped')
  const [available, setAvailable] = useState<boolean | null>(null)

  // ── Derive runtime availability ─────────────────────────────────────────

  const runtimeKind = branch?.runtime?.kind ?? 'host'
  const isIncus = runtimeKind === 'incus-container'

  // ── Poll state on mount + after start/stop ─────────────────────────────

  useEffect(() => {
    // Inline poller (cancelled-guard + .then) so the lint rule doesn't see a
    // synchronous setState in the effect body — same shape as workspace-actions.
    let cancelled = false
    const tick = () => {
      api.get<StateView>(`${browserBase(repoId, branchId)}/state`)
        .then((sv) => {
          if (cancelled) return
          setAvailable(sv.available)
          setBrowserState(sv.state)
        })
        .catch(() => {/* ignore transient errors */})
    }
    tick()
    // Poll every 5 s while starting so we pick up the "running" transition.
    const id = setInterval(tick, 5000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [repoId, branchId])

  // ── Start / Stop ───────────────────────────────────────────────────────

  const handleStart = useCallback(async () => {
    setBrowserState('starting')
    try {
      const r = await api.post<StartResponse>(`${browserBase(repoId, branchId)}/start`)
      setBrowserState(r.state)
    } catch {
      setBrowserState('stopped')
    }
  }, [repoId, branchId])

  const handleStop = useCallback(async () => {
    try {
      const r = await api.post<StopResponse>(`${browserBase(repoId, branchId)}/stop`)
      setBrowserState(r.state)
    } catch {
      setBrowserState('stopped')
    }
  }, [repoId, branchId])

  // ── Popout route ──────────────────────────────────────────────────────

  const popoutHref = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/browser?view=fullscreen`

  // ── Render ────────────────────────────────────────────────────────────

  // Determine display state.
  // If available is null we haven't loaded yet; use isIncus as proxy.
  const isAvailable = available ?? isIncus

  return (
    <div className={styles.shell} data-testid="browser-tab-panel">
      {/* Host-runtime notice — no browser available */}
      {!isAvailable && available !== null ? (
        <div className={styles.center}>
          <div className={styles.stateCard} data-testid="browser-host-notice">
            <div className={styles.glyph}>🖥️</div>
            <h2>Browser タブは incus-container 専用です</h2>
            <p>
              共有ブラウザは Workspace を隔離する{' '}
              <b style={{ color: 'var(--color-fg)' }}>incus-container runtime</b>{' '}
              上でのみ動きます。host runtime ではブラウザの隔離・VNC 共有を行いません。
            </p>
            <p className={styles.subnote}>
              ヘッダーの runtime chip から <code>incus-container</code> に切り替えると、
              この Workspace でブラウザを起動できます。
            </p>
          </div>
        </div>
      ) : browserState === 'stopped' ? (
        <>
          {/* Inert control bar while stopped */}
          <ControlBar state="stopped" popoutHref={popoutHref} onStop={() => {}} />
          <div className={styles.center} data-testid="browser-stopped">
            <div className={styles.stateCard}>
              <div className={styles.glyph}>🌐</div>
              <h2>ブラウザは停止中</h2>
              <p>
                この Workspace 専用の共有ブラウザです。起動すると、
                <b style={{ color: 'var(--color-fg)' }}>Claude と同じ画面</b>
                をリアルタイムで観察でき、マウス/キーボードで自分でも操作できます。
              </p>
              <button
                className={styles.startBtn}
                data-testid="browser-start"
                onClick={() => void handleStart()}
              >▶ ブラウザを起動</button>
              <p className={styles.subnote}>
                Claude も <code>palmux-browser start</code> で起動できます。
                Workspace を開いただけでは起動しません。
              </p>
            </div>
          </div>
        </>
      ) : browserState === 'starting' ? (
        <>
          <ControlBar state="starting" popoutHref={popoutHref} onStop={() => {}} />
          <div className={styles.center} data-testid="browser-starting">
            <div className={styles.stateCard}>
              <div className={styles.spinner} />
              <h2>ブラウザを起動中…</h2>
              <p>Xvfb・Chromium・x11vnc を起動しています。</p>
            </div>
          </div>
        </>
      ) : (
        /* running */
        <>
          <ControlBar
            state="running"
            popoutHref={popoutHref}
            onStop={() => void handleStop()}
          />
          <LiveViewport repoId={repoId} branchId={branchId} />
        </>
      )}
    </div>
  )
}

// ─── BrowserFullscreen (standalone popout) ────────────────────────────────────
// Rendered at /<repoId>/<branchId>/browser?view=fullscreen.
// Same shared browser, no palmux tab/drawer chrome.
// [AC-S62374c-2-10]
export function BrowserFullscreen({ repoId, branchId }: { repoId: string; branchId: string }) {
  const branch = usePalmuxStore(selectBranchById(repoId, branchId))
  const [browserState, setBrowserState] = useState<BrowserState>('stopped')

  useEffect(() => {
    let cancelled = false
    api.get<StateView>(`${browserBase(repoId, branchId)}/state`)
      .then((sv) => { if (!cancelled) setBrowserState(sv.state) })
      .catch(() => {/* ignore */})
    return () => { cancelled = true }
  }, [repoId, branchId])

  const runtimeKind = branch?.runtime?.kind ?? 'incus-container'

  return (
    <div className={styles.fullscreenShell} data-testid="browser-fullscreen">
      <header className={styles.fullscreenHeader}>
        <span className={styles.fullscreenBrand}>◐ palmux2 · Browser</span>
        <span className={styles.fullscreenCrumb}>
          {branch?.name ?? branchId}
          {' · '}
          <span style={{ color: 'var(--color-accent-light)' }}>{runtimeKind}</span>
        </span>
      </header>

      <div className={styles.bar}>
        <StateBadge state={browserState} />
        <div className={styles.barRight}>
          {browserState === 'running' && (
            <button
              className={styles.stopBtn}
              data-testid="browser-stop"
              onClick={() =>
                api.post(`${browserBase(repoId, branchId)}/stop`)
                  .then(() => setBrowserState('stopped'))
                  .catch(() => {})
              }
            >■ Stop</button>
          )}
        </div>
      </div>

      {browserState === 'running' ? (
        <LiveViewport repoId={repoId} branchId={branchId} />
      ) : (
        <div className={styles.center}>
          <div className={styles.stateCard}>
            <div className={styles.glyph}>🌐</div>
            <h2>ブラウザは{browserState === 'starting' ? '起動中…' : '停止中'}</h2>
            <p>メインタブから起動してください。</p>
          </div>
        </div>
      )}
    </div>
  )
}
