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
              {c.available ? (
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

          <div className={styles.foot}>
            {snap?.nixOSHost ? (
              <div className={styles.manualNote} data-testid="update-nixos-note">
                このホストは NixOS（palmuxOS アプライアンス）です。更新は端末から{' '}
                <code>sudo nixos-rebuild switch --flake /etc/palmux#appliance</code>{' '}
                で行ってください（世代切替＝アトミック、<code>nixos-rebuild switch --rollback</code>{' '}
                または旧世代 boot で確実に戻せます）。本体更新後、この画面は数秒で再接続します。
              </div>
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
