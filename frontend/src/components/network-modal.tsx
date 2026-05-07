/**
 * NetworkModal — S034
 *
 * Shows the network isolation status for the current branch:
 *   - Detected listeners (ports inside the netns)
 *   - Public ports (exposed via slirp4netns port-forward)
 *
 * Triggered by clicking the 🛡 Isolated badge in the header.
 */

import { useCallback, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'

import { useNetnsStore } from '../stores/netns'
import styles from './network-modal.module.css'
import modalStyles from './modal.module.css'

interface NetworkModalProps {
  repoId: string
  branchId: string
  branchName?: string
  onClose: () => void
}

export function NetworkModal({ repoId, branchId, onClose }: NetworkModalProps) {
  const navigate = useNavigate()
  const {
    listeners: allListeners,
    ports: allPorts,
    slirpAvailable,
    fetchListeners,
    fetchPorts,
    exposePort,
    unexposePort,
  } = useNetnsStore()

  const listeners = allListeners[branchId] ?? []
  const ports = allPorts[branchId] ?? []

  useEffect(() => {
    void fetchListeners(repoId, branchId)
    void fetchPorts(repoId, branchId)
  }, [repoId, branchId, fetchListeners, fetchPorts])

  const handleExpose = useCallback(
    async (internalPort: number) => {
      try {
        await exposePort(repoId, branchId, internalPort)
        // Refresh listener list to update exposed badges.
        void fetchListeners(repoId, branchId)
      } catch (e) {
        console.error('expose port failed', e)
      }
    },
    [repoId, branchId, exposePort, fetchListeners]
  )

  const handleUnexpose = useCallback(
    async (hostPort: number) => {
      try {
        await unexposePort(repoId, branchId, hostPort)
      } catch (e) {
        console.error('unexpose port failed', e)
      }
    },
    [repoId, branchId, unexposePort]
  )

  const handleBackdropClick = useCallback(
    (e: React.MouseEvent) => {
      if (e.target === e.currentTarget) onClose()
    },
    [onClose]
  )

  const goToSettings = useCallback(() => {
    navigate('/settings/network')
    onClose()
  }, [navigate, onClose])

  return (
    <div className={modalStyles.overlay} role="dialog" aria-modal="true" aria-labelledby="net-modal-title" onClick={handleBackdropClick} data-testid="network-modal-overlay">
      <div className={modalStyles.card} style={{ width: 'min(600px, 92vw)' }} data-testid="network-modal">

        {/* Header */}
        <div className={styles.modalHeaderRow}>
          <h2 className={styles.modalHeaderTitle} id="net-modal-title">
            Network &middot; <span style={{ color: 'var(--color-accent-light)' }}>🛡 Isolated</span>
          </h2>
          <button className={styles.settingsLink} onClick={goToSettings} data-testid="network-modal-settings-link">
            Settings ↗
          </button>
          <button className={styles.modalCloseBtn} onClick={onClose} title="Close" data-testid="network-modal-close">✕</button>
        </div>

        <div className={modalStyles.body}>
          {/* slirp4netns missing warning */}
          {!slirpAvailable && (
            <div className={styles.slirpWarning} data-testid="network-slirp-warning">
              ⚠ slirp4netns not found — running in compatibility mode. Port forwarding is unavailable.
              Install <code>slirp4netns</code> to enable outbound connectivity and port exposure.
            </div>
          )}

          {/* Detected listeners */}
          <div className={styles.netSection}>
            <h3 className={styles.netSectionTitle}>Detected listeners</h3>

            {listeners.length === 0 ? (
              <p className={styles.netEmpty}>
                No listening ports detected inside the isolated network.
              </p>
            ) : (
              listeners.map((l) => (
                <div className={styles.netRow} key={l.port} data-testid="network-listener-row">
                  <span className={styles.netRowPort}>:{l.port}</span>
                  <span className={styles.netRowProc}>
                    {l.processName || '(unknown)'}
                    {l.pid ? <span style={{ color: 'var(--color-fg-dim)' }}> [{l.pid}]</span> : null}
                  </span>
                  {l.exposed ? (
                    <span className={styles.netExposedBadge} data-testid="network-exposed-badge">
                      :{l.hostPort}
                    </span>
                  ) : (
                    <button
                      className={styles.netExposeBtn}
                      onClick={() => handleExpose(l.port)}
                      title={`Expose :${l.port} on a host port`}
                      data-testid="network-listener-expose-btn"
                      disabled={!slirpAvailable}
                    >
                      Expose
                    </button>
                  )}
                </div>
              ))
            )}
          </div>

          {/* Public ports */}
          <div className={styles.netSection}>
            <h3 className={styles.netSectionTitle}>Public ports</h3>

            {ports.length === 0 ? (
              <p className={styles.netEmpty}>
                No ports are currently exposed to the host.
                Click &ldquo;Expose&rdquo; on a detected listener above.
              </p>
            ) : (
              ports.map((pm) => (
                <div className={styles.netRow} key={pm.hostPort} data-testid="network-port-row">
                  <span className={styles.netRowPort}>:{pm.hostPort}</span>
                  <span className={styles.netRowProc}>→ :{pm.internalPort}</span>
                  {pm.publicUrl && (
                    <a
                      className={styles.netFqdn}
                      href={pm.publicUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      title={pm.publicUrl}
                      data-testid="network-port-fqdn"
                    >
                      {pm.publicUrl.replace(/^https?:\/\//, '')}
                    </a>
                  )}
                  <button
                    className={styles.netRowOpen}
                    onClick={() => {
                      // S034 hotfix: open via the palmux UI's hostname (not
                      // "localhost"). When accessing palmux2 from a remote
                      // machine, "localhost" points to the user's own
                      // machine — not the server hosting the dev server.
                      // Strip the palmux port (window.location.host includes
                      // it) and use the host part with the forwarded port.
                      const host = window.location.hostname
                      window.open(`http://${host}:${pm.hostPort}`, '_blank')
                    }}
                    title={`Open ${window.location.hostname}:${pm.hostPort}`}
                    data-testid="network-port-open-btn"
                  >
                    ↗
                  </button>
                  <button
                    className={styles.netRowClose}
                    onClick={() => handleUnexpose(pm.hostPort)}
                    title={`Remove forward for :${pm.hostPort}`}
                    data-testid="network-port-remove-btn"
                  >
                    ✕
                  </button>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/** IsolatedBadge — shown in the header when isolation is active */
export function IsolatedBadge({ onClick, count = 0 }: { onClick: () => void; count?: number }) {
  return (
    <button
      className={styles.isolatedPill}
      onClick={onClick}
      title={
        count > 0
          ? `${count} listener${count === 1 ? '' : 's'} detected — click to manage public ports`
          : 'Network isolation active — click to manage public ports'
      }
      data-testid="isolated-badge"
    >
      🛡 Isolated{count > 0 ? ` · ${count}` : ''}
    </button>
  )
}
