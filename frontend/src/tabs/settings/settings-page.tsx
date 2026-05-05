/**
 * SettingsPage — /settings/network (S034)
 *
 * The first global settings page in Palmux v2. Covers:
 *   - Default network isolation for new repos
 *   - Caddy reverse-proxy integration (toggle + FQDN template + config paths)
 *
 * Other settings (palette commands etc.) remain in their respective modals
 * for now — see S035 for the full settings migration.
 */

import { useCallback, useEffect, useState } from 'react'

import { api } from '../../lib/api'
import { usePalmuxStore } from '../../stores/palmux-store'
import styles from './settings-page.module.css'

export function NetworkSettingsPage() {
  const globalSettings = usePalmuxStore((s) => s.globalSettings)
  const reloadSettings = usePalmuxStore((s) => s.reloadSettings)

  // Local form state — initialised from global settings.
  const [defaultIsolate, setDefaultIsolate] = useState(
    globalSettings.networkIsolation?.defaultIsolate ?? false
  )
  const [caddyEnabled, setCaddyEnabled] = useState(
    globalSettings.networkIsolation?.caddy?.enabled ?? false
  )
  const [fqdnTemplate, setFqdnTemplate] = useState(
    globalSettings.networkIsolation?.caddy?.fqdnTemplate ?? ''
  )
  const [configPath, setConfigPath] = useState(
    globalSettings.networkIsolation?.caddy?.configPath ?? ''
  )
  const [reloadCmd, setReloadCmd] = useState(
    globalSettings.networkIsolation?.caddy?.reloadCmd ?? ''
  )
  const [saving, setSaving] = useState(false)
  const [savedMsg, setSavedMsg] = useState('')

  const caddyAvailable = globalSettings.networkIsolation?.caddy?.available !== false

  useEffect(() => {
    // Sync from global settings when they reload (e.g. from WS).
    setDefaultIsolate(globalSettings.networkIsolation?.defaultIsolate ?? false)
    setCaddyEnabled(globalSettings.networkIsolation?.caddy?.enabled ?? false)
    setFqdnTemplate(globalSettings.networkIsolation?.caddy?.fqdnTemplate ?? '')
    setConfigPath(globalSettings.networkIsolation?.caddy?.configPath ?? '')
    setReloadCmd(globalSettings.networkIsolation?.caddy?.reloadCmd ?? '')
  }, [globalSettings])

  const handleSave = useCallback(async () => {
    setSaving(true)
    setSavedMsg('')
    try {
      await api.patch('/api/settings', {
        networkIsolation: {
          defaultIsolate,
          caddy: {
            enabled: caddyEnabled,
            fqdnTemplate,
            configPath,
            reloadCmd,
          },
        },
      })
      // Trigger a reload so the WS event or direct fetch updates the store.
      if (reloadSettings) await reloadSettings()
      setSavedMsg('Saved!')
      setTimeout(() => setSavedMsg(''), 2500)
    } catch (e) {
      setSavedMsg(`Error: ${(e as Error).message}`)
    } finally {
      setSaving(false)
    }
  }, [defaultIsolate, caddyEnabled, fqdnTemplate, configPath, reloadCmd, reloadSettings])

  return (
    <div className={styles.page} data-testid="settings-network-page">
      <h1 className={styles.pageTitle}>Settings → Network</h1>

      {/* Network Isolation section */}
      <div className={styles.settingsSection}>
        <h2 className={styles.settingsSectionTitle}>Network isolation</h2>
        <p className={styles.settingsSectionSub}>
          Per-worktree Linux network namespaces prevent port conflicts between
          concurrent dev servers. Each isolated worktree gets its own{' '}
          <code>localhost</code> — use &ldquo;Expose&rdquo; to forward specific
          ports to the host.
        </p>

        <div className={styles.settingsRow}>
          <label className={styles.toggle} htmlFor="default-isolate" title="Isolate new worktrees by default">
            <input
              id="default-isolate"
              type="checkbox"
              checked={defaultIsolate}
              onChange={(e) => setDefaultIsolate(e.target.checked)}
              data-testid="settings-default-isolate-toggle"
            />
            <span className={styles.toggleTrack} />
          </label>
          <div className={styles.settingsRowBody}>
            <div className={styles.settingsRowLabel}>Isolate network for new repos by default</div>
            <div className={styles.settingsRowHelp}>
              When ON, newly added repos default to{' '}
              <code>isolateNetwork: &quot;on&quot;</code>. Existing repos are
              unaffected — toggle per-repo from the Repository Settings in the Drawer.
            </div>
          </div>
        </div>
      </div>

      {/* Caddy section */}
      <div className={styles.settingsSection} data-testid="settings-caddy-section">
        <h2 className={styles.settingsSectionTitle}>Caddy reverse-proxy (optional)</h2>
        <p className={styles.settingsSectionSub}>
          When enabled, each port you expose via palmux gets a stable FQDN +
          TLS certificate via Caddy. Your main Caddyfile must{' '}
          <code>import</code> the snippet file.
        </p>

        {/* Caddy availability indicator */}
        {!caddyAvailable && (
          <div
            className={`${styles.statusPill} ${styles.statusWarning}`}
            style={{ marginBottom: 16 }}
            data-testid="caddy-not-available-warning"
          >
            ⚠ <code>caddy</code> binary not found in PATH — Caddy integration unavailable
          </div>
        )}
        {caddyAvailable && (
          <div
            className={`${styles.statusPill} ${styles.statusOk}`}
            style={{ marginBottom: 16 }}
            data-testid="caddy-available-pill"
          >
            ✓ Caddy installed
          </div>
        )}

        <div className={styles.settingsRow}>
          <label className={styles.toggle} htmlFor="caddy-enabled">
            <input
              id="caddy-enabled"
              type="checkbox"
              checked={caddyEnabled}
              onChange={(e) => setCaddyEnabled(e.target.checked)}
              disabled={!caddyAvailable}
              data-testid="settings-caddy-enabled-toggle"
            />
            <span className={styles.toggleTrack} />
          </label>
          <div className={styles.settingsRowBody}>
            <div className={styles.settingsRowLabel}>Enable Caddy integration</div>
            <div className={styles.settingsRowHelp}>
              Automatically create a <code>reverse_proxy</code> route when you
              expose a port from the Network modal.
            </div>
          </div>
        </div>

        {caddyEnabled && (
          <>
            <div className={styles.settingsRow}>
              <div className={styles.settingsRowBody}>
                <div className={styles.settingsRowLabel}>FQDN template</div>
                <div className={styles.settingsRowHelp}>
                  Placeholders: <code>{'{{.repo}}'}</code>{' '}
                  <code>{'{{.branch}}'}</code>{' '}
                  <code>{'{{.port}}'}</code>{' '}
                  <code>{'{{.hostPort}}'}</code>
                </div>
                <input
                  className={styles.settingsInput}
                  value={fqdnTemplate}
                  onChange={(e) => setFqdnTemplate(e.target.value)}
                  placeholder="{repo}-{branch}-{port}.example.com"
                  data-testid="settings-caddy-fqdn-template"
                />
              </div>
            </div>

            <div className={styles.settingsRow}>
              <div className={styles.settingsRowBody}>
                <div className={styles.settingsRowLabel}>Snippet file path</div>
                <div className={styles.settingsRowHelp}>
                  Palmux writes Caddy route stanzas here. Your main Caddyfile
                  must <code>import</code> this file.
                </div>
                <input
                  className={styles.settingsInput}
                  value={configPath}
                  onChange={(e) => setConfigPath(e.target.value)}
                  placeholder="~/.config/palmux/caddy/active.caddyfile"
                  data-testid="settings-caddy-config-path"
                />
              </div>
            </div>

            <div className={styles.settingsRow}>
              <div className={styles.settingsRowBody}>
                <div className={styles.settingsRowLabel}>Reload command</div>
                <div className={styles.settingsRowHelp}>
                  Shell command run after the snippet is updated.
                </div>
                <input
                  className={styles.settingsInput}
                  value={reloadCmd}
                  onChange={(e) => setReloadCmd(e.target.value)}
                  placeholder="caddy reload --config ~/.config/caddy/Caddyfile"
                  data-testid="settings-caddy-reload-cmd"
                />
              </div>
            </div>
          </>
        )}
      </div>

      <button
        className={styles.saveBtn}
        onClick={handleSave}
        disabled={saving}
        data-testid="settings-save-btn"
      >
        {saving ? 'Saving…' : 'Save changes'}
      </button>
      {savedMsg && (
        <p className={styles.savedMsg} data-testid="settings-saved-msg">
          {savedMsg}
        </p>
      )}
    </div>
  )
}
