// Sdd4ce1-5-2: Workspace creation modal with Runtime selector.
//
// Shown when the user creates a new worktree (gwq add) from the
// BranchPicker. Defaults to the per-repo defaultRuntime; falls back to
// global defaultRuntime; falls back to host. Per AC-Sdd4ce1-5-3 the
// modal is shown EVERY TIME a worktree is created — there is no "use
// default" shortcut here.
//
// data-testid contract: workspace-create-modal, ws-branch-input,
// ws-path-preview, runtime-radio-group, network-select, image-select,
// vm-extras, vm-mem-input, vm-cpu-input, cancel-btn, create-btn.

import { useEffect, useState } from 'react'

import { api } from '../lib/api'
import type { BranchRuntimeResponse, RuntimeConfig, RuntimeKind } from '../lib/api'

import { RuntimeSelector, useLXDAvailability, DEFAULT_OPTIONS } from './runtime-selector'
import styles from './repo-picker.module.css'

interface Props {
  open: boolean
  repoId: string
  /** Pre-fill the branch input. Optional. */
  initialBranchName?: string
  onCancel: () => void
  /** Called after the runtime override is persisted. The caller is
   *  responsible for the actual `gwq add` + branch open. The `cfg`
   *  passed back is the chosen runtime. */
  onConfirm: (params: { branchName: string; runtime: RuntimeConfig }) => void
}

