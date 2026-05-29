// Shared non-component helpers used by the Sprint Dashboard screens.
// Lives outside view-header.tsx so the eslint react-refresh rule
// (only-export-components) stays satisfied.

import styles from '../sprint-view.module.css'

export function statusClass(kind: string): string {
  switch (kind) {
    case 'done':
      return styles.statusDone
    case 'in-progress':
      return styles.statusInProgress
    case 'blocked':
      return styles.statusBlocked
    case 'needs-human':
      return styles.statusNeedsHuman
    default:
      return styles.statusPending
  }
}

/** Returns the CSS Module class for a colored status pill (Story 1 table). */
export function statusPillClass(kind: string): string {
  const base = styles.statusPill
  switch (kind) {
    case 'done':
      return `${base} ${styles.pillDone}`
    case 'in-progress':
      return `${base} ${styles.pillInProgress}`
    case 'blocked':
      return `${base} ${styles.pillBlocked}`
    default:
      return `${base} ${styles.pillPending}`
  }
}
