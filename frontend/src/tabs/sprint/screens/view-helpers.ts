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
    case 'needs-human':
      return `${base} ${styles.pillBlocked}`
    case 'needs-user-review':
      return `${base} ${styles.pillNeedsReview}`
    default:
      return `${base} ${styles.pillPending}`
  }
}

/** CSS class for a pass/fail/warn verdict word. */
export function verdictClass(status: string): string {
  switch (String(status).toLowerCase()) {
    case 'pass':
    case 'ok':
    case 'passed':
      return styles.verdictPass
    case 'fail':
    case 'failed':
      return styles.verdictFail
    case 'warn':
    case 'needs_user_review':
    case 'needs-user-review':
      return styles.verdictWarn
    default:
      return styles.verdictNa
  }
}

/** CSS class for a severity chip (high / medium / low). */
export function severityClass(sev: string): string {
  switch (String(sev).toLowerCase()) {
    case 'high':
      return `${styles.sev} ${styles.sevHigh}`
    case 'medium':
    case 'med':
      return `${styles.sev} ${styles.sevMed}`
    default:
      return `${styles.sev} ${styles.sevLow}`
  }
}
