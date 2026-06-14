// Browser tab view — S62374c-2
//
// Renders the shared Chromium browser live via CDP screencast.
// States: host-notice | stopped | starting | running (viewport + hint)
//
// WS protocol (palmux attach endpoint):
//   Server → Client:
//     {type:"frame", data:<base64 jpeg>, meta:{deviceWidth,deviceHeight}}
//     {type:"url",   url:<string>}
//     {type:"error", msg:<string>}
//   Client → Server (input):
//     {type:"input", kind:"mouse", eventType, x, y, button, clickCount}
//     {type:"input", kind:"mouse", eventType:"mouseWheel", x, y, deltaX, deltaY}
//     {type:"input", kind:"key",   eventType, key, text}
//     {type:"input", kind:"touch", x, y, touchType}
//     {type:"navigate", url}
//     {type:"reload"}
//     {type:"back"}
//     {type:"forward"}
//
// [AC-S62374c-2-1] [AC-S62374c-2-2] [AC-S62374c-2-3] [AC-S62374c-2-4]
// [AC-S62374c-2-5] [AC-S62374c-2-6] [AC-S62374c-2-7] [AC-S62374c-2-8]
// [AC-S62374c-2-10]

import { useCallback, useEffect, useRef, useState } from 'react'

import { api } from '../../lib/api'
import { ReconnectingWebSocket } from '../../lib/ws'
import type { TabViewProps } from '../../lib/tab-registry'
import { usePalmuxStore, selectBranchById } from '../../stores/palmux-store'

import styles from './browser-view.module.css'

// ─── Types ───────────────────────────────────────────────────────────────────

type BrowserState = 'stopped' | 'starting' | 'running'

interface StateView {
  state: BrowserState
  cdpReachable: boolean
  url?: string
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

interface ControlBarProps {
  state: BrowserState
  url: string
  popoutHref: string
  onUrlChange: (u: string) => void
  onGo: () => void
  onBack: () => void
  onForward: () => void
  onReload: () => void
  onStop: () => void
}

function ControlBar({
  state, url, popoutHref,
  onUrlChange, onGo, onBack, onForward, onReload, onStop,
}: ControlBarProps) {
  const disabled = state !== 'running'

  const handleKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') onGo()
  }

  return (
    <div className={styles.bar}>
      <div className={styles.navBtns}>
        <button
          className={styles.navBtn}
          data-testid="browser-back"
          title="戻る"
          disabled={disabled}
          onClick={onBack}
        >◀</button>
        <button
          className={styles.navBtn}
          data-testid="browser-forward"
          title="進む"
          disabled={disabled}
          onClick={onForward}
        >▶</button>
        <button
          className={styles.navBtn}
          data-testid="browser-reload"
          title="リロード"
          disabled={disabled}
          onClick={onReload}
        >⟳</button>
      </div>

      <input
        className={styles.urlInput}
        data-testid="browser-url-input"
        value={url}
        placeholder={disabled ? 'ブラウザ停止中 — Start すると操作できます' : 'URL を入力して Enter…'}
        spellCheck={false}
        disabled={disabled}
        onChange={(e) => onUrlChange(e.target.value)}
        onKeyDown={handleKey}
      />
      <button
        className={styles.goBtn}
        data-testid="browser-go"
        disabled={disabled}
        onClick={onGo}
      >Go</button>

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
        <StateBadge state={state} />
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

interface LiveViewportProps {
  repoId: string
  branchId: string
  onUrl: (url: string) => void
}

function LiveViewport({ repoId, branchId, onUrl }: LiveViewportProps) {
  const imgRef = useRef<HTMLImageElement>(null)
  const wsRef  = useRef<ReconnectingWebSocket | null>(null)
  const metaRef = useRef<{ deviceWidth: number; deviceHeight: number }>({ deviceWidth: 1280, deviceHeight: 800 })

  // Establish WS and pump frames into the img src.
  useEffect(() => {
    const ws = new ReconnectingWebSocket({
      url: buildAttachURL(repoId, branchId),
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const msg = JSON.parse(ev.data as string) as {
            type: string
            data?: string
            meta?: { deviceWidth: number; deviceHeight: number }
            url?: string
          }
          if (msg.type === 'frame' && msg.data) {
            if (msg.meta) metaRef.current = msg.meta
            if (imgRef.current) {
              imgRef.current.src = `data:image/jpeg;base64,${msg.data}`
            }
          } else if (msg.type === 'url' && msg.url) {
            onUrl(msg.url)
          }
        } catch {
          // ignore parse errors
        }
      },
    })
    ws.connect()
    wsRef.current = ws

    return () => {
      ws.close()
      wsRef.current = null
    }
  }, [repoId, branchId, onUrl])

