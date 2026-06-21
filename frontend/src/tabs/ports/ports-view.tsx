// Ports tab — See8bd4-3 + S4c591a
// Lists an incus-container workspace's listening ports. Two publishing modes,
// selected by whether a public domain (wildcard DNS) is configured:
//   - subdomain mode (publicDomainConfigured=true): publish each port as an
//     SSO-protected HTTPS subdomain via the Caddy admin API (See8bd4).
//   - host-port mode  (publicDomainConfigured=false): publish each port as
//     http://<hostIP>:<port> via an incus proxy device, UNAUTHENTICATED — the
//     wildcard-DNS-less fallback (S4c591a). The mode is environment-level, not
//     a per-port choice.
import { useCallback, useEffect, useRef, useState } from 'react'

import { usePalmuxStore, selectBranchById } from '../../stores/palmux-store'
import { portsApi, type PortView } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import styles from './ports-view.module.css'

// Per-port local UI state (toggling, error, copy flash).
interface PortRowState {
  pending: boolean
  error: string | null
  copied: boolean
}

function usePortRowState() {
  const [rows, setRows] = useState<Record<number, PortRowState>>({})

  const getRow = (port: number): PortRowState =>
    rows[port] ?? { pending: false, error: null, copied: false }

  // Stable across renders so handler useCallbacks can depend on it without
  // re-creating every render. Uses the functional updater (prev) rather than
  // capturing `rows`, so [] deps are correct.
  const setRow = useCallback((port: number, patch: Partial<PortRowState>) =>
    setRows((prev) => ({
      ...prev,
      [port]: { ...(prev[port] ?? { pending: false, error: null, copied: false }), ...patch },
    })), [])

  return { getRow, setRow }
}

// ─── Individual port row ──────────────────────────────────────────────────────

interface PortRowProps {
  port: PortView
  state: PortRowState
  onToggle: (port: number, wantExposed: boolean) => void
  onFlipPublic: (port: number, wantPublic: boolean) => void
  onCopy: (port: number, url: string) => void
}

function PortRow({ port: p, state, onToggle, onFlipPublic, onCopy }: PortRowProps) {
  const isExposed = p.exposed
  const hasError = !!state.error

  return (
    <div
      className={`${styles.portRow} ${isExposed ? styles.exposed : ''}`}
      data-testid={`ports-row-${p.port}`}
    >
      {/* Toggle */}
      <button
        role="switch"
        aria-checked={isExposed ? 'true' : 'false'}
        data-testid={`ports-expose-toggle-${p.port}`}
        className={styles.toggle}
        disabled={state.pending}
        onClick={() => onToggle(p.port, !isExposed)}
        aria-label={isExposed ? `Unexpose port ${p.port}` : `Expose port ${p.port}`}
      />

      {/* Port identity */}
      <div className={styles.portMain}>
        <div className={styles.portIdent}>
          <span className={styles.portNum}>:{p.port}</span>
          <span className={styles.portMeta}>{p.proto} · {p.process}</span>
          <span className={styles.portBind}>{p.bindAddr}</span>
          {(p.category === 'system' || p.category === 'palmux') && (
            <span
              className={`${styles.catBadge} ${p.category === 'palmux' ? styles.catBadgePalmux : styles.catBadgeSystem}`}
              data-testid={`ports-cat-badge-${p.port}`}
              title={p.category === 'palmux'
                ? 'palmux の Browser タブ用ポート (noVNC / CDP) — あなたのサーバではありません'
                : 'OS のシステム/インフラポート'}
            >
              {p.category}
            </span>
          )}
          {p.localhostOnly && (
            <span className={styles.relayMark} title="localhost-only — exposed via in-container relay">
              localhost · relay
            </span>
          )}
        </div>
      </div>

      {/* URL / placeholder (row 2, col 2) */}
      <div className={styles.portUrlArea}>
        {isExposed && p.publicUrl ? (
          <>
            <a
              className={styles.portUrl}
              data-testid={`ports-public-url-${p.port}`}
              href={p.publicUrl}
              target="_blank"
              rel="noopener noreferrer"
              title={`${p.publicUrl} — open in new tab`}
            >
              {p.publicUrl}
            </a>
            <button
              className={`${styles.copyBtn} ${state.copied ? styles.copied : ''}`}
              data-testid={`ports-copy-${p.port}`}
              onClick={() => onCopy(p.port, p.publicUrl)}
              aria-label="Copy URL"
            >
              {state.copied ? 'copied' : 'copy'}
            </button>
          </>
        ) : (
          <span className={styles.portUrlNone}>— not exposed —</span>
        )}
      </div>

      {/* Right: badge + public/auth flip (col 3, rows 1–2) */}
      <div className={styles.portRight}>
        {isExposed ? (
          <>
            <span
              className={`${styles.badge} ${p.public ? styles.badgePublic : styles.badgeAuth}`}
              data-testid={`ports-public-badge-${p.port}`}
            >
              {p.public ? '🌐 public' : '🔒 auth'}
            </span>
            <button
              className={styles.authFlipBtn}
              disabled={state.pending}
              onClick={() => onFlipPublic(p.port, !p.public)}
              aria-label={p.public ? 'Require auth for this port' : 'Make this port public'}
            >
              {p.public ? 'require auth' : 'make public'}
            </button>
          </>
        ) : (
          <span
            className={`${styles.badge} ${styles.badgePrivate}`}
            data-testid={`ports-public-badge-${p.port}`}
          >
            private
          </span>
        )}
      </div>

      {/* Inline error (full-width row beneath everything) */}
      {hasError && (
        <div className={styles.rowError} data-testid={`ports-row-error-${p.port}`}>
          {state.error}
        </div>
      )}
    </div>
  )
}

