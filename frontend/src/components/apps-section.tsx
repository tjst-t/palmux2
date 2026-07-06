/**
 * AppsSection — S41bdf2 "1アプリ=1カード" model.
 *
 * Each app is one card with two toggles: install (home.packages/systemPackages →
 * nixos-rebuild, reaches host + all containers via the shared /nix/store) and
 * auth-folder share (Sd44947 shared_dirs, hot, reaches all containers). The share
 * toggle is DEPENDENT on install: greyed + aria-disabled until installed. Every
 * toggle row states its 適用先 and rebuild boundary. Rendered inside the deploy
 * settings panel. Matches prototype/s41bdf2-app-cards.html classes/testids.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { appsApi, type AppView, type AppsListView, type AppValidateResult } from '../lib/api'
import styles from './apps-section.module.css'

// $HOME-scope validate mirrors the server rule (config.ExpandSharedDir) so the
// optional custom-app auth folder gets an inline message before submit.
function shareScopeError(raw: string, home: string): string | null {
  const t = raw.trim()
  if (t === '') return null // optional
  let abs = t
  if (t === '~') abs = home
  else if (t.startsWith('~/')) abs = home.replace(/\/$/, '') + '/' + t.slice(2)
  else if (t.startsWith('~')) return 'サポートされるのは ~/（現在のユーザのホーム）のみです'
  if (!abs.startsWith('/')) return '絶対パスか ~/ で始めてください'
  const homeClean = home.replace(/\/$/, '')
  if (abs !== homeClean && !abs.startsWith(homeClean + '/')) return `$HOME (${homeClean}) 配下のみ共有できます`
  return null
}

export function AppsSection() {
  const [view, setView] = useState<AppsListView | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [busyIds, setBusyIds] = useState<Record<string, boolean>>({})
  const [toast, setToast] = useState<string | null>(null)
  const toastTimer = useRef<number | undefined>(undefined)

  const showToast = useCallback((msg: string) => {
    setToast(msg)
    window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(null), 4000)
  }, [])

  const refresh = useCallback(async () => {
    try {
      const v = await appsApi.list()
      setView(v)
      setLoadError(null)
    } catch (err: unknown) {
      setLoadError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  // Fetch on mount. The async work is fully inside the effect's inner function so
  // the first state update lands in a later microtask (React data-fetch-in-effect
  // pattern), not a synchronous cascading setState.
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const v = await appsApi.list()
        if (!cancelled) {
          setView(v)
          setLoadError(null)
        }
      } catch (err: unknown) {
        if (!cancelled) setLoadError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  // Poll while a rebuild is in progress (installing state) so the card advances
  // installing → installed/error without a manual reload.
  const anyInstalling = view?.apps.some((a) => a.state === 'installing') || view?.rebuildRunning
  useEffect(() => {
    if (!anyInstalling) return
    const t = window.setInterval(() => void refresh(), 3000)
    return () => window.clearInterval(t)
  }, [anyInstalling, refresh])

  const setBusy = (id: string, b: boolean) => setBusyIds((prev) => ({ ...prev, [id]: b }))

  const onInstallToggle = useCallback(async (app: AppView) => {
    setBusy(app.id, true)
    try {
      if (app.installed) {
        await appsApi.uninstall(app.id)
        showToast(`${app.display} をアンインストールしました`)
      } else {
        const r = await appsApi.install({ id: app.id })
        showToast(r.rebuildKicked
          ? `${app.display} をインストール中（ホスト + 全コンテナに反映）`
          : `${app.display} を保存しました（NixOS で rebuild 時に反映）`)
      }
      await refresh()
    } catch (err: unknown) {
      showToast(`エラー: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(app.id, false)
    }
  }, [refresh, showToast])

  const onShareToggle = useCallback(async (app: AppView) => {
    if (!app.installed) return // 従属: dependent on install
    setBusy(app.id, true)
    try {
      const r = await appsApi.share(app.id, !app.shared)
      showToast(!app.shared
        ? `${app.display} の認証フォルダを共有（${r.containers} 個の稼働中コンテナに反映）`
        : `${app.display} の認証フォルダ共有を解除`)
      await refresh()
    } catch (err: unknown) {
      showToast(`エラー: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(app.id, false)
    }
  }, [refresh, showToast])

  if (loadError) {
    return (
      <div className={styles.wrap}>
        <div className={styles.sectionLabel}>アプリ</div>
        <p className={styles.err} data-testid="apps-load-error">読み込みエラー: {loadError}</p>
        <button type="button" className={styles.btnGhost} onClick={() => void refresh()}>再試行</button>
      </div>
    )
  }

  if (!view) {
    return (
      <div className={styles.wrap}>
        <div className={styles.sectionLabel}>アプリ</div>
        <p className={styles.loading} data-testid="apps-loading">読み込み中…</p>
      </div>
    )
  }

  return (
    <div className={styles.wrap}>
      <div className={styles.sectionLabel}>アプリ</div>
      <p className={styles.intro}>
        1アプリ = 1枚のカード。各カードに <b>2つのトグル</b>だけ。行ごとに「適用先」と反映コストを明記します。
        <br />
        <span className={styles.introMuted}>インストール</span>は<b>ホストと全コンテナに同時に</b>行き渡ります（別々ではありません）。裏では{' '}
        <code>home.packages</code> に足して共有 <code>/nix/store</code> 経由で全コンテナへ届けています。
        {!view.nixOSHost && (
          <span className={styles.introNote}> ※ このホストは NixOS アプライアンスではないため、インストールは設定を保存するのみ（反映は NixOS 上の rebuild 時）。</span>
        )}
      </p>

      <div className={styles.grid} data-testid="apps-grid">
        {view.apps.map((app) => (
          <AppCard
            key={app.id}
            app={app}
            busy={!!busyIds[app.id]}
            onInstall={() => void onInstallToggle(app)}
            onShare={() => void onShareToggle(app)}
            onRetry={() => void onInstallToggle(app)}
          />
        ))}
      </div>

      <AddAppRow home={view.home} onAdded={refresh} showToast={showToast} nixOSHost={view.nixOSHost} />

      <p className={styles.catalogNote}>
        既知アプリのカタログ: <code>infisical</code> / <code>1password-cli</code> / <code>gh</code> / <code>awscli2</code>{' '}
        …（認証フォルダのパスはカタログが保持）。
      </p>

      {toast && <div className={styles.toast} data-testid="toast">✓ {toast}</div>}
    </div>
  )
}

function AppCard({
  app, busy, onInstall, onShare, onRetry,
}: {
  app: AppView
  busy: boolean
  onInstall: () => void
  onShare: () => void
  onRetry: () => void
}) {
  const installing = app.state === 'installing'
  const shareDisabled = !app.installed || installing
  const cardClass = [styles.card, app.installed ? styles.installed : '', app.state === 'error' ? styles.errorCard : ''].filter(Boolean).join(' ')

  return (
    <div className={cardClass} data-testid={`app-card-${app.id}`} data-state={app.state}>
      <div className={styles.head}>
        <div className={styles.ico}>{app.icon}</div>
        <div className={styles.title}>
          <span className={styles.nm}>{app.display}</span>
          <span className={styles.idText}>{app.id}</span>
        </div>
      </div>
      <div className={styles.desc}>{app.description}{!app.installed && app.state !== 'error' ? ' 未インストール' : ''}</div>

      {app.state === 'error' && (
        <div className={styles.acErr} data-testid={`app-error-${app.id}`}>
          <span>⚠</span>
          <div>
            {app.error || 'nixos-rebuild が失敗しました（旧世代を維持）。'}
            <span className={styles.rollback} data-testid={`app-rollback-${app.id}`} onClick={onRetry} role="button" tabIndex={0}>再試行</span>
          </div>
        </div>
      )}

      {/* install row */}
      <div className={styles.row}>
        <div className={styles.rlabel}>
          <span className={styles.rt}>
            インストール <span className={`${styles.chip} ${styles.chipRebuild}`}>要 rebuild（軽・即）</span>
          </span>
          {installing ? (
            <div className={styles.progress} data-testid={`install-progress-${app.id}`}>
              <span className={styles.spinner} /> nixos-rebuild switch を実行中… — ホスト + 全コンテナへ反映
            </div>
          ) : (
            <span className={styles.reach}>適用先: <b>ホスト + 全コンテナ</b>（共有 /nix/store 経由）</span>
          )}
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={app.installed}
          aria-label={`${app.display} をインストール`}
          data-testid={`install-toggle-${app.id}`}
          disabled={busy || installing}
          className={`${styles.sw} ${app.installed ? styles.swOn : ''} ${(busy || installing) ? styles.swBusy : ''}`}
          onClick={onInstall}
        />
      </div>

      {/* share row (dependent on install) */}
      {app.authPath && (
        <div className={`${styles.row} ${shareDisabled ? styles.rowDisabled : ''}`}>
          <div className={styles.rlabel}>
            <span className={styles.rt}>
              認証フォルダを共有 <span className={`${styles.chip} ${styles.chipHot}`}>即時反映（hot）</span>
            </span>
            <span className={styles.rp}>{app.authPath}</span>
            <span className={styles.reach}>
              {app.installed
                ? <>適用先: <b>全コンテナ</b>（稼働中コンテナへ即反映・汎用「共有フォルダ」と同一 source）</>
                : <>インストール後に有効化されます（共有はインストールに<b>従属</b>）。</>}
            </span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={app.shared}
            aria-disabled={shareDisabled}
            aria-label={`${app.display} の認証フォルダを共有${shareDisabled ? '（未インストールのため無効）' : ''}`}
            data-testid={`share-toggle-${app.id}`}
            disabled={shareDisabled || busy}
            className={`${styles.sw} ${styles.swHot} ${app.shared ? `${styles.swOn} ${styles.swOnHot}` : ''} ${busy ? styles.swBusy : ''}`}
            onClick={onShare}
          />
        </div>
      )}
    </div>
  )
}

