/**
 * SettingsPanel — Sa53137
 *
 * Two-tab settings modal:
 *   - "アプリ設定" tab: global settings (PATCH /api/settings)
 *   - "デプロイ設定" tab: deploy/server config (GET+POST /api/deploy)
 *
 * Opened from ⌘K palette (settings / deploy-settings items).
 */

import { useCallback, useEffect, useRef, useState } from 'react'

import { api, deployApi, type DeployView, type ApplyResult } from '../lib/api'
import { usePalmuxStore, type GlobalSettings } from '../stores/palmux-store'
import { AppsSection } from './apps-section'
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
  claudePermissionMode: 'default' | 'auto' | 'plan' | 'acceptEdits' | 'bypassPermissions'
  defaultRuntimeKind: 'host' | 'incus-container'
  previewMaxBytes: number
  readPreviewLineCount: number
  attachmentUploadDir: string
  attachmentTtlDays: number
  autoWorktreePathPatterns: string
  subagentStaleAfterDays: number
}

const PERM_MODES = ['default', 'auto', 'plan', 'acceptEdits', 'bypassPermissions'] as const
function normalizePermMode(v: string | undefined): AppFormState['claudePermissionMode'] {
  return (PERM_MODES as readonly string[]).includes(v ?? '')
    ? (v as AppFormState['claudePermissionMode'])
    : 'auto'
}

function deriveAppForm(gs: GlobalSettings): AppFormState {
  return {
    branchSortOrder: gs.branchSortOrder ?? 'name',
    maxClaudeTabsPerBranch: gs.maxClaudeTabsPerBranch ?? 3,
    maxBashTabsPerBranch: gs.maxBashTabsPerBranch ?? 5,
    claudeDefaultMode: (gs as GlobalSettings & { claude?: { default_mode?: string } }).claude?.default_mode === 'tui' ? 'tui' : 'agent',
    claudePermissionMode: normalizePermMode(
      (gs as GlobalSettings & { claude?: { permission_mode?: string } }).claude?.permission_mode,
    ),
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
        claude: { default_mode: form.claudeDefaultMode, permission_mode: form.claudePermissionMode },
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
          <span className={styles.fieldName}>Claude 権限モード</span>
          <span className={styles.fieldKey}>claude.permission_mode</span>
        </div>
        <div className={styles.fieldControl}>
          <div className={styles.seg} data-testid="field-claudePermissionMode">
            {PERM_MODES.map((pm) => (
              <button
                key={pm}
                type="button"
                className={`${styles.segBtn} ${form.claudePermissionMode === pm ? styles.segBtnActive : ''}`}
                onClick={() => update('claudePermissionMode', pm)}
              >
                {pm === 'bypassPermissions' ? 'bypass' : pm}
              </button>
            ))}
          </div>
          <div className={styles.fieldHint}>
            claude 起動時の <code>--permission-mode</code>。既定 <code>auto</code>。<code>bypass</code>
            （=bypassPermissions）は確認を全てスキップ（コンテナ/非 root で有効）。変更は次回 claude 再起動から適用。
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
  // Sd44947: [workspace] shared folders (absolute host paths, pending until Apply).
  sharedDirs: string[]
  newSharedDir: string
  sharedDirError: string | null
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
    sharedDirs: d.workspace?.sharedDirs ?? [],
    newSharedDir: '',
    sharedDirError: null,
  }
}

