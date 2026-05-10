// Sdd4ce1-5-1 / 5-3: Open Repository — Runtime selector modal.
//
// Shown immediately after the user picks (or clones) a repository in the
// Open Repository modal. Records the chosen runtime as the per-repo
// default and opens the primary Workspace. Per prototype-review.json
// round 2 + the new AC-Sdd4ce1-5-3, this modal is shown EVERY TIME a
// repo is opened — there is no "Don't ask again" checkbox; settings'
// defaultRuntime is editable only from the Settings page.
//
// data-testid contract:
//   open-repo-runtime-modal, selected-repo, selected-repo-change,
//   runtime-radio-group, runtime-option-<kind>,
//   network-select, image-select, lxd-not-installed-banner,
//   cancel-btn, open-btn

import { useState } from 'react'

import { api } from '../lib/api'
import type { RuntimeConfig, RuntimeKind } from '../lib/api'

import { RuntimeSelector, useLXDAvailability } from './runtime-selector'
import styles from './repo-picker.module.css'
import selectorStyles from './runtime-selector.module.css'

interface Props {
  open: boolean
  /** Repo metadata to display. */
  repoLabel: string
  repoId: string
  /** Triggered by the user clicking [Cancel] or pressing Esc. */
  onCancel: () => void
  /** Called once the runtime selection is confirmed AND the per-repo
   *  default has been persisted. The caller is responsible for opening
   *  the primary Workspace afterwards (it has the Repository object
   *  from the original Open call). */
  onConfirm: (cfg: RuntimeConfig) => void
  /** Optional: jump back to the repo selector. */
  onChangeRepo?: () => void
}

export function RepoOpenRuntimeModal({ open, repoLabel, repoId, onCancel, onConfirm, onChangeRepo }: Props) {
  const lxd = useLXDAvailability(open)
  const lxdReady = lxd.available === true
  const initialKind: RuntimeKind = lxdReady ? 'lxd-container' : 'host'
  const [kind, setKind] = useState<RuntimeKind | string>(initialKind)
  const [image, setImage] = useState('ghcr.io/tjst-t/palmux-workspace:default')
  const [network, setNetwork] = useState('bridged')
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  if (!open) return null

  const lxdAvailableForOpts = lxd.available !== false
  const isHostFallback = lxd.available === false

  // host-only runtimes need no image / network.
  const showLXDFields = kind.startsWith('lxd-')

  const handleConfirm = async () => {
    setSubmitting(true)
    setErr(null)
    const cfg: RuntimeConfig = { kind }
    if (kind.startsWith('lxd-')) {
      cfg.image = image
      cfg.network = { mode: network }
    }
    try {
      await api.patch(`/api/repos/${encodeURIComponent(repoId)}/default-runtime`, cfg)
      onConfirm(cfg)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className={styles.overlay} onClick={onCancel} data-testid="open-repo-runtime-modal">
      <div
        className={styles.card}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="open-repo-runtime-title"
      >
        <div className={styles.header}>
          <h2 className={styles.title} id="open-repo-runtime-title">Open Repository</h2>
          <p className={styles.sub}>Pick the runtime that will host the primary Workspace.</p>
        </div>

        <div className={styles.runtimeBody}>
          <div className={styles.field}>
            <label className={styles.label}>Selected</label>
            <div className={styles.selectedRepoChip} data-testid="selected-repo">
              <span className={styles.chipLabel}>REPO</span>
              <span className={styles.chipPath}>{repoLabel}</span>
              {onChangeRepo && (
                <button
                  type="button"
                  className={styles.chipChange}
                  data-testid="selected-repo-change"
                  onClick={onChangeRepo}
                  disabled={submitting}
                >
                  Change…
                </button>
              )}
            </div>
          </div>

          <div className={styles.field}>
            <label className={styles.label}>Runtime</label>
            <RuntimeSelector
              value={kind}
              onChange={setKind}
              lxdAvailable={lxdAvailableForOpts}
              lxdReason={lxd.reason}
            />
            {isHostFallback && !lxd.loading && (
              <p className={selectorStyles.banner}>
                LXD is not installed — <code>host</code> is auto-selected as the only available runtime.
              </p>
            )}
          </div>

          {showLXDFields && (
            <div className={styles.twoCol}>
              <label className={styles.selectField}>
                <span className={styles.label}>Network</span>
                <select
                  className={styles.selectInput}
                  value={network}
                  onChange={(e) => setNetwork(e.target.value)}
                  data-testid="network-select"
                  disabled={isHostFallback || submitting}
                >
                  <option value="bridged">bridged</option>
                  <option value="host-netns">host-netns</option>
                  <option value="tailnet">tailnet</option>
                </select>
              </label>
              <label className={styles.selectField}>
                <span className={styles.label}>Image</span>
                <input
                  className={styles.selectInput}
                  type="text"
                  value={image}
                  onChange={(e) => setImage(e.target.value)}
                  data-testid="image-select"
                  disabled={isHostFallback || submitting}
                />
              </label>
            </div>
          )}

          <p className={styles.helpText}>
            The selected runtime applies to the <b>primary Workspace</b> and is recorded as the
            <b> per-repo default</b> for any worktrees added later. You can change it anytime via
            right-click → <i>Change runtime…</i>
          </p>

          {err && <pre className={styles.errorBox}>{err}</pre>}
        </div>

        <div className={styles.footer}>
          <span className={styles.footerTip}>You'll see this picker every time a Repository is opened.</span>
          <button
            className={styles.btnGhost}
            data-testid="cancel-btn"
            onClick={onCancel}
            disabled={submitting}
          >
            Cancel
          </button>
          <button
            className={styles.btnPrimary}
            data-testid="open-btn"
            onClick={handleConfirm}
            disabled={submitting || lxd.loading}
          >
            {submitting ? 'Opening…' : 'Open'}
          </button>
        </div>
      </div>
    </div>
  )
}
