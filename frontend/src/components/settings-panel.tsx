/**
 * SettingsPanel — Sa53137
 *
 * Two-tab settings modal:
 *   - "アプリ設定" tab: global settings (PATCH /api/settings)
 *   - "デプロイ設定" tab: deploy/server config (GET+POST /api/deploy)
 *
 * Opened from ⌘K palette (settings / deploy-settings items).
 */

import { useCallback, useEffect, useState } from 'react'

import { api, deployApi, type DeployView, type ApplyResult } from '../lib/api'
import { usePalmuxStore, type GlobalSettings } from '../stores/palmux-store'
import { Modal } from './modal'
import { UserCommandsModal } from './user-commands-modal'

import styles from './settings-panel.module.css'

export type SettingsTab = 'app' | 'deploy'

interface Props {
  open: boolean
  onClose: () => void
  initialTab?: SettingsTab
}

// ─── App Settings Tab ────────────────────────────────────────────────────────

interface AppFormState {
  branchSortOrder: 'name' | 'activity'
  maxClaudeTabsPerBranch: number
  maxBashTabsPerBranch: number
  claudeDefaultMode: 'agent' | 'tui'
  defaultRuntimeKind: 'host' | 'incus-container'
  previewMaxBytes: number
  readPreviewLineCount: number
  attachmentUploadDir: string
  attachmentTtlDays: number
  autoWorktreePathPatterns: string
  subagentStaleAfterDays: number
}

function deriveAppForm(gs: GlobalSettings): AppFormState {
  return {
    branchSortOrder: gs.branchSortOrder ?? 'name',
    maxClaudeTabsPerBranch: gs.maxClaudeTabsPerBranch ?? 3,
    maxBashTabsPerBranch: gs.maxBashTabsPerBranch ?? 5,
    claudeDefaultMode: (gs as GlobalSettings & { claude?: { default_mode?: string } }).claude?.default_mode === 'tui' ? 'tui' : 'agent',
    defaultRuntimeKind: (gs as GlobalSettings & { defaultRuntime?: { kind?: string } }).defaultRuntime?.kind === 'incus-container' ? 'incus-container' : 'host',
    previewMaxBytes: gs.previewMaxBytes ?? 10485760,
    readPreviewLineCount: gs.readPreviewLineCount ?? 50,
    attachmentUploadDir: gs.attachmentUploadDir ?? '/tmp/palmux-uploads/',
    attachmentTtlDays: gs.attachmentTtlDays ?? 30,
    autoWorktreePathPatterns: (gs.autoWorktreePathPatterns ?? ['.claude/worktrees/*']).join('\n'),
    subagentStaleAfterDays: (gs as GlobalSettings & { subagentStaleAfterDays?: number }).subagentStaleAfterDays ?? 7,
  }
}

const EMPTY_USER_COMMANDS: NonNullable<NonNullable<GlobalSettings['palette']>['userCommands']> = []