  // ── Input forwarding helpers ──────────────────────────────────────────────

  const send = useCallback((msg: object) => {
    wsRef.current?.send(JSON.stringify(msg))
  }, [])

  // Convert DOM coords (relative to the img element) to CDP viewport coords.
  const toViewport = useCallback((clientX: number, clientY: number, el: HTMLImageElement) => {
    const rect = el.getBoundingClientRect()
    const { deviceWidth, deviceHeight } = metaRef.current
    const scaleX = deviceWidth  / rect.width
    const scaleY = deviceHeight / rect.height
    return {
      x: (clientX - rect.left) * scaleX,
      y: (clientY - rect.top)  * scaleY,
    }
  }, [])

  const handleMouseDown = useCallback((e: React.MouseEvent<HTMLImageElement>) => {
    if (!imgRef.current) return
    const { x, y } = toViewport(e.clientX, e.clientY, imgRef.current)
    const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left'
    send({ type: 'input', kind: 'mouse', eventType: 'mousePressed', x, y, button, clickCount: 1 })
  }, [send, toViewport])

  const handleMouseUp = useCallback((e: React.MouseEvent<HTMLImageElement>) => {
    if (!imgRef.current) return
    const { x, y } = toViewport(e.clientX, e.clientY, imgRef.current)
    const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left'
    send({ type: 'input', kind: 'mouse', eventType: 'mouseReleased', x, y, button, clickCount: 1 })
  }, [send, toViewport])

  const handleMouseMove = useCallback((e: React.MouseEvent<HTMLImageElement>) => {
    if (!imgRef.current) return
    const { x, y } = toViewport(e.clientX, e.clientY, imgRef.current)
    send({ type: 'input', kind: 'mouse', eventType: 'mouseMoved', x, y })
  }, [send, toViewport])

  const handleWheel = useCallback((e: React.WheelEvent<HTMLImageElement>) => {
    if (!imgRef.current) return
    const { x, y } = toViewport(e.clientX, e.clientY, imgRef.current)
    send({ type: 'input', kind: 'mouse', eventType: 'mouseWheel', x, y, deltaX: e.deltaX, deltaY: e.deltaY })
  }, [send, toViewport])

  const handleKeyDown = useCallback((e: React.KeyboardEvent<HTMLImageElement>) => {
    send({ type: 'input', kind: 'key', eventType: 'keyDown', key: e.key, text: e.key.length === 1 ? e.key : '' })
  }, [send])

  const handleKeyUp = useCallback((e: React.KeyboardEvent<HTMLImageElement>) => {
    send({ type: 'input', kind: 'key', eventType: 'keyUp', key: e.key })
  }, [send])

  const handleTouchStart = useCallback((e: React.TouchEvent<HTMLImageElement>) => {
    e.preventDefault()
    if (!imgRef.current) return
    const t = e.changedTouches[0]
    const { x, y } = toViewport(t.clientX, t.clientY, imgRef.current)
    send({ type: 'input', kind: 'touch', x, y, touchType: 'touchStart' })
  }, [send, toViewport])

  const handleTouchEnd = useCallback((e: React.TouchEvent<HTMLImageElement>) => {
    e.preventDefault()
    if (!imgRef.current) return
    const t = e.changedTouches[0]
    const { x, y } = toViewport(t.clientX, t.clientY, imgRef.current)
    send({ type: 'input', kind: 'touch', x, y, touchType: 'touchEnd' })
  }, [send, toViewport])

  const handleTouchMove = useCallback((e: React.TouchEvent<HTMLImageElement>) => {
    e.preventDefault()
    if (!imgRef.current) return
    const t = e.changedTouches[0]
    const { x, y } = toViewport(t.clientX, t.clientY, imgRef.current)
    send({ type: 'input', kind: 'touch', x, y, touchType: 'touchMove' })
  }, [send, toViewport])

  // Expose navigate/reload/back/forward on window for ControlBar to call.
  useEffect(() => {
    const el = imgRef.current
    if (!el) return
    // Give the img keyboard focus when clicked.
    el.tabIndex = 0
  }, [])

