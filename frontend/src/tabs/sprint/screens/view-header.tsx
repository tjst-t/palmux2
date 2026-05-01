// Shared view header — title + offline indicator + Refresh button.
// Each of the five Sprint Dashboard screens renders one of these so the
// 4-layer refresh contract is uniform across the tab.

import styles from '../sprint-view.module.css'

interface ViewHeaderProps {
  title: string
  offline: boolean
  loading: boolean
  onRefresh: () => void
  testIdPrefix: string
  children?: React.ReactNode
}

export function ViewHeader({ title, offline, loading, onRefresh, testIdPrefix, children }: ViewHeaderProps) {
  return (
    <div className={styles.viewHeader} data-testid={`${testIdPrefix}-header`}>
      <h2 className={styles.viewTitle}>{title}</h2>
      <div className={styles.viewActions}>
        {children}
        {offline && (
          <span className={styles.offlineBadge} data-testid={`${testIdPrefix}-offline`}>
            offline
          </span>
        )}
        <button
          type="button"
          className={styles.iconButton}
          onClick={onRefresh}
          disabled={loading}
          data-testid={`${testIdPrefix}-refresh`}
          aria-label="Refresh"
        >
          {loading ? '...' : '⟳ Refresh'}
        </button>
      </div>
    </div>
  )
}

export function ParseErrorsBanner({ errors }: { errors?: { section: string; detail: string }[] }) {
  if (!errors || errors.length === 0) return null
  return (
    <div className={styles.parseErrors}>
      <h4>Markdown parse warnings ({errors.length})</h4>
      <ul>
        {errors.map((e, i) => (
          <li key={i}>
            <strong>{e.section}</strong>: {e.detail}
          </li>
        ))}
      </ul>
    </div>
  )
}

export function ErrorBanner({ message }: { message: string | null }) {
  if (!message) return null
  return <div className={styles.errorBanner}>{message}</div>
}

// statusClass moved to view-helpers.ts (eslint react-refresh rule).