function AppTab({ onManageUserCommands }: { onManageUserCommands: () => void }) {
  const globalSettings = usePalmuxStore((s) => s.globalSettings)
  // Select the raw value (stable reference); default to a module-level constant
  // so the selector never returns a fresh [] each render (which would make
  // Zustand's Object.is equality treat every render as a change → infinite
  // re-render loop, React error #185).
  const userCommands = usePalmuxStore(
    (s) => s.globalSettings.palette?.userCommands ?? EMPTY_USER_COMMANDS,
  )

  const [form, setForm] = useState<AppFormState>(() => deriveAppForm(globalSettings))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [dirty, setDirty] = useState(false)

  // Derive-state-from-props: re-prime form when globalSettings changes (WS
  // settings.updated) as long as we haven't locally dirtied the form.
  const [trackedSettings, setTrackedSettings] = useState(globalSettings)
  if (trackedSettings !== globalSettings && !dirty) {
    setTrackedSettings(globalSettings)
    setForm(deriveAppForm(globalSettings))
  }

  const update = useCallback(<K extends keyof AppFormState>(key: K, value: AppFormState[K]) => {
    setForm((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    setSaveSuccess(false)
  }, [])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    setSaveSuccess(false)
    try {
      const patterns = form.autoWorktreePathPatterns
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean)
      const body: Record<string, unknown> = {
        branchSortOrder: form.branchSortOrder,
        maxClaudeTabsPerBranch: form.maxClaudeTabsPerBranch,
        maxBashTabsPerBranch: form.maxBashTabsPerBranch,
        previewMaxBytes: form.previewMaxBytes,
        readPreviewLineCount: form.readPreviewLineCount,
        attachmentUploadDir: form.attachmentUploadDir,
        attachmentTtlDays: form.attachmentTtlDays,
        autoWorktreePathPatterns: patterns,
        subagentStaleAfterDays: form.subagentStaleAfterDays,
        claude: { default_mode: form.claudeDefaultMode },
        defaultRuntime: { kind: form.defaultRuntimeKind },
      }
      const updated = await api.patch<GlobalSettings>('/api/settings', body)
      usePalmuxStore.setState((state) => ({
        globalSettings: { ...state.globalSettings, ...updated },
      }))
      setDirty(false)
      setSaveSuccess(true)
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }, [form])

  const mibHelper = Math.round((form.previewMaxBytes / 1024 / 1024) * 10) / 10

  return (
    <div data-testid="settings-app-panel" className={styles.panel}>
      <p className={styles.help}>
        全デバイス共有のグローバル設定（<code>~/.config/palmux/settings.json</code>）。保存で即時反映され、再起動は不要です。
      </p>

      <div className={styles.sectionLabel}>タブ / 表示</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Workspace 並び順</span>
          <span className={styles.fieldKey}>branchSortOrder</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.seg} data-testid="field-branchSortOrder">
            <button
              type="button"
              className={`${styles.segBtn} ${form.branchSortOrder === 'name' ? styles.segBtnActive : ''}`}
              onClick={() => update('branchSortOrder', 'name')}
            >
              name
            </button>
            <button
              type="button"
              className={`${styles.segBtn} ${form.branchSortOrder === 'activity' ? styles.segBtnActive : ''}`}
              onClick={() => update('branchSortOrder', 'activity')}
            >
              activity
            </button>
          </div>
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Claude タブ上限 / Workspace</span>
          <span className={styles.fieldKey}>maxClaudeTabsPerBranch</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="number"
            className={styles.inputNumber}
            value={form.maxClaudeTabsPerBranch}
            min={1}
            max={10}
            data-testid="field-maxClaudeTabsPerBranch"
            onChange={(e) => update('maxClaudeTabsPerBranch', Math.max(1, parseInt(e.target.value) || 1))}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Bash タブ上限 / Workspace</span>
          <span className={styles.fieldKey}>maxBashTabsPerBranch</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="number"
            className={styles.inputNumber}
            value={form.maxBashTabsPerBranch}
            min={1}
            max={20}
            data-testid="field-maxBashTabsPerBranch"
            onChange={(e) => update('maxBashTabsPerBranch', Math.max(1, parseInt(e.target.value) || 1))}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Claude 既定モード</span>
          <span className={styles.fieldKey}>claude.default_mode</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.seg} data-testid="field-claudeDefaultMode">
            <button
              type="button"
              className={`${styles.segBtn} ${form.claudeDefaultMode === 'agent' ? styles.segBtnActive : ''}`}
              onClick={() => update('claudeDefaultMode', 'agent')}
            >
              agent
            </button>
            <button
              type="button"
              className={`${styles.segBtn} ${form.claudeDefaultMode === 'tui' ? styles.segBtnActive : ''}`}
              onClick={() => update('claudeDefaultMode', 'tui')}
            >
              tui
            </button>
          </div>
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>既定 Runtime</span>
          <span className={styles.fieldKey}>defaultRuntime.kind</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.seg} data-testid="field-defaultRuntime">
            <button
              type="button"
              className={`${styles.segBtn} ${form.defaultRuntimeKind === 'host' ? styles.segBtnActive : ''}`}
              onClick={() => update('defaultRuntimeKind', 'host')}
            >
              host
            </button>
            <button
              type="button"
              className={`${styles.segBtn} ${form.defaultRuntimeKind === 'incus-container' ? styles.segBtnActive : ''}`}
              onClick={() => update('defaultRuntimeKind', 'incus-container')}
            >
              incus-container
            </button>
          </div>
        </div>
      </div>

      <div className={styles.sectionLabel}>ファイル / プレビュー</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>プレビュー最大バイト</span>
          <span className={styles.fieldKey}>previewMaxBytes</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.rowInline}>
            <input
              type="number"
              className={styles.inputNumber}
              value={form.previewMaxBytes}
              data-testid="field-previewMaxBytes"
              onChange={(e) => update('previewMaxBytes', Math.max(1, parseInt(e.target.value) || 10485760))}
            />
            <span className={styles.dim}>= {mibHelper} MiB</span>
          </div>
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Read プレビュー行数</span>
          <span className={styles.fieldKey}>readPreviewLineCount</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="number"
            className={styles.inputNumber}
            value={form.readPreviewLineCount}
            data-testid="field-readPreviewLineCount"
            onChange={(e) => update('readPreviewLineCount', Math.max(1, parseInt(e.target.value) || 50))}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>添付アップロード先</span>
          <span className={styles.fieldKey}>attachmentUploadDir</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.attachmentUploadDir}
            data-testid="field-attachmentUploadDir"
            onChange={(e) => update('attachmentUploadDir', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>添付保持日数</span>
          <span className={styles.fieldKey}>attachmentTtlDays</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="number"
            className={styles.inputNumber}
            value={form.attachmentTtlDays}
            data-testid="field-attachmentTtlDays"
            onChange={(e) => update('attachmentTtlDays', Math.max(1, parseInt(e.target.value) || 30))}
          />
        </div>
      </div>

      <div className={styles.sectionLabel}>Worktree / Subagent</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>自動 worktree path パターン</span>
          <span className={styles.fieldKey}>autoWorktreePathPatterns</span>
          <span className={styles.fieldHint}>1行1パターン（glob）</span>
        </div>
        <div className={styles.fieldControl}>
          <textarea
            className={styles.textarea}
            value={form.autoWorktreePathPatterns}
            data-testid="field-autoWorktreePathPatterns"
            onChange={(e) => update('autoWorktreePathPatterns', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>Subagent stale 判定日数</span>
          <span className={styles.fieldKey}>subagentStaleAfterDays</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="number"
            className={styles.inputNumber}
            value={form.subagentStaleAfterDays}
            data-testid="field-subagentStaleAfterDays"
            onChange={(e) => update('subagentStaleAfterDays', Math.max(1, parseInt(e.target.value) || 7))}
          />
        </div>
      </div>

      <div className={styles.sectionLabel}>⌘K ユーザコマンド</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>palette.userCommands</span>
          <span className={styles.fieldHint}>既存の管理モーダルをこのタブに統合</span>
        </div>
        <div className={styles.fieldControl}>
          <span className={styles.dim} data-testid="field-userCommands-summary">
            {userCommands.length} 件登録済み
          </span>
          <button
            type="button"
            className={styles.btnSmallGhost}
            data-testid="manage-user-commands"
            onClick={onManageUserCommands}
          >
            ユーザコマンドを編集…
          </button>
        </div>
      </div>

      <div className={styles.btnRow}>
        <button
          type="button"
          className={styles.btnPrimary}
          data-testid="save-app-settings"
          onClick={save}
          disabled={saving}
        >
          {saving ? '保存中…' : '保存'}
        </button>
        {saveSuccess && (
          <span className={styles.tagLive} data-testid="app-save-status">
            ✓ 保存しました — 即時反映
          </span>
        )}
        {saveError && (
          <span className={styles.saveError} data-testid="app-save-error">
            {saveError}
          </span>
        )}
      </div>
    </div>
  )
}

// ─── Deploy Settings Tab ──────────────────────────────────────────────────────

const RESTART_CLASS_FIELDS = new Set(['addr', 'base_path', 'tmux_prefix', 'max_connections', 'basic_auth_user'])

interface DeployFormState {
  addr: string
  base_path: string
  tmux_prefix: string
  max_connections: number
  caddy_admin: string
  claude_bin: string
  claude_args: string
  domain: string
  basic_auth_user: string
  newPassword: string
  ssoSecret: string
  cloudflareToken: string
  showSsoInput: boolean
}

function deriveDeployForm(d: DeployView): DeployFormState {
  return {
    addr: d.server.addr,
    base_path: d.server.base_path,
    tmux_prefix: d.server.tmux_prefix,
    max_connections: d.server.max_connections,
    caddy_admin: d.server.caddy_admin,
    claude_bin: d.server.claude_bin,
    claude_args: d.server.claude_args,
    domain: d.public.domain,
    basic_auth_user: d.public.basic_auth_user,
    newPassword: '',
    ssoSecret: '',
    cloudflareToken: '',
    showSsoInput: false,
  }
}

function DeployTab() {
  const [deployView, setDeployView] = useState<DeployView | null>(null)
  const [form, setForm] = useState<DeployFormState | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [applying, setApplying] = useState(false)
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  // reloadKey bumps to re-trigger the load effect (reload-from-disk button).
  const [reloadKey, setReloadKey] = useState(0)

  const loadDeploy = useCallback(() => setReloadKey((k) => k + 1), [])

  // Fetch the current deploy view when the tab mounts and whenever the user
  // hits "reload from disk". The async work is fully inside the effect's inner
  // function, so the first state update lands in a later microtask — this is
  // the React-documented data-fetch-in-effect pattern, not a synchronous
  // cascading setState.
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const d = await deployApi.get()
        if (cancelled) return
        setDeployView(d)
        setForm(deriveDeployForm(d))
        setApplyResult(null)
        setApplyError(null)
        setLoadError(null)
      } catch (err: unknown) {
        if (!cancelled) setLoadError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [reloadKey])

  const updateForm = useCallback(<K extends keyof DeployFormState>(key: K, value: DeployFormState[K]) => {
    setForm((prev) => prev ? { ...prev, [key]: value } : prev)
  }, [])

  // Compute whether any restart-class fields changed
  const hasRestartChanges = (() => {
    if (!deployView || !form) return false
    if (
      form.addr !== deployView.server.addr ||
      form.base_path !== deployView.server.base_path ||
      form.tmux_prefix !== deployView.server.tmux_prefix ||
      form.max_connections !== deployView.server.max_connections ||
      form.basic_auth_user !== deployView.public.basic_auth_user
    ) return true
    if (form.newPassword.trim() !== '') return true
    if (form.ssoSecret.trim() !== '') return true
    return false
  })()
  void RESTART_CLASS_FIELDS // referenced for clarity

  const applyDeploy = useCallback(async () => {
    if (!form) return
    setApplying(true)
    setApplyError(null)
    setApplyResult(null)
    try {
      // First handle secrets if any were entered
      if (form.newPassword.trim() || form.ssoSecret.trim() || form.cloudflareToken.trim()) {
        await deployApi.rotateSecrets({
          ...(form.ssoSecret.trim() ? { ssoSecret: form.ssoSecret } : {}),
          ...(form.newPassword.trim() ? { password: form.newPassword } : {}),
          ...(form.cloudflareToken.trim() ? { token: form.cloudflareToken } : {}),
        })
      }
      const result = await deployApi.apply({
        server: {
          addr: form.addr,
          base_path: form.base_path,
          tmux_prefix: form.tmux_prefix,
          max_connections: form.max_connections,
          caddy_admin: form.caddy_admin,
          claude_bin: form.claude_bin,
          claude_args: form.claude_args,
        },
        public: {
          domain: form.domain,
          basic_auth_user: form.basic_auth_user,
        },
      })
      setApplyResult(result)
      // Reload the view to reflect server state
      const d = await deployApi.get()
      setDeployView(d)
      // keep form as-is (user can see the result)
    } catch (err: unknown) {
      setApplyError(err instanceof Error ? err.message : String(err))
    } finally {
      setApplying(false)
    }
  }, [form])

  const domainChanged = deployView && form && form.domain !== deployView.public.domain

  if (loadError) {
    return (
      <div data-testid="settings-deploy-panel" className={styles.panel}>
        <p className={styles.saveError}>読み込みエラー: {loadError}</p>
        <button type="button" className={styles.btnGhost} onClick={loadDeploy}>再試行</button>
      </div>
    )
  }

  if (!form || !deployView) {
    return (
      <div data-testid="settings-deploy-panel" className={styles.panel}>
        <p className={styles.loadingText}>読み込み中…</p>
      </div>
    )
  }

  return (
    <div data-testid="settings-deploy-panel" className={styles.panel}>
      {hasRestartChanges && (
        <div className={styles.bannerWarn} data-testid="restart-required-banner">
          <span>⟳</span>
          <span>
            未適用の変更に <strong>再起動が必要なもの</strong> が含まれます。
            下部の <strong>Apply</strong> 後に{' '}
            <code>systemctl --user restart palmux2</code> が必要です。
          </span>
        </div>
      )}

      <p className={styles.help}>
        サーバ/公開設定のマスター（<code>~/.config/palmux/config.toml</code>）。
        {' '}<span className={styles.tagHot}>即時</span>{' '}
        <span className={styles.tagRestart}>要再起動</span>{' '}
        <span className={styles.tagRoot}>要特権</span>{' '}
        のバッジで反映コストを示します。
      </p>

      <div className={styles.sectionLabel}>[server]</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            待受アドレス <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>server.addr</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.addr}
            data-testid="field-addr"
            onChange={(e) => updateForm('addr', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            ベースパス <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>server.base_path</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.base_path}
            data-testid="field-base_path"
            onChange={(e) => updateForm('base_path', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            tmux prefix <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>server.tmux_prefix</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.tmux_prefix}
            data-testid="field-tmux_prefix"
            onChange={(e) => updateForm('tmux_prefix', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            WS 最大接続 / Workspace <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>server.max_connections</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.rowInline}>
            <input
              type="number"
              className={styles.inputNumber}
              value={form.max_connections}
              data-testid="field-max_connections"
              onChange={(e) => updateForm('max_connections', parseInt(e.target.value) || 0)}
            />
            <span className={styles.dim}>0 = 無制限</span>
          </div>
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            Caddy admin API <span className={styles.tagHot}>即時</span>
          </span>
          <span className={styles.fieldKey}>server.caddy_admin</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.caddy_admin}
            data-testid="field-caddy_admin"
            onChange={(e) => updateForm('caddy_admin', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            claude バイナリ <span className={styles.tagHot}>即時</span>
          </span>
          <span className={styles.fieldKey}>server.claude_bin</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.claude_bin}
            data-testid="field-claude_bin"
            onChange={(e) => updateForm('claude_bin', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.sectionLabel}>[public] · 公開 / 認証</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            公開ドメイン <span className={styles.tagRoot}>要特権</span>
          </span>
          <span className={styles.fieldKey}>public.domain</span>
          <span className={styles.fieldHint}>TLS 証明書 + Caddy の書換が要るため特権操作</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.domain}
            data-testid="field-domain"
            onChange={(e) => updateForm('domain', e.target.value)}
          />
          {domainChanged && (
            <div className={styles.bannerInfo} data-testid="domain-root-notice">
              <span>
                この変更は root を要します。Apply で{' '}
                <code>palmux reconcile-system</code> の確認に進みます（or install.sh 再実行）。
              </span>
            </div>
          )}
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            BasicAuth ユーザ <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>public.basic_auth_user</span>
        </div>
        <div className={styles.fieldControl}>
          <input
            type="text"
            className={styles.input}
            value={form.basic_auth_user}
            data-testid="field-basic_auth_user"
            onChange={(e) => updateForm('basic_auth_user', e.target.value)}
          />
        </div>
      </div>

      <div className={styles.sectionLabel}>秘密（マスク表示・書込のみ）</div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            SSO 署名鍵 <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>secrets.env · PALMUX_SSO_SECRET</span>
          <span className={styles.fieldHint}>変更すると全セッションが無効化されます</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.rowInline}>
            <span className={styles.tagMasked} data-testid="secret-sso-status">
              {deployView.secrets.hasSsoSecret ? '設定済み ••••••••' : '未設定'}
            </span>
            <button
              type="button"
              className={styles.btnSmallGhost}
              data-testid="rotate-sso"
              onClick={() => updateForm('showSsoInput', !form.showSsoInput)}
            >
              ローテーション…
            </button>
          </div>
          {form.showSsoInput && (
            <input
              type="password"
              className={styles.input}
              placeholder="新しい SSO シークレット"
              value={form.ssoSecret}
              onChange={(e) => updateForm('ssoSecret', e.target.value)}
            />
          )}
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            ログインパスワード <span className={styles.tagRestart}>要再起動</span>
          </span>
          <span className={styles.fieldKey}>secrets.env · BASIC_AUTH_HASH (bcrypt)</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.rowInline}>
            <input
              type="password"
              className={styles.input}
              placeholder="新しいパスワード（空欄=変更なし）"
              value={form.newPassword}
              data-testid="field-new-password"
              onChange={(e) => updateForm('newPassword', e.target.value)}
            />
            {deployView.secrets.hasBasicAuthHash && (
              <span className={styles.tagMasked}>設定済み</span>
            )}
          </div>
        </div>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            Cloudflare API トークン <span className={styles.tagRoot}>要特権</span>
          </span>
          <span className={styles.fieldKey}>secrets · CLOUDFLARE_API_TOKEN</span>
          <span className={styles.fieldHint}>公開ドメインの wildcard TLS (DNS-01) に使用</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.rowInline}>
            <input
              type="password"
              className={styles.input}
              placeholder="新しいトークン（空欄=変更なし）"
              value={form.cloudflareToken}
              data-testid="field-cloudflare-token"
              onChange={(e) => updateForm('cloudflareToken', e.target.value)}
            />
            {deployView.secrets.hasCloudflareToken && (
              <span className={styles.tagMasked}>設定済み</span>
            )}
          </div>
        </div>
      </div>

      <div className={styles.btnRow}>
        <button
          type="button"
          className={styles.btnPrimary}
          data-testid="apply-deploy"
          onClick={applyDeploy}
          disabled={applying}
        >
          {applying ? '適用中…' : 'Apply（差分を反映）'}
        </button>
        <button
          type="button"
          className={styles.btnGhost}
          data-testid="reload-from-disk"
          onClick={loadDeploy}
          disabled={applying}
        >
          ディスクから再読込
        </button>
        {applyError && (
          <span className={styles.saveError}>{applyError}</span>
        )}
      </div>

      {applyResult && (
        <div className={styles.applyResult} data-testid="apply-result">
          <div className={styles.applyMessage}>{applyResult.message}</div>
          {applyResult.changes.length > 0 && (
            <div className={styles.changesList}>
              {applyResult.changes.map((c) => (
                <div key={c.field} className={styles.changeItem}>
                  <code>{c.field}</code>
                  {c.class === 'hot' && <span className={styles.tagHot}>即時</span>}
                  {c.class === 'restart' && <span className={styles.tagRestart}>要再起動</span>}
                  {c.class === 'root' && <span className={styles.tagRoot}>要特権</span>}
                </div>
              ))}
            </div>
          )}
          {applyResult.needRestart && (
            <p className={styles.help}>
              ⟳ <code>systemctl --user restart palmux2</code> または{' '}
              <code>palmux apply</code> で再起動してください。
            </p>
          )}
          {applyResult.needPrivilege && (
            <p className={styles.help}>
              🔐 ドメイン/TLS の確定には root が必要です。{' '}
              <code>sudo palmux reconcile-system</code> または install.sh を再実行してください。
            </p>
          )}
        </div>
      )}
    </div>
  )
}

