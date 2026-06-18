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
import { getKeycode, getKeysym } from '@novnc/novnc/util'

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

// ─── Keyboard capture (S8fe0cb) ───────────────────────────────────────────────
//
// noVNC binds its Keyboard handler to the VNC <canvas> and derives keys from
// `e.code`. When the user's LOCAL (OS) Japanese IME is ON, composition keydowns
// arrive as keyCode 229 with an empty `e.code`, so noVNC drops them — nothing
// reaches the remote browser, and a web page cannot turn off the OS IME.
//
// Fix: while the Browser tab is focused, capture the keyboard on a hidden
// IME-disabled `<input type="password">`. Password fields disable IME
// composition in every browser (direct/raw input is forced), so local IME ON no
// longer matters — raw keys reach the remote. We map each event to a keysym with
// noVNC's OWN helpers (getKeysym / getKeycode) and forward via the public
// `rfb.sendKey(keysym, code, down)`. Japanese is then typed through the REMOTE
// fcitx5 (Ctrl+Space, per S62374c). Mouse / scroll / click stay on the canvas.
//
// We mirror noVNC's core tracking: remember the keysym we sent for each `code`
// so the matching keyup releases the same keysym, and release everything on blur
// to avoid stuck modifiers. [AC-S8fe0cb-1-1] [AC-S8fe0cb-1-2]

// Minimal structural view of the RFB methods we drive. Lets tests pass a stand-in
// recorder when there is no live VNC connection (the real RFB satisfies this).
interface KeySink {
  sendKey(keysym: number | null, code: string, down: boolean): void
}

// Optional test tap. When `window.__palmuxVncKeyTap` is a function (set by an
// E2E test before interacting), every forwarded key is mirrored to it so the
// test can assert keysym/code/down WITHOUT a live VNC connection. In production
// the global is undefined and this is a no-op — it is a verification seam over
// the component's real output, not a network/IO mock.
declare global {
  interface Window {
    __palmuxVncKeyTap?: (keysym: number | null, code: string, down: boolean) => void
  }
}

/**
 * Attach raw-key forwarding to a hidden input that drives the given key sink.
 * Returns handlers + a release-all. Logic kept in one place so the focus routing
 * in LiveViewport stays declarative.
 */
function makeKeyForwarder(rfb: KeySink) {
  // code -> keysym currently held down (so keyup releases the same keysym even
  // if the layout/keysym would now resolve differently).
  const downList = new Map<string, number>()

  const send = (keysym: number | null, code: string, down: boolean) => {
    rfb.sendKey(keysym, code, down)
    try {
      window.__palmuxVncKeyTap?.(keysym, code, down)
    } catch {
      /* tap must never break input */
    }
  }

  const releaseAll = () => {
    for (const [code, keysym] of downList) {
      send(keysym, code, false)
    }
    downList.clear()
  }

  const onKeyDown = (e: KeyboardEvent) => {
    // Never let the hidden password input accumulate text or submit.
    e.preventDefault()

    const code = getKeycode(e)
    let keysym = getKeysym(e)

    // Key repeat: the browser fires keydown repeatedly while held. Reuse the
    // keysym we first sent for this code (matches noVNC) so the server sees a
    // consistent down stream. [AC-S8fe0cb-1-2]
    if (downList.has(code)) {
      keysym = downList.get(code) ?? keysym
    }

    if (keysym === null || keysym === 0) {
      // Can't identify the key (e.g. pure dead-composition with no code). Drop
      // silently — nothing to forward. Local IME composition that produces a
      // real code still has a keysym and is forwarded above.
      return
    }

    if (code !== 'Unidentified') {
      downList.set(code, keysym)
    }
    send(keysym, code, true)
  }

  const onKeyUp = (e: KeyboardEvent) => {
    e.preventDefault()
    const code = getKeycode(e)
    // Only release a key we actually sent a press for (mirrors noVNC core:
    // release this._keyDownList[code], nothing if it was never pressed). This
    // avoids forwarding an unpaired key-up when the keydown was dropped (keysym
    // 0/null) but the keyup now resolves to a usable keysym.
    const keysym = downList.get(code)
    if (code !== 'Unidentified') {
      downList.delete(code)
    }
    if (keysym) {
      send(keysym, code, false)
    }
  }

  return { onKeyDown, onKeyUp, releaseAll }
}

// ─── LiveViewport ─────────────────────────────────────────────────────────────
// Uses noVNC RFB to render the remote Chromium and handle mouse/scroll/clipboard.
// Keyboard is captured on a hidden IME-disabled input (see makeKeyForwarder).

interface LiveViewportProps {
  repoId: string
  branchId: string
}