// ─── Host-port row (S4c591a — wildcard-DNS-less fallback) ─────────────────────

interface HostPortRowProps {
  port: PortView
  state: PortRowState
  hostIP: string
  onToggle: (port: number, wantPublished: boolean) => void
  onCopy: (port: number, url: string) => void
}

function HostPortRow({ port: p, state, hostIP, onToggle, onCopy }: HostPortRowProps) {
  const isPublished = p.hostPublished
  const hasError = !!state.error
  // host port differs from container port → auto-reassigned to avoid collision.
  const reassigned = isPublished && p.hostPort > 0 && p.hostPort !== p.port
  const hostUrl = p.hostUrl || (hostIP ? `http://${hostIP}:${p.hostPort || p.port}` : '')

  return (
    <div
      className={`${styles.portRow} ${isPublished ? styles.exposed : ''}`}
      data-testid={`ports-row-${p.port}`}
    >
      {/* Toggle */}
      <button
        role="switch"
        aria-checked={isPublished ? 'true' : 'false'}
        data-testid={`ports-hostport-toggle-${p.port}`}
        className={styles.toggle}
        disabled={state.pending}
        onClick={() => onToggle(p.port, !isPublished)}
        aria-label={isPublished ? `Unpublish host port ${p.port}` : `Publish host port ${p.port}`}
      />

      {/* Port identity + URL */}
      <div className={styles.portMain}>
        <div className={styles.portIdent}>
          <span className={styles.portNum}>:{p.port}</span>
          <span className={styles.portMeta}>{p.proto} · {p.process}</span>
          {isPublished && (
            <span
              className={styles.noAuthWarning}
              data-testid={`ports-noauth-warning-${p.port}`}
              title="このポートは Caddy/SSO を通らず無認証で外部に公開されています"
            >
              ⚠ 無認証で外部に公開
            </span>
          )}
          {reassigned && (
            <span
              className={styles.reallocBadge}
              data-testid={`ports-hostport-realloc-${p.port}`}
              title={`${p.port} はホスト側で使用中のため、空きポート ${p.hostPort} を自動割当`}
            >
              host:{p.hostPort} に再割当
            </span>
          )}
        </div>
        {isPublished && hostUrl ? (
          <a
            className={styles.portUrl}
            data-testid={`ports-hostport-url-${p.port}`}
            href={hostUrl}
            target="_blank"
            rel="noopener noreferrer"
            title={`${hostUrl} — 無認証で開く`}
          >
            {hostUrl}
          </a>
        ) : (
          <span className={styles.portHint}>
            トグルで <code>http://&lt;hostIP&gt;:{p.port}</code> に公開（無認証）。
          </span>
        )}
      </div>

      {/* Copy */}
      {isPublished && hostUrl && (
        <button
          className={`${styles.copyBtn} ${state.copied ? styles.copied : ''}`}
          data-testid={`ports-hostport-copy-${p.port}`}
          onClick={() => onCopy(p.port, hostUrl)}
          aria-label="Copy host URL"
        >
          {state.copied ? 'copied' : 'コピー'}
        </button>
      )}

      {hasError && (
        <div className={styles.rowError} data-testid={`ports-row-error-${p.port}`}>
          {state.error}
        </div>
      )}
    </div>
  )
}