function AddAppRow({
  home, onAdded, showToast, nixOSHost,
}: {
  home: string
  onAdded: () => Promise<void>
  showToast: (m: string) => void
  nixOSHost: boolean
}) {
  const [pkg, setPkg] = useState('')
  const [sharePath, setSharePath] = useState('')
  const [validity, setValidity] = useState<AppValidateResult | null>(null)
  const [validating, setValidating] = useState(false)
  const [adding, setAdding] = useState(false)
  const debounce = useRef<number | undefined>(undefined)

  const shareErr = shareScopeError(sharePath, home)

  // Validation is triggered from the input's onChange (an event handler, not an
  // effect) so there is no synchronous setState-in-effect. Debounced so we only
  // hit `nix eval` after the user pauses typing.
  const onPkgChange = useCallback((value: string) => {
    setPkg(value)
    window.clearTimeout(debounce.current)
    const t = value.trim()
    if (t === '') {
      setValidity(null)
      setValidating(false)
      return
    }
    setValidating(true)
    debounce.current = window.setTimeout(async () => {
      try {
        const r = await appsApi.validate(t)
        setValidity(r)
      } catch {
        setValidity({ package: t, valid: false, unavailable: false, message: '検証に失敗しました' })
      } finally {
        setValidating(false)
      }
    }, 500)
  }, [])

  // Add is allowed when the package resolved OR nix is unavailable to validate
  // (dev/non-NixOS) — never when it explicitly failed to resolve.
  const canAdd = pkg.trim() !== '' && !validating && !shareErr && !!validity && (validity.valid || validity.unavailable)

  const onAdd = useCallback(async () => {
    if (!canAdd) return
    setAdding(true)
    try {
      await appsApi.install({ id: pkg.trim(), package: pkg.trim(), authPath: sharePath.trim() || undefined })
      showToast(`${pkg.trim()} を追加しました`)
      setPkg('')
      setSharePath('')
      setValidity(null)
      await onAdded()
    } catch (err: unknown) {
      showToast(`エラー: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setAdding(false)
    }
  }, [canAdd, pkg, sharePath, onAdded, showToast])

  return (
    <div>
      <div className={styles.sectionLabel} style={{ marginTop: 20 }}>ユーザ定義アプリを追加</div>
      <p className={styles.intro}>
        カタログに無い nixpkgs パッケージを追加します。追加前に <code>nix eval nixpkgs#&lt;name&gt;</code> で存在を検証し、
        <b>見つからなければ ⚠ を表示して「追加」を無効化</b>します（rebuild は走りません）。
        {!nixOSHost && <span className={styles.introNote}> ※ 非 NixOS ホストでは検証はスキップされます。</span>}
      </p>
      <div className={styles.addRow}>
        <div className={styles.addField}>
          <input
            type="text"
            className={styles.input}
            placeholder="nixpkgs パッケージ名（例 ripgrep, jq）"
            value={pkg}
            aria-invalid={!!validity && !validity.valid && !validity.unavailable}
            data-testid="app-add-input"
            onChange={(e) => onPkgChange(e.target.value)}
          />
          <span
            className={`${styles.validity} ${validity?.valid ? styles.validityOk : ''} ${validity && !validity.valid && !validity.unavailable ? styles.validityBad : ''}`}
            data-testid="app-add-validity"
            data-state={validating ? 'checking' : validity ? (validity.valid ? 'valid' : validity.unavailable ? 'unavailable' : 'invalid') : 'empty'}
          >
            {validating ? '検証中…' : validity ? (validity.valid ? `✓ ${validity.message}` : validity.unavailable ? `ⓘ ${validity.message}` : `⚠ ${validity.message}`) : ' '}
          </span>
        </div>
        <div className={styles.addField}>
          <input
            type="text"
            className={styles.input}
            placeholder="認証フォルダ（任意, 例 ~/.config/foo）"
            value={sharePath}
            aria-invalid={!!shareErr}
            data-testid="app-add-share-input"
            onChange={(e) => setSharePath(e.target.value)}
          />
          <span className={`${styles.validity} ${shareErr ? styles.validityBad : ''}`} data-testid="app-add-share-validity">
            {shareErr ? `⚠ ${shareErr}` : '$HOME スコープ内のみ'}
          </span>
        </div>
        <button
          type="button"
          className={styles.btnPrimary}
          data-testid="app-add-btn"
          disabled={!canAdd || adding}
          onClick={() => void onAdd()}
        >
          ＋ 追加
        </button>
      </div>
    </div>
  )
}