function LiveViewport({ repoId, branchId }: LiveViewportProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const rfbRef = useRef<RFB | null>(null)

  // ── Connect noVNC + own the keyboard ───────────────────────────────────
  // ONE effect so teardown order is deterministic: we MUST release any held
  // keys (sendKey requires a 'connected' socket) BEFORE rfb.disconnect() tears
  // the connection down — otherwise the release bytes are dropped and a held
  // modifier stays stuck on the remote after leaving the tab.
  useEffect(() => {
    const el = containerRef.current
    const input = inputRef.current
    if (!el || !input) return

    const wsURL = buildAttachURL(repoId, branchId)
    const rfb = new RFB(el, wsURL, { wsProtocols: ['binary'] })
    rfb.scaleViewport = true
    rfb.clipViewport = false
    rfb.resizeSession = false
    // We own the keyboard via the hidden IME-disabled input, so noVNC must NOT
    // grab keys on its <canvas> (its canvas path is the one that drops IME
    // composition keys) and must NOT steal focus on click.
    rfb.focusOnClick = false
    rfbRef.current = rfb

    const forwarder = makeKeyForwarder(rfb)

    // noVNC (re)grabs its canvas keyboard on connect; ungrab so the hidden input
    // is the SOLE keyboard sink. Idempotent + defensive against API drift.
    const ungrabCanvasKeyboard = () => {
      try {
        const kb = (rfb as unknown as { _keyboard?: { ungrab?: () => void } })._keyboard
        kb?.ungrab?.()
      } catch {
        /* internal API absent — focusOnClick=false still keeps focus on input */
      }
    }
    ungrabCanvasKeyboard()
    rfb.addEventListener('connect', ungrabCanvasKeyboard)

    const onKeyDown = (e: KeyboardEvent) => {
      forwarder.onKeyDown(e)
      input.value = '' // keep the password field empty (managers/IME find nothing)
    }
    const onKeyUp = (e: KeyboardEvent) => {
      forwarder.onKeyUp(e)
      input.value = ''
    }
    // Genuine focus loss (tab switch, alt-tab, click into another field) must
    // release every held key so modifiers don't get stuck on the remote. We do
    // NOT release on our own canvas-click refocus, because that re-focuses the
    // input synchronously (see refocusInput) so no blur fires. [AC-S8fe0cb-1-2]
    const onBlur = () => {
      forwarder.releaseAll()
    }
    const onInput = () => {
      input.value = '' // defensive: nothing should land here (preventDefault)
    }

    input.addEventListener('keydown', onKeyDown)
    input.addEventListener('keyup', onKeyUp)
    input.addEventListener('blur', onBlur)
    input.addEventListener('input', onInput)

    // Auto-focus so keys flow as soon as the tab is shown (tab active), unless
    // the user is actively editing some other field.
    const focusId = window.setTimeout(() => {
      const ae = document.activeElement
      const editing =
        ae instanceof HTMLElement &&
        (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.isContentEditable) &&
        ae !== input
      if (!editing) input.focus({ preventScroll: true })
    }, 0)

    return () => {
      window.clearTimeout(focusId)
      input.removeEventListener('keydown', onKeyDown)
      input.removeEventListener('keyup', onKeyUp)
      input.removeEventListener('blur', onBlur)
      input.removeEventListener('input', onInput)
      rfb.removeEventListener('connect', ungrabCanvasKeyboard)
      // Release held keys while the socket is STILL connected, then disconnect.
      forwarder.releaseAll()
      rfb.disconnect()
      rfbRef.current = null
    }
  }, [repoId, branchId])

  // Clicking / touching the canvas must keep keyboard flowing. We focus the
  // hidden input SYNCHRONOUSLY in the capture phase, before noVNC sees the
  // pointer — so the canvas never holds focus (no blur→releaseAll, no key lands
  // on noVNC's IME-broken canvas path) while the pointer still reaches the
  // canvas for mouse/scroll/drag. [AC-S8fe0cb-1-2]
  const refocusInput = useCallback(() => {
    inputRef.current?.focus({ preventScroll: true })
  }, [])

  return (
    <div className={styles.viewportWrap}>
      {/*
        Hidden IME-disabled keyboard capture. type=password forces direct input
        (no OS IME composition). Off-screen + aria-hidden + autocomplete tricks
        keep password managers / autofill / save-password prompts away.
        [AC-S8fe0cb-1-1] [AC-S8fe0cb-1-4]
      */}
      <input
        ref={inputRef}
        type="password"
        className={styles.keyCapture}
        data-testid="browser-keycapture"
        // Password-manager / autofill avoidance:
        name="palmux-vnc-kbd-7c8aff"
        id="palmux-vnc-kbd-7c8aff"
        autoComplete="off"
        // Chrome often ignores autoComplete=off for password fields; the obscured
        // name + these per-manager hints keep 1Password / LastPass / autofill away.
        data-1p-ignore="true"
        data-lpignore="true"
        data-form-type="other"
        aria-hidden="true"
        tabIndex={-1}
        spellCheck={false}
        defaultValue=""
      />
      {/* noVNC renders its own <canvas> inside this div. We refocus the hidden
          input in the CAPTURE phase (before noVNC handles the pointer) so the
          input never loses focus on a canvas click — keys keep flowing while the
          pointer still reaches the canvas. [AC-S8fe0cb-1-2] */}
      <div
        ref={containerRef}
        className={styles.viewport}
        data-testid="browser-viewport"
        style={{ flex: 1, minHeight: 0 }}
        onMouseDownCapture={refocusInput}
        onTouchStartCapture={refocusInput}
      />
      <div className={styles.claudeHint} data-testid="browser-claude-hint">
        <span className={styles.liveDot} />
        <span>
          <b style={{ color: 'var(--color-fg)' }}>LIVE</b>
          {' · '}Claude と同じブラウザを共有中。マウス/キーボードで直接操作できます（セッション・ログインは共有）。
          ローカル IME はオンのままで OK（日本語はリモートで Ctrl+Space）。
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