// validateSharedDir mirrors the server-side $HOME-scope rule (config.ExpandSharedDir)
// so the GUI can reject an out-of-$HOME path inline before Apply. Returns the
// expanded absolute path on success, or an error message.
function validateSharedDir(raw: string, home: string): { path?: string; error?: string } {
  const entry = raw.trim()
  if (!entry) return { error: 'パスを入力してください' }
  let abs = entry
  if (entry === '~') abs = home
  else if (entry.startsWith('~/')) abs = home.replace(/\/$/, '') + '/' + entry.slice(2)
  else if (entry.startsWith('~')) return { error: '~/（自分のホーム）のみ指定できます' }
  if (!abs.startsWith('/')) return { error: '絶対パスまたは ~/ で始まるパスを指定してください' }
  // Collapse a trailing slash for the scope check.
  const homeClean = home.replace(/\/$/, '')
  if (abs !== homeClean && !abs.startsWith(homeClean + '/')) {
    return { error: `パスは $HOME（${homeClean}）配下のみ許可されます` }
  }
  return { path: abs.replace(/\/$/, '') || '/' }
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
  // Sb14caa: NixOS appliance — kick `nixos-rebuild switch` to apply privileged
  // (domain/TLS) changes from the GUI instead of `sudo palmux reconcile-system`.
  const [rebuildState, setRebuildState] = useState<'idle' | 'running' | 'done' | 'failed'>('idle')
  // The apply result (and its "適用 (nixos-rebuild)" button) renders at the bottom
  // of a long scrollable modal, below the Apply button — so a click could look
  // like "nothing happened" when the result was just below the fold. Scroll it
  // into view whenever it appears.
  const applyResultRef = useRef<HTMLDivElement>(null)

  const loadDeploy = useCallback(() => setReloadKey((k) => k + 1), [])

  const handleRebuild = useCallback(async () => {
    setRebuildState('running')
    setApplyError(null)
    try {
      await deployApi.rebuild()
    } catch (err: unknown) {
      setRebuildState('failed')
      setApplyError(err instanceof Error ? err.message : String(err))
      return
    }
    const started = Date.now()
    const poll = async (): Promise<void> => {
      if (Date.now() - started > 15 * 60 * 1000) {
        setRebuildState('failed')
        setApplyError('nixos-rebuild timed out (15m). Check `journalctl -u palmux-rebuild`.')
        return
      }
      try {
        const st = await deployApi.rebuildStatus()
        if (st.active === 'failed' || (st.result && st.result !== 'success' && !st.running)) {
          setRebuildState('failed')
          setApplyError('nixos-rebuild failed — previous generation kept. See `journalctl -u palmux-rebuild`.')
          return
        }
        if (st.active === 'inactive' && st.result === 'success') {
          setRebuildState('done')
          return
        }
      } catch {
        // transient — palmux2 likely restarting from the switch; keep polling.
      }
      setTimeout(() => void poll(), 3000)
    }
    setTimeout(() => void poll(), 3000)
  }, [])

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

  // Sd44947: add the typed path to the pending shared-folder list (with inline
  // $HOME-scope validation), or remove one by index.
  const addSharedDir = useCallback(() => {
    setForm((prev) => {
      if (!prev || !deployView) return prev
      const { path, error } = validateSharedDir(prev.newSharedDir, deployView.workspace?.home ?? '')
      if (error || !path) return { ...prev, sharedDirError: error ?? 'invalid path' }
      if (prev.sharedDirs.includes(path)) {
        return { ...prev, sharedDirError: '既に追加済みです', newSharedDir: '' }
      }
      return { ...prev, sharedDirs: [...prev.sharedDirs, path], newSharedDir: '', sharedDirError: null }
    })
  }, [deployView])

  const removeSharedDir = useCallback((idx: number) => {
    setForm((prev) => prev ? { ...prev, sharedDirs: prev.sharedDirs.filter((_, i) => i !== idx) } : prev)
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
          // CLOUDFLARE_API_TOKEN (DNS-01), not the palmux auth token (PALMUX_TOKEN).
          ...(form.cloudflareToken.trim() ? { cloudflareToken: form.cloudflareToken } : {}),
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
        workspace: {
          sharedDirs: form.sharedDirs,
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

  // Bring the apply result / rebuild button into view so the click is never silent.
  useEffect(() => {
    if (applyResult || applyError) {
      applyResultRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }, [applyResult, applyError])

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
        <p className={styles.loadingText} data-testid="deploy-loading">読み込み中…</p>
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

      <div className={styles.sectionLabel}>
        [workspace] · 共有フォルダ <span className={styles.tagWorkspace}>即時 (稼働中コンテナへ live 反映)</span>
      </div>

      <div className={styles.bannerWarn} data-testid="shared-dirs-warning">
        <span>⚠</span>
        <span>
          ここに追加したフォルダは <strong>全 Workspace コンテナに露出</strong>します。
          <code>~/.claude</code> と同じく、認証情報を含むディレクトリは中の Claude / シェルから
          読み書き可能になります。信頼するパスだけを追加してください。
        </span>
      </div>

      <div className={styles.field}>
        <div className={styles.fieldLabel}>
          <span className={styles.fieldName}>
            共有フォルダ <span className={styles.tagWorkspace}>即時</span>
          </span>
          <span className={styles.fieldKey}>config.toml · [workspace] shared_dirs</span>
          <span className={styles.fieldHint}>
            incus profile <code>palmux-shared</code> の device として全コンテナへ bind-mount されます。source 不在のパスは自動 skip。
          </span>
        </div>
        <div className={styles.fieldControl}>
          {form.sharedDirs.length === 0 ? (
            <div className={styles.sfEmpty} data-testid="shared-dirs-list">
              共有フォルダはまだありません。下の入力欄から <code>~/.infisical</code> のようなパスを追加できます。
            </div>
          ) : (
            <div className={styles.sfList} data-testid="shared-dirs-list">
              {form.sharedDirs.map((dir, i) => (
                <div key={dir} className={styles.sfRow}>
                  <span className={styles.sfPath}>{dir}</span>
                  <button
                    type="button"
                    className={styles.sfRemove}
                    data-testid={`shared-dir-remove-${i}`}
                    title="削除"
                    onClick={() => removeSharedDir(i)}
                  >
                    ✕
                  </button>
                </div>
              ))}
            </div>
          )}
          <div className={styles.sfAdd}>
            <input
              type="text"
              className={styles.input}
              placeholder="~/.infisical のような $HOME 配下のパス"
              value={form.newSharedDir}
              data-testid="shared-dir-input"
              onChange={(e) => updateForm('newSharedDir', e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addSharedDir()
                }
              }}
            />
            <button type="button" className={styles.btnGhost} data-testid="shared-dir-add" onClick={addSharedDir}>
              ＋ 追加
            </button>
          </div>
          {form.sharedDirError && (
            <div className={styles.sfInlineError} data-testid="shared-dir-error">
              <span>✕</span>
              <span>{form.sharedDirError}</span>
            </div>
          )}
          <span className={styles.help}>
            追加は一覧に <strong>保留 (pending)</strong> として入り、下部の <strong>Apply</strong> で profile へ書込 + 稼働中コンテナに反映されます。
          </span>
        </div>
      </div>

      {/* S41bdf2: 1アプリ=1カード（install + auth-folder 共有 従属トグル）。
          Self-contained (own GET/POST /api/apps); not part of the deploy Apply. */}
      <AppsSection />

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
          <span className={styles.saveError} data-testid="deploy-apply-error">{applyError}</span>
        )}
      </div>

      {applyResult && (
        <div ref={applyResultRef} className={styles.applyResult} data-testid="apply-result">
          <div className={styles.applyMessage}>{applyResult.message}</div>
          {applyResult.changes.length > 0 && (
            <div className={styles.changesList}>
              {applyResult.changes.map((c) => (
                <div key={c.field} className={styles.changeItem}>
                  <code>{c.field}</code>
                  {c.class === 'hot' && <span className={styles.tagHot}>即時</span>}
                  {c.class === 'workspace' && <span className={styles.tagWorkspace}>即時 (全コンテナ)</span>}
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
            deployView.nixOSHost ? (
              <div className={styles.help} data-testid="deploy-rebuild">
                <p style={{ margin: 0 }}>
                  🔄 ドメイン/TLS の反映には generation 切替が必要です（NixOS）。下のボタンで{' '}
                  <code>nixos-rebuild switch</code> を実行します（root 不要・適用中に palmux2 が再起動→自動再接続・失敗時は旧 generation 維持）。
                </p>
                {rebuildState === 'done' ? (
                  <p style={{ margin: '8px 0 0', color: 'var(--color-success)' }} data-testid="deploy-rebuild-done">
                    ✓ 適用しました。
                  </p>
                ) : (
                  <button
                    type="button"
                    className={styles.btnPrimary}
                    data-testid="deploy-rebuild-btn"
                    disabled={rebuildState === 'running'}
                    onClick={() => void handleRebuild()}
                    style={{ marginTop: '8px' }}
                  >
                    {rebuildState === 'running' ? 'nixos-rebuild 実行中…' : '適用 (nixos-rebuild)'}
                  </button>
                )}
                <p style={{ margin: '8px 0 0', fontSize: '12px', color: 'var(--color-fg-muted)' }}>
                  手動(root): <code>systemctl start palmux-rebuild.service</code>
                </p>
              </div>
            ) : (
              <p className={styles.help}>
                🔐 ドメイン/TLS の確定には root が必要です。{' '}
                <code>sudo palmux reconcile-system</code> または install.sh を再実行してください。
              </p>
            )
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