// ─── SettingsPanel (root) ─────────────────────────────────────────────────────

export function SettingsPanel({ open, onClose, initialTab = 'app' }: Props) {
  const [activeTab, setActiveTab] = useState<SettingsTab>(initialTab)
  const [userCmdModalOpen, setUserCmdModalOpen] = useState(false)

  // Re-prime active tab when initialTab changes (palette opens with a specific tab)
  const [trackedInitialTab, setTrackedInitialTab] = useState(initialTab)
  if (trackedInitialTab !== initialTab) {
    setTrackedInitialTab(initialTab)
    setActiveTab(initialTab)
  }

  return (
    <>
      <Modal open={open} onClose={onClose} title="設定" width={720}>
        <div>
          <div className={styles.tabs} data-testid="settings-tabs">
            <button
              type="button"
              className={`${styles.tab} ${activeTab === 'app' ? styles.tabActive : ''}`}
              data-testid="settings-tab-app"
              onClick={() => setActiveTab('app')}
            >
              アプリ設定
            </button>
            <button
              type="button"
              className={`${styles.tab} ${activeTab === 'deploy' ? styles.tabActive : ''}`}
              data-testid="settings-tab-deploy"
              onClick={() => setActiveTab('deploy')}
            >
              デプロイ設定
            </button>
          </div>

          {activeTab === 'app' && (
            <AppTab onManageUserCommands={() => setUserCmdModalOpen(true)} />
          )}
          {activeTab === 'deploy' && <DeployTab />}
        </div>
      </Modal>

      <UserCommandsModal
        open={userCmdModalOpen}
        onClose={() => setUserCmdModalOpen(false)}
      />
    </>
  )
}
