// Sdd4ce1: shared <RuntimeSelector /> radio group used by RepoPicker
// (Open Repository modal) and WorkspaceCreateModal. Mirrors the prototype:
//   prototype/sdd4ce1-open-repo-runtime.html
//   prototype/sdd4ce1-workspace-create-runtime.html
// Round-2 user feedback removed all runtime icons; this component is
// strictly text-only. data-testid attributes match the prototype contract
// in docs/sprint-logs/Sdd4ce1/prototype-review.json.
//
// AC-Sdd4ce1-5-1 / AC-Sdd4ce1-5-2 / AC-Sdd4ce1-5-3.

import { useEffect, useState } from 'react'

import { api } from '../lib/api'
import type { LXDAvailability, RuntimeKind } from '../lib/api'

import styles from './runtime-selector.module.css'

export interface RuntimeOption {
  kind: RuntimeKind | string
  /** Optional badge — "DEFAULT" / "REPO DEFAULT" / "⚠ NO ISOLATION". */
  badge?: { label: string; tone: 'default' | 'warn' }
  description: string
  /** Right-side meta (e.g. "~1s start" / "remote"). */
  meta?: string
}

export const DEFAULT_OPTIONS: RuntimeOption[] = [
  {
    kind: 'lxd-container',
    badge: { label: 'DEFAULT', tone: 'default' },
    description: 'Lightweight LXD system container. Host stays clean — package installs and dev servers are sandboxed.',
    meta: '~1s start',
  },
  {
    kind: 'lxd-vm',
    description: 'LXD virtual machine. Stronger isolation (own kernel) at the cost of memory and start time.',
    meta: '~15s start',
  },
  {
    kind: 'lxd-remote',
    description: 'Container on a registered remote LXD host. Use for GPU / heavy build machines.',
    meta: 'remote',
  },
  {
    kind: 'ssh-remote',
    description: 'Run directly on a remote machine via SSH (no LXD on the remote required).',
    meta: 'remote',
  },
  {
    kind: 'host',
    badge: { label: '⚠ NO ISOLATION', tone: 'warn' },
    description: 'In-process on the host. Existing palmux behavior — exposes the host to dev-server ports and global installs.',
    meta: 'instant',
  },
]

interface Props {
  /** Currently-selected runtime kind. */
  value: RuntimeKind | string
  onChange: (kind: RuntimeKind | string) => void
  /** Override the option list. Used by WorkspaceCreateModal to inject a
   *  "REPO DEFAULT" badge on the per-repo default. */
  options?: RuntimeOption[]
  /** When false (LXD not installed), all `lxd-*` options are disabled and
   *  a tooltip+banner explains the install path. host stays selectable. */
  lxdAvailable?: boolean
  /** Reason from /api/runtime/lxd/available — surfaced in the banner. */
  lxdReason?: string
}

/** RuntimeSelector — text-only radio group of runtime kinds. */
export function RuntimeSelector({ value, onChange, options = DEFAULT_OPTIONS, lxdAvailable = true, lxdReason }: Props) {
  return (
    <>
      <div
        className={styles.fieldset}
        role="radiogroup"
        aria-label="Runtime"
        data-testid="runtime-radio-group"
      >
        {options.map((opt) => {
          const isLXD = opt.kind.startsWith('lxd-')
          const disabled = isLXD && !lxdAvailable
          const selected = opt.kind === value
          const className = [
            styles.option,
            selected ? styles.optionSelected : '',
            disabled ? styles.optionDisabled : '',
          ]
            .filter(Boolean)
            .join(' ')
          const handleClick = () => {
            if (disabled) return
            onChange(opt.kind)
          }
          return (
            <button
              type="button"
              key={opt.kind}
              className={className}
              role="radio"
              aria-checked={selected}
              aria-disabled={disabled}
              data-runtime={opt.kind}
              data-selected={selected}
              data-disabled={disabled || undefined}
              data-testid={`runtime-option-${opt.kind}`}
              onClick={handleClick}
              title={disabled ? `requires LXD${lxdReason ? ` — ${lxdReason}` : ''}` : undefined}
            >
              <span className={styles.radio} aria-hidden="true" />
              <div>
                <span className={styles.name}>{opt.kind}</span>
                {opt.badge && (
                  <span
                    className={`${styles.badge} ${
                      opt.badge.tone === 'warn' ? styles.badgeWarn : styles.badgeDefault
                    }`}
                  >
                    {opt.badge.label}
                  </span>
                )}
                {disabled && (
                  <span className={`${styles.badge} ${styles.badgeWarn}`}>requires LXD</span>
                )}
                <div className={styles.desc}>{opt.description}</div>
              </div>
              <span className={styles.meta}>{disabled ? 'unavailable' : opt.meta ?? ''}</span>
            </button>
          )
        })}
      </div>
      {!lxdAvailable && (
        <div className={styles.banner} data-testid="lxd-not-installed-banner">
          LXD is not installed on this host — <code>lxd-*</code> options are disabled.
          See <a href="https://documentation.ubuntu.com/lxd/en/latest/installing/" target="_blank" rel="noreferrer">
            installation guide
          </a>{lxdReason ? ` (${lxdReason})` : ''}.
        </div>
      )}
    </>
  )
}

/** useLXDAvailability fetches /api/runtime/lxd/available once when `enabled`
 *  flips true. Returns { available, reason, loading, error }. */
export function useLXDAvailability(enabled: boolean): {
  available: boolean | null
  reason?: string
  loading: boolean
  error?: string
} {
  const [state, setState] = useState<{
    available: boolean | null
    reason?: string
    loading: boolean
    error?: string
  }>({ available: null, loading: false })

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    setState({ available: null, loading: true })
    void api
      .get<LXDAvailability>('/api/runtime/lxd/available')
      .then((res) => {
        if (cancelled) return
        setState({ available: res.available, reason: res.reason, loading: false })
      })
      .catch((err) => {
        if (cancelled) return
        setState({
          available: false,
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        })
      })
    return () => {
      cancelled = true
    }
  }, [enabled])

  return state
}