  return (
    <div className={styles.viewportWrap}>
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <img
        ref={imgRef}
        className={styles.viewport}
        data-testid="browser-viewport"
        alt="Browser screencast"
        onMouseDown={handleMouseDown}
        onMouseUp={handleMouseUp}
        onMouseMove={handleMouseMove}
        onWheel={handleWheel}
        onKeyDown={handleKeyDown}
        onKeyUp={handleKeyUp}
        onTouchStart={handleTouchStart}
        onTouchEnd={handleTouchEnd}
        onTouchMove={handleTouchMove}
        // make focusable for key events
        tabIndex={0}
        role="img"
        style={{ outline: 'none' }}
      />
      <div className={styles.claudeHint} data-testid="browser-claude-hint">
        <span className={styles.liveDot} />
        <span>
          <b style={{ color: 'var(--color-fg)' }}>LIVE</b>
          {' · '}Claude と同じブラウザを共有中。クリック/入力すると自分でも操作できます（セッション・ログインは共有）。
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
  const [urlBar, setUrlBar] = useState('')

  const wsNavRef = useRef<ReconnectingWebSocket | null>(null)

  // ── Derive runtime availability ─────────────────────────────────────────

  const runtimeKind = branch?.runtime?.kind ?? 'host'
  const isIncus = runtimeKind === 'incus-container'

  // ── Poll state on mount + after start/stop ─────────────────────────────

  const fetchState = useCallback(async () => {
    try {
      const sv = await api.get<StateView>(`${browserBase(repoId, branchId)}/state`)
      setAvailable(sv.available)
      setBrowserState(sv.state)
      if (sv.url) setUrlBar(sv.url)
    } catch {
      // ignore transient errors
    }
  }, [repoId, branchId])

  useEffect(() => {
    void fetchState()
    // Poll every 5 s while starting so we pick up the "running" transition.
    const id = setInterval(() => void fetchState(), 5000)
    return () => clearInterval(id)
  }, [fetchState])

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

  // ── Navigation commands (sent via the attach WS) ──────────────────────

  // We open a dedicated WS for navigation commands when running so we
  // can send back/forward/reload/navigate without needing the viewport WS ref.
  // The LiveViewport manages its own WS for frames.
  useEffect(() => {
    if (browserState !== 'running') {
      wsNavRef.current?.close()
      wsNavRef.current = null
      return
    }
    const ws = new ReconnectingWebSocket({ url: buildAttachURL(repoId, branchId) })
    ws.connect()
    wsNavRef.current = ws
    return () => {
      ws.close()
      wsNavRef.current = null
    }
  }, [browserState, repoId, branchId])

  const sendNav = useCallback((msg: object) => {
    wsNavRef.current?.send(JSON.stringify(msg))
  }, [])

  const handleGo = useCallback(() => {
    let url = urlBar.trim()
    if (!url) return
    if (!url.startsWith('http://') && !url.startsWith('https://')) {
      url = 'http://' + url
    }
    sendNav({ type: 'navigate', url })
  }, [urlBar, sendNav])

  const handleBack    = useCallback(() => sendNav({ type: 'back'    }), [sendNav])
  const handleForward = useCallback(() => sendNav({ type: 'forward' }), [sendNav])
  const handleReload  = useCallback(() => sendNav({ type: 'reload'  }), [sendNav])

  // ── Popout route ──────────────────────────────────────────────────────

  const popoutHref = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/browser?view=fullscreen`

  // ── Render ────────────────────────────────────────────────────────────

  const handleUrlFromViewport = useCallback((u: string) => setUrlBar(u), [])

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
              上でのみ動きます。host runtime ではブラウザの隔離・CDP 共有を行いません。
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
          <ControlBar
            state="stopped"
            url=""
            popoutHref={popoutHref}
            onUrlChange={() => {}}
            onGo={() => {}}
            onBack={() => {}}
            onForward={() => {}}
            onReload={() => {}}
            onStop={() => {}}
          />
          <div className={styles.center} data-testid="browser-stopped">
            <div className={styles.stateCard}>
              <div className={styles.glyph}>🌐</div>
              <h2>ブラウザは停止中</h2>
              <p>
                この Workspace 専用の共有ブラウザです。起動すると、
                <b style={{ color: 'var(--color-fg)' }}>Claude と同じ画面</b>
                をリアルタイムで観察でき、必要なときに自分でも操作できます。
              </p>
              <button
                className={styles.startBtn}
                data-testid="browser-start"
                onClick={() => void handleStart()}
              >▶ ブラウザを起動</button>
              <p className={styles.subnote}>
                Claude も <code>palmux-browser start</code> で起動できます（起動は Activity Inbox に表示）。
                Workspace を開いただけでは起動しません。
              </p>
            </div>
          </div>
        </>
      ) : browserState === 'starting' ? (
        <>
          <ControlBar
            state="starting"
            url=""
            popoutHref={popoutHref}
            onUrlChange={() => {}}
            onGo={() => {}}
            onBack={() => {}}
            onForward={() => {}}
            onReload={() => {}}
            onStop={() => {}}
          />
          <div className={styles.center} data-testid="browser-starting">
            <div className={styles.stateCard}>
              <div className={styles.spinner} />
              <h2>ブラウザを起動中…</h2>
              <p>Chromium を起動して CDP が応答するまで待っています。</p>
            </div>
          </div>
        </>
      ) : (
        /* running */
        <>
          <ControlBar
            state="running"
            url={urlBar}
            popoutHref={popoutHref}
            onUrlChange={setUrlBar}
            onGo={handleGo}
            onBack={handleBack}
            onForward={handleForward}
            onReload={handleReload}
            onStop={() => void handleStop()}
          />
          <LiveViewport
            repoId={repoId}
            branchId={branchId}
            onUrl={handleUrlFromViewport}
          />
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
  const [urlBar, setUrlBar] = useState('')

  const wsNavRef = useRef<ReconnectingWebSocket | null>(null)

  const fetchState = useCallback(async () => {
    try {
      const sv = await api.get<StateView>(`${browserBase(repoId, branchId)}/state`)
      setBrowserState(sv.state)
      if (sv.url) setUrlBar(sv.url)
    } catch { /* ignore */ }
  }, [repoId, branchId])

  useEffect(() => { void fetchState() }, [fetchState])

  useEffect(() => {
    if (browserState !== 'running') {
      wsNavRef.current?.close(); wsNavRef.current = null; return
    }
    const ws = new ReconnectingWebSocket({ url: buildAttachURL(repoId, branchId) })
    ws.connect(); wsNavRef.current = ws
    return () => { ws.close(); wsNavRef.current = null }
  }, [browserState, repoId, branchId])

  const sendNav = useCallback((msg: object) => wsNavRef.current?.send(JSON.stringify(msg)), [])

  const handleGo = useCallback(() => {
    let url = urlBar.trim()
    if (!url) return
    if (!url.startsWith('http://') && !url.startsWith('https://')) url = 'http://' + url
    sendNav({ type: 'navigate', url })
  }, [urlBar, sendNav])

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
        <div className={styles.navBtns}>
          <button className={styles.navBtn} data-testid="browser-back"
            disabled={browserState !== 'running'} onClick={() => sendNav({ type: 'back' })}>◀</button>
          <button className={styles.navBtn} data-testid="browser-forward"
            disabled={browserState !== 'running'} onClick={() => sendNav({ type: 'forward' })}>▶</button>
          <button className={styles.navBtn} data-testid="browser-reload"
            disabled={browserState !== 'running'} onClick={() => sendNav({ type: 'reload' })}>⟳</button>
        </div>
        <input
          className={styles.urlInput}
          data-testid="browser-url-input"
          value={urlBar}
          spellCheck={false}
          disabled={browserState !== 'running'}
          placeholder="URL を入力…"
          onChange={(e) => setUrlBar(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') handleGo() }}
        />
        <button className={styles.goBtn} data-testid="browser-go"
          disabled={browserState !== 'running'} onClick={handleGo}>Go</button>
        <div className={styles.barRight}>
          <StateBadge state={browserState} />
          {browserState === 'running' && (
            <button className={styles.stopBtn} data-testid="browser-stop"
              onClick={() => api.post(`${browserBase(repoId, branchId)}/stop`).then(() => setBrowserState('stopped')).catch(() => {})}>
              ■ Stop
            </button>
          )}
        </div>
      </div>

      {browserState === 'running' ? (
        <LiveViewport repoId={repoId} branchId={branchId} onUrl={setUrlBar} />
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
