import { useEffect, useRef, useState } from 'react'

import { usePalmuxStore } from '../../stores/palmux-store'

import styles from './update-panel.module.css'

// S6ab0ed: top-right self-update badge + panel. Mirrors the Activity Inbox
// bellWrap/badge/popover pattern and the approved prototype
// (prototype/s6ab0ed-update-panel.html / s6ab0ed-updating.html). The badge is
// hidden when nothing is to update and no update is in flight.
export function UpdatePanel() {
  const snap = usePalmuxStore((s) => s.selfUpdate)
  const inProgress = usePalmuxStore((s) => s.updateInProgress)
  const updateFailed = usePalmuxStore((s) => s.updateFailed)
  const runSelfUpdate = usePalmuxStore((s) => s.runSelfUpdate)
  const runNixosRebuildUpdate = usePalmuxStore((s) => s.runNixosRebuildUpdate)
  const runImageInstall = usePalmuxStore((s) => s.runImageInstall)
  const imageInstallInProgress = usePalmuxStore((s) => s.imageInstallInProgress)
  const imageInstallError = usePalmuxStore((s) => s.imageInstallError)
  const [open, setOpen] = useState(false)
  const [runError, setRunError] = useState<string | null>(null)
  const ref = useRef<HTMLDivElement>(null)

  // Click-outside / Esc closes the popover (same as Activity Inbox).
  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const hasUpdate = !!snap?.available

  // ── Update in flight: progress badge + reconnecting panel ──────────────────
  if (inProgress) {
    return (
      <div ref={ref} className={styles.wrap}>
        <button
          className={`${styles.badge} ${styles.badgeProgress}`}
          data-testid="update-progress-badge"
          onClick={() => setOpen((v) => !v)}
        >
          <span className={styles.spin} /> 更新中…
        </button>
        {open && (
          <div className={styles.panel} data-testid="update-progress-panel" role="dialog">
            <div className={styles.head}>
              <div className={styles.headTitle}>まとめて更新中…</div>
            </div>
            <div className={styles.reconnect} data-testid="update-reconnecting">
              <span className={styles.spin} />
              <span>
                本体更新に伴い palmux が再起動します。<b>接続が切れたら自動で再接続</b>
                し、新バージョンを確認したら「更新しました」と表示します。
              </span>
            </div>
          </div>
        )}
      </div>
    )
  }

  // ── No update available (and not failed): render nothing (badge hidden). ───
  if (!hasUpdate && !updateFailed) return null

  const onUpdateAll = async () => {
    setRunError(null)
    try {
      await runSelfUpdate()
      // success path observed via the reconnect handshake; panel switches to
      // the progress state above on next render (updateInProgress flips true).
    } catch (err) {
      setRunError(err instanceof Error ? err.message : String(err))
    }
  }

  // S673a42-2: appliance host update button. If the palmux-ws image is ALSO out
  // of date, fetch it FIRST (in-process, while palmux2 is still up so the ~810 MB
  // download survives), THEN kick the host rebuild — one click updates both, which
  // is what the operator expects. The image fetch is best-effort: a failure is
  // surfaced (imageInstallError) but does not block the host update. After the
  // rebuild kick the panel switches to the progress/reconnect state
  // (updateInProgress) on the next render, same as onUpdateAll. The container
  // itself still picks up the new image via the operator-driven "Update container"
  // (drift badge) — we deliberately don't restart a running claude here.
  const onNixosUpdate = async () => {
    setRunError(null)
    try {
      const imageStale = snap?.components.some((c) => c.name === 'image' && c.available)
      if (imageStale) {
        await runImageInstall() // resolves on completion; sets imageInstallError on failure
      }
      await runNixosRebuildUpdate()
    } catch (err) {
      setRunError(err instanceof Error ? err.message : String(err))
    }
  }

  // S673a42-3: palmux-ws image fetch button (appliance only).
  const onImageInstall = () => {
    void runImageInstall()
  }

  return (
    <div ref={ref} className={styles.wrap}>
      <button
        className={`${styles.badge} ${updateFailed ? styles.badgeError : styles.badgeAvail}`}
        data-testid="update-available-badge"
        onClick={() => setOpen((v) => !v)}
        title="管理コンポーネントの更新があります"
      >
        <span className={styles.dot} /> {updateFailed ? '更新失敗' : '更新あり'}
      </button>
      {open && (
        <div className={styles.panel} data-testid="update-panel" role="dialog">
          <div className={styles.head}>
            <div className={styles.headTitle}>アップデートがあります</div>
            <div className={styles.headSub}>
              palmux が管理するコンポーネントに新リリースが出ています。6時間ごとに GitHub を確認。
            </div>
            {snap?.forced && (
              <div className={styles.headSub} data-testid="update-forced-note">
                🧪 強制更新テスト（同一バージョンで実行経路を検証中。実リリースではありません）
              </div>
            )}
          </div>

          {(snap?.components ?? []).map((c) => (
            <div className={styles.comp} key={c.name} data-testid={`update-comp-${c.name}`}>
              <div className={styles.compInfo}>
                <div className={styles.compName}>
                  {c.display} <span className={styles.src}>{c.source}</span>
                </div>
                <div className={styles.compVer}>
                  {c.available ? (
                    <>
                      {c.installed || '?'} <span className={styles.arrow}>→</span>{' '}
                      <span className={styles.new}>{c.latest}</span>
                    </>
                  ) : c.fetchable ? (
                    <>{c.installed || '?'} (最新)</>
                  ) : (
                    <>{c.installed || '?'} (最新版を取得できません)</>
                  )}
                </div>
              </div>
              {c.available && snap?.nixOSHost && c.name === 'image' ? (
                // S673a42-3: on the appliance the image is a separate axis from the
                // host nixos-rebuild — offer a direct fetch button on its row.
                <button
                  className={styles.rowBtn}
                  data-testid="update-image-fetch-btn"
                  onClick={onImageInstall}
                  disabled={imageInstallInProgress}
                >
                  {imageInstallInProgress ? (
                    <>
                      <span className={styles.spin} /> 取得中…
                    </>
                  ) : (
                    'image を取得'
                  )}
                </button>
              ) : c.available ? (
                <span className={styles.uptag}>更新あり</span>
              ) : c.fetchable ? (
                <span className={styles.curtag}>最新</span>
              ) : (
                <span className={styles.curtag} data-testid={`update-unfetchable-${c.name}`}>
                  取得不可
                </span>
              )}
            </div>
          ))}
          {imageInstallError && (
            <div className={styles.errBox} data-testid="update-image-error">
              ⚠ palmux-ws image の取得に失敗しました: {imageInstallError}
            </div>
          )}

          <div className={styles.foot}>
            {snap?.nixOSHost ? (
              <>
                {/* S673a42-2: GUI-kick the host update on the appliance. Only when a
                    newer palmux binary is available; otherwise no button (最新). */}
                {!snap.components.some((c) => c.name === 'palmux' && c.available) ? (
                  <div className={styles.restartNote} data-testid="update-nixos-uptodate">
                    本体 (palmux) は最新です。
                  </div>
                ) : snap.rebuildUpdaterReady === false ? (
                  // Bootstrap gap: the running palmux binary is newer than the
                  // deployed NixOS generation, which predates the GUI update unit
                  // (palmux-rebuild-update.service) + its polkit grant. The button
                  // would fail with a polkit "Access denied", so show the one-time
                  // manual crossing instead.
                  <div className={styles.errBox} data-testid="update-nixos-bootstrap-gap">
                    ⚠ この NixOS 世代には GUI 更新ユニットがありません（稼働中の palmux が世代より新しい状態）。
                    一度だけ端末で手動更新してください:
                    <br />
                    <code>sudo nixos-rebuild switch --flake {snap.applianceFlakeTarget || 'nixos'}</code>
                    <br />
                    以降は GUI ボタンで更新できます。
                  </div>
                ) : (
                  <button
                    className={styles.primaryBtn}
                    data-testid="update-nixos-rebuild-btn"
                    onClick={onNixosUpdate}
                    disabled={imageInstallInProgress}
                  >
                    {imageInstallInProgress
                      ? 'palmux-ws image を取得中…'
                      : snap.components.some((c) => c.name === 'image' && c.available)
                        ? '本体 + image を更新 (nixos-rebuild)'
                        : '本体を更新 (nixos-rebuild)'}
                  </button>
                )}
                <div className={styles.manualNote} data-testid="update-nixos-note">
                  このホストは NixOS（palmuxOS アプライアンス）です。ボタンで{' '}
                  <code>
                    nix flake update palmux && sudo nixos-rebuild switch --flake{' '}
                    {snap.applianceFlakeTarget || 'nixos'}
                  </code>{' '}
                  をキックします（世代切替＝アトミック、失敗時は旧世代が残り{' '}
                  <code>nixos-rebuild switch --flake {snap.applianceFlakeTarget || 'nixos'} --rollback</code>{' '}
                  または旧世代 boot で戻せます）。本体更新後 palmux2 は再起動し、この画面は数秒で再接続します。
                </div>
              </>
            ) : snap?.nixManaged ? (
              <>
                <button
                  className={styles.primaryBtn}
                  data-testid="update-all-btn"
                  onClick={onUpdateAll}
                >
                  すべてまとめて更新
                </button>
                <div className={styles.restartNote} data-testid="update-restart-note">
                  ⓘ 本体更新後 palmux は自動で再起動し、この画面は数秒で再接続します。実行中の
                  claude は <code>--resume</code> で復帰します。
                  <br />
                  CLI からも <code>palmux update</code> で同じ一括更新ができます（
                  <code>palmux update --check</code> で検出のみ）。
                </div>
              </>
            ) : (
              <div className={styles.manualNote} data-testid="update-manual-note">
                このインストール形態は手動更新です（Nix 管理外）。端末で{' '}
                <code>~/update-palmux2.sh</code> を実行するか、install.sh を再実行してください。
              </div>
            )}
            {updateFailed && (
              <div className={styles.errBox} data-testid="update-failed-note">
                ⚠ 更新に失敗しました。home-manager 世代でロールバックされ、旧バージョンが維持されています。
              </div>
            )}
            {runError && <div className={styles.errBox}>⚠ {runError}</div>}
          </div>
        </div>
      )}
    </div>
  )
}
