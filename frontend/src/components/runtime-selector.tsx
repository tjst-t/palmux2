// runtime-selector.tsx — S8478ca-5: Runtime selector radiogroup.
//
// Renders a radiogroup for choosing between "host" and "incus-container"
// runtimes. Incus availability is driven by GET /api/runtimes (caps).
//
// Props:
//   value     — currently-selected kind (or undefined = not yet chosen)
//   caps      — RuntimeCaps from GET /api/runtimes
//   onChange  — called when the user picks a new kind (may be async; parent
//               handles the PATCH and surfaces errors via `error` prop)
//   error     — inline error string to render below the options
//   disabled  — disables all interaction (e.g. during a pending PATCH)
//
// data-testids (per gui-spec):
//   runtime-selector                    — the radiogroup root
//   runtime-option-host                 — host radio when enabled
//   runtime-option-incus-container      — incus-container radio when enabled
//   runtime-option-incus-container-disabled — incus-container when unavailable
//   runtime-incus-install-tooltip       — tooltip shown on hover of disabled
//   runtime-selector-error              — inline error message

import { useState } from 'react'

import type { RuntimeCaps } from '../lib/api'

import styles from './runtime-selector.module.css'

interface Props {
  value?: 'host' | 'incus-container'
  caps: RuntimeCaps | null
  onChange: (kind: 'host' | 'incus-container') => void
  error?: string | null
  disabled?: boolean
}

export function RuntimeSelector({ value, caps, onChange, error, disabled }: Props) {
  const [tooltipVisible, setTooltipVisible] = useState(false)

  const incusEntry = caps?.kinds.find((k) => k.kind === 'incus-container')
  const incusAvailable = incusEntry?.available ?? false
  const incusReason = incusEntry?.reason ?? 'Incus is not installed on this host'

  // Default when value is not yet set: incus if available, else host.
  const effective = value ?? (incusAvailable ? 'incus-container' : 'host')

  return (
    <div
      className={styles.fieldset}
      data-testid="runtime-selector"
      role="radiogroup"
      aria-label="Workspace runtime"
    >
      <div className={styles.legend}>Runtime — where this Workspace runs</div>

      {/* incus-container option */}
      {incusAvailable ? (
        <div
          className={`${styles.option} ${effective === 'incus-container' ? styles.optionSelected : ''} ${disabled ? styles.disabled : ''}`}
          role="radio"
          aria-checked={effective === 'incus-container'}
          tabIndex={disabled ? -1 : 0}
          data-testid="runtime-option-incus-container"
          onClick={() => { if (!disabled) onChange('incus-container') }}
          onKeyDown={(e) => {
            if (!disabled && (e.key === 'Enter' || e.key === ' ')) onChange('incus-container')
          }}
        >
          <span className={styles.radio} />
          <div className={styles.body}>
            <div className={styles.nameRow}>
              incus-container
              <span className={styles.pill}>isolated</span>
              <span className={`${styles.pill} ${styles.pillDefault}`}>default</span>
            </div>
            <div className={styles.meta}>Incus detected</div>
            <div className={styles.desc}>
              Runs in an unprivileged Incus container. Keeps <code>npm -g</code>/
              <code>apt</code>, build artifacts &amp; ports off the host. <code>~/ghq</code>{' '}
              + auth are shared.
            </div>
          </div>
        </div>
      ) : (
        <div className={styles.tooltipAnchor}
          onMouseEnter={() => setTooltipVisible(true)}
          onMouseLeave={() => setTooltipVisible(false)}
        >
          <div
            className={`${styles.option} ${styles.disabled}`}
            role="radio"
            aria-checked={false}
            aria-disabled="true"
            tabIndex={-1}
            data-testid="runtime-option-incus-container-disabled"
          >
            <span className={styles.radio} />
            <div className={styles.body}>
              <div className={styles.nameRow}>
                incus-container
                <span className={styles.pill}>isolated</span>
              </div>
              <div className={styles.meta}>Incus not installed</div>
              <div className={styles.desc}>
                Install Incus to enable container isolation.
              </div>
            </div>
          </div>
          {tooltipVisible && (
            <div className={styles.tooltip} data-testid="runtime-incus-install-tooltip">
              Incus not available — {incusReason}. Install incus to enable this runtime.
            </div>
          )}
        </div>
      )}

      {/* host option */}
      <div
        className={`${styles.option} ${effective === 'host' ? styles.optionSelected : ''} ${disabled ? styles.disabled : ''}`}
        role="radio"
        aria-checked={effective === 'host'}
        tabIndex={disabled ? -1 : 0}
        data-testid="runtime-option-host"
        onClick={() => { if (!disabled) onChange('host') }}
        onKeyDown={(e) => {
          if (!disabled && (e.key === 'Enter' || e.key === ' ')) onChange('host')
        }}
      >
        <span className={styles.radio} />
        <div className={styles.body}>
          <div className={styles.nameRow}>host</div>
          <div className={styles.meta}>no isolation</div>
          <div className={styles.desc}>
            Runs directly on the host — legacy behaviour. Shares ports &amp; packages with
            everything else.
          </div>
        </div>
      </div>

      {/* inline error */}
      {error && (
        <div className={styles.error} data-testid="runtime-selector-error">
          {error}
        </div>
      )}
    </div>
  )
}