export function WorkspaceCreateModal({ open, repoId, initialBranchName, onCancel, onConfirm }: Props) {
  const lxd = useLXDAvailability(open)
  const [branchName, setBranchName] = useState(initialBranchName ?? '')
  const [kind, setKind] = useState<RuntimeKind | string>('host')
  const [image, setImage] = useState('ghcr.io/tjst-t/palmux-workspace:default')
  const [network, setNetwork] = useState('bridged')
  const [memory, setMemory] = useState('4096')
  const [cpus, setCPUs] = useState('2')
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [perRepoDefault, setPerRepoDefault] = useState<RuntimeConfig | null>(null)

  // Fetch the per-repo default and seed `kind` from it so the user lands on
  // the most likely choice (priority 9: defaults that respect the user's
  // prior choices).
  useEffect(() => {
    if (!open || !repoId) return
    let cancelled = false
    setBranchName(initialBranchName ?? '')
    setErr(null)
    void (async () => {
      try {
        // We piggy-back on a primary-branch runtime fetch to learn the
        // per-repo default + global. If the repo has no Open branches yet,
        // this returns 404 and we fall back to the LXD-availability-based
        // default.
        const repos = await api.get<Array<{ id: string; openBranches: Array<{ id: string }> }>>(
          '/api/repos',
        )
        const repo = repos.find((r) => r.id === repoId)
        const branchID = repo?.openBranches[0]?.id
        if (!branchID) return
        const r = await api.get<BranchRuntimeResponse>(
          `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchID)}/runtime`,
        )
        if (cancelled) return
        if (r.per_repo) {
          setPerRepoDefault(r.per_repo)
          if (r.per_repo.kind) setKind(r.per_repo.kind)
          if (r.per_repo.image) setImage(r.per_repo.image)
          if (r.per_repo.network?.mode) setNetwork(r.per_repo.network.mode)
        } else if (r.global.kind) {
          setKind(r.global.kind)
        }
      } catch {
        // Ignore — the user can still pick. The default `host` is safe.
      }
    })()
    return () => {
      cancelled = true
    }
  }, [open, repoId, initialBranchName])

  // Once LXD availability arrives, prefer lxd-container over host when
  // the per-repo default is empty AND LXD is available.
  useEffect(() => {
    if (!open || perRepoDefault) return
    if (lxd.available === true && kind === 'host') {
      setKind('lxd-container')
    }
  }, [lxd.available, open, perRepoDefault, kind])

  if (!open) return null

  const lxdAvailable = lxd.available !== false
  const showLXDFields = kind.startsWith('lxd-')
  const showVMFields = kind === 'lxd-vm'

  // Build runtime options — clone DEFAULT_OPTIONS but inject "REPO DEFAULT"
  // badge when the per-repo default matches.
  const options = DEFAULT_OPTIONS.map((opt) => {
    if (perRepoDefault && opt.kind === perRepoDefault.kind) {
      return { ...opt, badge: { label: 'REPO DEFAULT', tone: 'default' as const } }
    }
    return opt
  })

  const pathPreview = branchName ? `~/worktrees/.../${branchName.replace(/\//g, '-')}` : '~/worktrees/.../'

  const handleSubmit = async () => {
    if (!branchName.trim()) {
      setErr('branch name is required')
      return
    }
    setSubmitting(true)
    setErr(null)
    try {
      const cfg: RuntimeConfig = { kind }
      if (showLXDFields) {
        cfg.image = image
        cfg.network = { mode: network }
      }
      if (showVMFields) {
        cfg.resources = {
          memory_mib: parseInt(memory, 10) || 0,
          cpu_count: parseInt(cpus, 10) || 0,
        }
      }
      onConfirm({ branchName: branchName.trim(), runtime: cfg })
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className={styles.overlay} onClick={onCancel} data-testid="workspace-create-modal">
      <div
        className={styles.card}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="ws-create-title"
      >
        <div className={styles.header}>
          <h2 className={styles.title} id="ws-create-title">New Workspace</h2>
          <p className={styles.sub}>Create a worktree and pick its runtime.</p>
        </div>

        <div className={styles.runtimeBody}>
          <div className={styles.field}>
            <label className={styles.label} htmlFor="ws-branch">Branch / worktree name</label>
            <input
              id="ws-branch"
              className={styles.selectInput}
              type="text"
              value={branchName}
              onChange={(e) => setBranchName(e.target.value)}
              placeholder="feat/my-feature"
              data-testid="ws-branch-input"
              autoFocus
              disabled={submitting}
            />
            <div className={styles.selectedRepoChip} data-testid="ws-path-preview">
              <span className={styles.chipLabel}>PATH</span>
              <span className={styles.chipPath}>{pathPreview}</span>
            </div>
          </div>

          <div className={styles.field}>
            <label className={styles.label}>Runtime</label>
            <RuntimeSelector
              value={kind}
              onChange={setKind}
              options={options}
              lxdAvailable={lxdAvailable}
              lxdReason={lxd.reason}
            />
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
                  disabled={submitting}
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
                  disabled={submitting}
                />
              </label>
            </div>
          )}

          {showVMFields && (
            <div className={styles.twoCol} data-testid="vm-extras">
              <label className={styles.selectField}>
                <span className={styles.label}>Memory (MiB)</span>
                <input
                  className={styles.selectInput}
                  type="number"
                  value={memory}
                  onChange={(e) => setMemory(e.target.value)}
                  data-testid="vm-mem-input"
                  disabled={submitting}
                />
              </label>
              <label className={styles.selectField}>
                <span className={styles.label}>CPUs</span>
                <input
                  className={styles.selectInput}
                  type="number"
                  value={cpus}
                  onChange={(e) => setCPUs(e.target.value)}
                  data-testid="vm-cpu-input"
                  disabled={submitting}
                />
              </label>
            </div>
          )}

          {err && <pre className={styles.errorBox}>{err}</pre>}
        </div>

        <div className={styles.footer}>
          <button className={styles.btnGhost} data-testid="cancel-btn" onClick={onCancel} disabled={submitting}>
            Cancel
          </button>
          <button
            className={styles.btnPrimary}
            data-testid="create-btn"
            onClick={handleSubmit}
            disabled={submitting || !branchName.trim()}
          >
            {submitting ? 'Creating…' : 'Create Workspace'}
          </button>
        </div>
      </div>
    </div>
  )
}
