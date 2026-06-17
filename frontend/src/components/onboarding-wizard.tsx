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
            ...(cloudflareToken.trim() ? { token: cloudflareToken } : {}),
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
                  <span>🔐</span>
                  <span>
                    公開ドメイン/TLS の確定には root が必要です。次へ進むと{' '}
                    <code>sudo palmux reconcile-system</code>（or install.sh 再実行）の手順を案内します。
                    アプリ/サーバ設定はこのまま GUI だけで反映されます。
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
