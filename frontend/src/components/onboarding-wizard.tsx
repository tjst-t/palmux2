/**
 * OnboardingWizard — Sa53137
 *
 * Full-screen first-launch wizard shown when GET /api/deploy returns
 * configured:false AND localStorage 'palmux:onboarding-seen' is not set.
 *
 * Modes:
 *   - ローカル/非公開: minimal config, no public domain needed
 *   - 公開（ドメイン + SSO）: public domain + Cloudflare token + auth
 */

import { useCallback, useEffect, useState } from 'react'

import { deployApi } from '../lib/api'

import styles from './onboarding-wizard.module.css'

const LS_KEY = 'palmux:onboarding-seen'

type Mode = 'local' | 'public'

interface Props {
  open: boolean
  onClose: () => void
}

export function OnboardingWizard({ open, onClose }: Props) {
  const [selectedMode, setSelectedMode] = useState<Mode>('local')
  const [domain, setDomain] = useState('')
  const [cloudflareToken, setCloudflareToken] = useState('')
  const [authUser, setAuthUser] = useState('admin')
  const [authPassword, setAuthPassword] = useState('')
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showPrivilegedNotice, setShowPrivilegedNotice] = useState(false)
  // Sb14caa: on a NixOS appliance the privileged apply is a GUI-kicked
  // `nixos-rebuild switch` rather than the `sudo palmux reconcile-system`
  // instruction (which the password-less, non-wheel palmux user cannot run).
  const [nixOSHost, setNixOSHost] = useState(false)
  const [rebuildState, setRebuildState] = useState<'idle' | 'running' | 'done' | 'failed'>('idle')

  useEffect(() => {
    void deployApi.get().then((d) => setNixOSHost(Boolean(d.nixOSHost))).catch(() => {})
  }, [])

  // Trigger `nixos-rebuild switch` via the palmux-rebuild unit and poll until it
  // settles. palmux2 itself restarts when the new config activates; the global
  // reconnect handshake (WS drop → /health → reconnect) covers that gap, and the
  // poll's transient failures are ignored until the post-switch server answers.
  const handleRebuild = useCallback(async () => {
    setRebuildState('running')
    setError(null)
    try {
      await deployApi.rebuild()
    } catch (err: unknown) {
      setRebuildState('failed')
      setError(err instanceof Error ? err.message : String(err))
      return
    }
    const started = Date.now()
    const poll = async (): Promise<void> => {
      if (Date.now() - started > 15 * 60 * 1000) {
        setRebuildState('failed')
        setError('nixos-rebuild timed out (15m). Check `journalctl -u palmux-rebuild`.')
        return
      }
      try {
        const st = await deployApi.rebuildStatus()
        if (st.active === 'failed' || (st.result && st.result !== 'success' && !st.running)) {
          setRebuildState('failed')
          setError('nixos-rebuild failed — config unchanged (previous generation kept). See `journalctl -u palmux-rebuild`.')
          return
        }
        if (st.active === 'inactive' && st.result === 'success') {
          setRebuildState('done')
          return
        }
      } catch {
        // transient — palmux2 is likely restarting from the switch; keep polling.
      }
      setTimeout(() => void poll(), 3000)
    }
    setTimeout(() => void poll(), 3000)
  }, [])

  const markSeen = useCallback(() => {
    try {
      localStorage.setItem(LS_KEY, '1')
    } catch {
      // ignore
    }
  }, [])

  const handleSkip = useCallback(() => {
    markSeen()
    onClose()
  }, [markSeen, onClose])

  const handleBack = useCallback(() => {
    setShowPrivilegedNotice(false)
    setError(null)
  }, [])

  const handleNext = useCallback(async () => {
    setApplying(true)
    setError(null)
    try {
      if (selectedMode === 'local') {
        await deployApi.apply({
          public: { domain: '', basic_auth_user: '' },
        })
        markSeen()
        onClose()
      } else {
        // Public mode: apply server + public, then handle secrets
        await deployApi.apply({
          public: {
            domain,
            basic_auth_user: authUser,
          },
        })
        if (authPassword.trim() || cloudflareToken.trim()) {
          await deployApi.rotateSecrets({
            ...(authPassword.trim() ? { password: authPassword } : {}),
            // CLOUDFLARE_API_TOKEN (DNS-01 wildcard cert) — NOT the palmux auth
            // token. Earlier this was mis-sent as `token` (= PALMUX_TOKEN), so the
            // Cloudflare token never reached secrets.env and Caddy couldn't issue
            // the wildcard cert.
            ...(cloudflareToken.trim() ? { cloudflareToken: cloudflareToken } : {}),
          })
        }
        markSeen()
        setShowPrivilegedNotice(true)
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setApplying(false)
    }
  }, [selectedMode, domain, authUser, authPassword, cloudflareToken, markSeen, onClose])

  if (!open) return null

  return (
    <div className={styles.overlay} data-testid="onboarding-wizard">
      <div className={styles.card}>
        <div className={styles.steps} aria-label="進捗">
          <div className={`${styles.pip} ${styles.pipDone}`} />
          <div className={`${styles.pip} ${styles.pipActive}`} />
          <div className={styles.pip} />
        </div>

        <div className={styles.brand}>
          <span>●</span> palmux2 へようこそ
        </div>
        <h1 className={styles.title}>セットアップ — 公開方法を選ぶ</h1>
        <p className={styles.lead}>
          この palmux をどう使うか選んでください。後から「デプロイ設定」でいつでも変更できます。
          install.sh にパラメータを渡して設定済みの場合、この画面は出ません。
        </p>

        {showPrivilegedNotice ? (
          nixOSHost ? (
            <div className={styles.privilegedNotice} data-testid="onboarding-rebuild">
              <strong>🔄 ドメイン/TLS を反映するには generation 切替が必要です。</strong>
              <p style={{ margin: '8px 0 0', fontSize: '13px' }}>
                下のボタンで <code>nixos-rebuild switch</code> を実行します（root 不要・polkit 認可）。
                適用中に palmux2 が再起動しますが、自動で再接続します。失敗しても旧 generation のまま
                （<code>--rollback</code> 可）なので安全です。
              </p>
              {rebuildState === 'done' ? (
                <p style={{ margin: '12px 0 0', color: 'var(--color-success)' }} data-testid="onboarding-rebuild-done">
                  ✓ 適用しました。公開ドメインで HTTPS が有効になります。
                </p>
              ) : (
                <button
                  type="button"
                  className={styles.btnPrimary}
                  data-testid="onboarding-rebuild-btn"
                  disabled={rebuildState === 'running'}
                  onClick={() => void handleRebuild()}
                  style={{ marginTop: '12px' }}
                >
                  {rebuildState === 'running' ? 'nixos-rebuild 実行中… (数分かかります)' : '適用 (nixos-rebuild)'}
                </button>
              )}
              <p style={{ margin: '10px 0 0', fontSize: '12px', color: 'var(--color-fg-muted)' }}>
                手動で行う場合（root シェル）:{' '}
                <code>systemctl start palmux-rebuild.service</code>
              </p>
            </div>
          ) : (
          <div className={styles.privilegedNotice}>
            <strong>🔐 ドメイン/TLS の確定には root が必要です。</strong>
            <p style={{ margin: '8px 0 0' }}>
              以下のコマンドを実行して TLS 証明書と Caddy の設定を確定してください:
            </p>
            <pre style={{ margin: '8px 0 0', fontFamily: 'var(--font-mono)', fontSize: '12px' }}>
              sudo palmux reconcile-system
            </pre>
            <p style={{ margin: '8px 0 0', fontSize: '12px', color: 'var(--color-fg-muted)' }}>
              または install.sh を再実行。アプリ/サーバ設定はこのまま GUI だけで反映されます。
            </p>
          </div>
          )
        ) : (
          <>
            <div className={styles.choice} data-testid="onboarding-mode">
              <button
                type="button"
                className={`${styles.opt} ${selectedMode === 'local' ? styles.optSelected : ''}`}
                data-testid="onboarding-mode-local"
                onClick={() => setSelectedMode('local')}
              >
                <div className={styles.optTitle}>ローカル / 非公開</div>
                <div className={styles.optDesc}>
                  この端末・LAN からのみ。公開ドメインも認証も不要。設定は全て GUI だけで完結（sudo なし）。
                </div>
              </button>
              <button
                type="button"
                className={`${styles.opt} ${selectedMode === 'public' ? styles.optSelected : ''}`}
                data-testid="onboarding-mode-public"
                onClick={() => setSelectedMode('public')}
              >
                <div className={styles.optTitle}>
                  公開（ドメイン + SSO）{selectedMode === 'public' ? ' ✓' : ''}
                </div>
                <div className={styles.optDesc}>
                  独自ドメインで HTTPS 公開。1回ログインの SSO + 公開サブドメイン。ドメイン/TLS の確定だけ特権操作を1回挟みます。
                </div>
              </button>
            </div>

            {selectedMode === 'public' && (
              <div className={styles.publicFields} data-testid="onboarding-public-fields">
                <div className={styles.wfield}>
                  <label htmlFor="onboarding-domain-input" className={styles.wfieldLabel}>
                    公開ドメイン
                  </label>
                  <input
                    id="onboarding-domain-input"
                    type="text"
                    className={styles.wfieldInput}
                    placeholder="例: palmux.example.net"
                    value={domain}
                    data-testid="onboarding-domain"
                    onChange={(e) => setDomain(e.target.value)}
                  />
                  <span className={styles.wfieldHelp}>
                    <code>*.&lt;domain&gt;</code> の wildcard DNS レコードが必要（公開サブドメイン用）。
                  </span>
                </div>
                <div className={styles.wfield}>
                  <label htmlFor="onboarding-cf-input" className={styles.wfieldLabel}>
                    Cloudflare API トークン
                  </label>
                  <input
                    id="onboarding-cf-input"
                    type="password"
                    className={styles.wfieldInput}
                    placeholder="DNS-01 wildcard TLS 用トークン"
                    value={cloudflareToken}
                    data-testid="onboarding-cloudflare-token"
                    onChange={(e) => setCloudflareToken(e.target.value)}
                  />
                  <span className={styles.wfieldHelp}>
                    <code>*.&lt;domain&gt;</code> の TLS 証明書を DNS-01 challenge で自動発行するのに使用。
                  </span>
                </div>
                <div className={styles.wfield}>
                  <label htmlFor="onboarding-user-input" className={styles.wfieldLabel}>
                    ログインユーザ
                  </label>
                  <input
                    id="onboarding-user-input"
                    type="text"
                    className={styles.wfieldInput}
                    value={authUser}
                    data-testid="onboarding-auth-user"
                    onChange={(e) => setAuthUser(e.target.value)}
                  />
                </div>
                <div className={styles.wfield}>
                  <label htmlFor="onboarding-pw-input" className={styles.wfieldLabel}>
                    ログインパスワード
                  </label>
                  <input
                    id="onboarding-pw-input"
                    type="password"
                    className={styles.wfieldInput}
                    placeholder="SSO ログイン用パスワード"
                    value={authPassword}
                    data-testid="onboarding-auth-password"
                    onChange={(e) => setAuthPassword(e.target.value)}
                  />
                </div>

                <div className={styles.bannerInfo} data-testid="onboarding-privileged-notice">
                  <span>{nixOSHost ? '🔄' : '🔐'}</span>
                  <span>
                    {nixOSHost ? (
                      <>
                        公開ドメイン/TLS の反映には generation 切替が要ります。次へ進むと{' '}
                        <code>nixos-rebuild switch</code> をその場でキックするボタンを出します（root 不要）。
                        アプリ/サーバ設定はこのまま GUI だけで反映されます。
                      </>
                    ) : (
                      <>
                        公開ドメイン/TLS の確定には root が必要です。次へ進むと{' '}
                        <code>sudo palmux reconcile-system</code>（or install.sh 再実行）の手順を案内します。
                        アプリ/サーバ設定はこのまま GUI だけで反映されます。
                      </>
                    )}
                  </span>
                </div>
              </div>
            )}
          </>
        )}

        {error && <p className={styles.errorText}>{error}</p>}

        <div className={styles.btnRow}>
          {showPrivilegedNotice ? (
            <>
              <button
                type="button"
                className={styles.btnGhost}
                data-testid="onboarding-back"
                onClick={handleBack}
              >
                戻る
              </button>
              <button
                type="button"
                className={styles.btnPrimary}
                onClick={() => { markSeen(); onClose() }}
              >
                完了
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                className={styles.btnGhost}
                data-testid="onboarding-back"
                onClick={handleBack}
              >
                戻る
              </button>
              <button
                type="button"
                className={styles.btnPrimary}
                data-testid="onboarding-next"
                onClick={handleNext}
                disabled={applying}
              >
                {applying ? '適用中…' : '次へ — 確認して適用'}
              </button>
              <button
                type="button"
                className={styles.btnGhost}
                data-testid="onboarding-skip"
                onClick={handleSkip}
              >
                スキップ（後で設定）
              </button>
            </>
          )}
        </div>
      </div>

      <p className={styles.footNote}>
        install.sh のパラメータ方式と、この GUI 方式は <strong>同じマスター設定</strong>
        （config.toml + secrets.env）に到達します。どちらでも選べます。
      </p>
    </div>
  )
}

// ─── Self-gating wrapper ──────────────────────────────────────────────────────

/** Checks configured state once at mount; shows wizard if not configured and not seen. */
export function OnboardingWizardGated() {
  const [shouldShow, setShouldShow] = useState(false)
  const [open, setOpen] = useState(false)

  useEffect(() => {
    const seen = (() => {
      try {
        return localStorage.getItem(LS_KEY) === '1'
      } catch {
        return false
      }
    })()
    if (seen) return

    void deployApi.get().then((d) => {
      if (!d.configured) {
        setShouldShow(true)
        setOpen(true)
      }
    }).catch(() => {
      // ignore — if deploy endpoint fails, don't show wizard
    })
  }, [])

  if (!shouldShow) return null

  return (
    <OnboardingWizard
      open={open}
      onClose={() => setOpen(false)}
    />
  )
}