// ─── PortsView (root) ─────────────────────────────────────────────────────────

export function PortsView({ repoId, branchId }: TabViewProps) {
  const branch = usePalmuxStore(selectBranchById(repoId, branchId))
  // Live-updated ports from the WS event; null means "not yet received via WS".
  const wsPorts = usePalmuxStore((s) => s.branchPorts[`${repoId}/${branchId}`] ?? null)
  const applyBranchPortPatch = usePalmuxStore((s) => s.applyBranchPortPatch)

  const [loading, setLoading] = useState(true)
  const [restPorts, setRestPorts] = useState<import('../../lib/api').WorkspacePorts | null>(null)

  // The active ports data is the WS-pushed version if available, else the REST snapshot.
  const portsData = wsPorts ?? restPorts

  const { getRow, setRow } = usePortRowState()

  // Initial fetch. `loading` starts true and is cleared in the async
  // resolve/catch below (never set synchronously in the effect body).
  useEffect(() => {
    let cancelled = false
    portsApi.list(repoId, branchId)
      .then((data) => {
        if (!cancelled) {
          setRestPorts(data)
          setLoading(false)
        }
      })
      .catch(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [repoId, branchId])

  // Copy timeout refs — one per port.
  const copyTimers = useRef<Record<number, ReturnType<typeof setTimeout>>>({})

  const handleToggle = useCallback(async (port: number, wantExposed: boolean) => {
    // Optimistically update the exposed state in the REST snapshot.
    setRow(port, { pending: true, error: null })
    try {
      if (wantExposed) {
        const resp = await portsApi.expose(repoId, branchId, port, false)
        // Update local REST snapshot with the returned publicUrl.
        setRestPorts((prev) => {
          if (!prev) return prev
          return {
            ...prev,
            ports: prev.ports.map((p) =>
              p.port === port
                ? { ...p, exposed: true, public: resp.public, publicUrl: resp.publicUrl }
                : p,
            ),
          }
        })
        // Also patch the WS-pushed snapshot so the UI reflects it instantly even
        // if the branch.portsChanged event is briefly delayed.
        applyBranchPortPatch(repoId, branchId, port, {
          exposed: true, public: resp.public, publicUrl: resp.publicUrl,
        })
      } else {
        await portsApi.unexpose(repoId, branchId, port)
        setRestPorts((prev) => {
          if (!prev) return prev
          return {
            ...prev,
            ports: prev.ports.map((p) =>
              p.port === port
                ? { ...p, exposed: false, public: false, publicUrl: '' }
                : p,
            ),
          }
        })
        applyBranchPortPatch(repoId, branchId, port, {
          exposed: false, public: false, publicUrl: '',
        })
      }
      setRow(port, { pending: false, error: null })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setRow(port, { pending: false, error: msg })
      // NOTE: toggle already reverted because we never updated exposed in the optimistic path —
      // the server response drives the state mutation above. The UI will show
      // aria-checked=false again because exposed stayed at its prior value.
    }
  }, [repoId, branchId, setRow, applyBranchPortPatch])

  // S4c591a host-port mode: toggle adds/removes an incus proxy device exposing
  // http://<hostIP>:<port>. Reuses the same expose/unexpose endpoints; the
  // backend routes by mode (publicDomainConfigured=false → host-port).
  const handleHostToggle = useCallback(async (port: number, wantPublished: boolean) => {
    setRow(port, { pending: true, error: null })
    try {
      if (wantPublished) {
        const resp = await portsApi.expose(repoId, branchId, port, true)
        const patch = {
          hostPublished: resp.hostPublished ?? true,
          hostPort: resp.hostPort ?? port,
          hostUrl: resp.hostUrl ?? '',
        }
        setRestPorts((prev) => prev && {
          ...prev,
          ports: prev.ports.map((p) => (p.port === port ? { ...p, ...patch } : p)),
        })
        applyBranchPortPatch(repoId, branchId, port, patch)
      } else {
        await portsApi.unexpose(repoId, branchId, port)
        const patch = { hostPublished: false, hostPort: 0, hostUrl: '' }
        setRestPorts((prev) => prev && {
          ...prev,
          ports: prev.ports.map((p) => (p.port === port ? { ...p, ...patch } : p)),
        })
        applyBranchPortPatch(repoId, branchId, port, patch)
      }
      setRow(port, { pending: false, error: null })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setRow(port, { pending: false, error: msg })
    }
  }, [repoId, branchId, setRow, applyBranchPortPatch])

  const handleFlipPublic = useCallback(async (port: number, wantPublic: boolean) => {
    setRow(port, { pending: true, error: null })
    try {
      const resp = await portsApi.expose(repoId, branchId, port, wantPublic)
      setRestPorts((prev) => {
        if (!prev) return prev
        return {
          ...prev,
          ports: prev.ports.map((p) =>
            p.port === port
              ? { ...p, public: resp.public, publicUrl: resp.publicUrl }
              : p,
          ),
        }
      })
      applyBranchPortPatch(repoId, branchId, port, {
        public: resp.public, publicUrl: resp.publicUrl,
      })
      setRow(port, { pending: false, error: null })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setRow(port, { pending: false, error: msg })
    }
  }, [repoId, branchId, setRow, applyBranchPortPatch])

  const handleCopy = useCallback((port: number, url: string) => {
    navigator.clipboard.writeText(url).catch(() => {
      // Clipboard API unavailable — graceful degradation, no throw.
    })
    setRow(port, { copied: true })
    if (copyTimers.current[port]) clearTimeout(copyTimers.current[port])
    copyTimers.current[port] = setTimeout(() => {
      setRow(port, { copied: false })
    }, 1500)
  }, [setRow])

  // Reveal system (OS infra) + palmux-internal (browser stack) ports. Hidden by
  // default so the tab shows only the user's dev servers.
  const [showHidden, setShowHidden] = useState(false)

  // ── Derived state ──────────────────────────────────────────────────────────
  const runtimeKind = portsData?.runtimeKind ?? branch?.runtime?.kind ?? 'host'
  const isHost = runtimeKind === 'host'
  const ports = portsData?.ports ?? []
  const isUserPort = (p: PortView) => !p.category || p.category === 'user'
  const hiddenCount = ports.filter((p) => !isUserPort(p)).length
  const visiblePorts = showHidden ? ports : ports.filter(isUserPort)
  // S4c591a: host-port mode iff a public domain is NOT configured. Default to
  // subdomain mode (true) until the REST/WS payload tells us otherwise, so we
  // never flash the unauth-warning banner spuriously.
  const publicDomainConfigured = portsData?.publicDomainConfigured ?? true
  const hostPortMode = !isHost && !publicDomainConfigured
  const hostIP = portsData?.hostIP ?? ''

  // ── Render ─────────────────────────────────────────────────────────────────
  return (
    <div className={styles.shell} data-testid="ports-panel">
      <div className={styles.header}>
        <div className={styles.titleRow}>
          <span className={styles.title}>Ports</span>
          {!isHost && (
            <span className={styles.runtimeChip}>incus-container</span>
          )}
        </div>
        <p className={styles.subtitle}>
          {isHost
            ? 'このタブは Workspace の runtime に依存します。'
            : hostPortMode
              ? <>公開ドメイン (wildcard DNS) が未設定のため <strong>ホストポート公開モード</strong>です。各ポートを <code>{'http://<hostIP>:<port>'}</code> で公開できます。</>
              : <>コンテナ内で listen 中のポートを <code>{'<port>--<workspace>--<repo>.<base>'}</code> の HTTPS サブドメインとして公開できます。公開は既定で <strong>エッジ basic_auth の内側</strong> — 🔒 を外すと無認証公開になります。</>
          }
        </p>
        {!isHost && hiddenCount > 0 && (
          <label className={styles.showHidden} data-testid="ports-show-hidden">
            <input
              type="checkbox"
              checked={showHidden}
              onChange={(e) => setShowHidden(e.target.checked)}
            />
            <span>システム / 内部ポートも表示 <span className={styles.showHiddenCount}>({hiddenCount})</span></span>
          </label>
        )}
      </div>

      {/* S4c591a: persistent host-port-mode notice (unauth warning + how to
          switch to SSO-protected subdomain publishing). Only in host-port mode. */}
      {hostPortMode && (
        <div className={styles.hostPortNotice} data-testid="ports-mode-hostport-notice">
          <span className={styles.hostPortNoticeIcon}>⚠</span>
          <span>
            <strong>ホストポート公開モード</strong> — 公開ドメイン (wildcard DNS) が未設定のため、各ポートを <code>{'http://<hostIP>:<port>'}</code> で公開します。
            {' '}<strong>Caddy/SSO を通らない無認証公開</strong>です。デプロイ設定で公開ドメインを設定すると、自動で <strong>SSO 保護のサブドメイン公開</strong>に切り替わります。
          </span>
        </div>
      )}

      {loading && !portsData ? (
        <div className={styles.centeredState}>
          <div className={styles.loadingSpinner} data-testid="ports-loading">
            <div className={styles.spinnerDots}>
              <span className={styles.spinnerDot} />
              <span className={styles.spinnerDot} />
              <span className={styles.spinnerDot} />
            </div>
            <span>ポートをスキャン中…</span>
          </div>
        </div>
      ) : isHost ? (
        <div className={styles.centeredState}>
          <div className={styles.hostNotice} data-testid="ports-host-notice">
            <div className={styles.hostNoticeTitle}>
              この Workspace は <code>host</code> runtime です
            </div>
            <div className={styles.hostNoticeDesc}>
              ポートのサブドメイン公開は <b>incus-container</b> 専用機能です。host のポートは <b>portman</b> が管轄します。<br />
              Header の runtime chip から <b>incus-container</b> に切り替えると、このタブでコンテナポートを公開できます。
            </div>
          </div>
        </div>
      ) : visiblePorts.length === 0 ? (
        <div className={styles.centeredState}>
          <div className={styles.emptyCard} data-testid="ports-empty">
            <div className={styles.emptyIcon}>🔌</div>
            <div className={styles.emptyTitle}>listen 中のポートはありません</div>
            <div className={styles.emptyDesc}>
              コンテナ内で dev サーバ (<code>npm run dev</code> / <code>python3 -m http.server</code> 等) を起動すると、ここに自動で現れます。10 秒ごとにスキャンします。
              {hiddenCount > 0 && (
                <> <br />（システム / 内部ポートが {hiddenCount} 個あります — 上のトグルで表示できます）</>
              )}
            </div>
          </div>
        </div>
      ) : (
        <div className={styles.body}>
          <div className={styles.portsList}>
            {visiblePorts.map((p) => (
              hostPortMode ? (
                <HostPortRow
                  key={p.port}
                  port={p}
                  state={getRow(p.port)}
                  hostIP={hostIP}
                  onToggle={handleHostToggle}
                  onCopy={handleCopy}
                />
              ) : (
                <PortRow
                  key={p.port}
                  port={p}
                  state={getRow(p.port)}
                  onToggle={handleToggle}
                  onFlipPublic={handleFlipPublic}
                  onCopy={handleCopy}
                />
              )
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
