import { useEffect } from 'react'

import { usePalmuxStore } from '../../stores/palmux-store'

import styles from './update-toast.module.css'

// S6ab0ed: completion toast shown after the self-update reconnect handshake
// confirms the new version is live ("vX に更新しました"). Auto-dismisses.
export function UpdateToast() {
  const toast = usePalmuxStore((s) => s.updateToast)
  const clear = usePalmuxStore((s) => s.clearUpdateToast)

  useEffect(() => {
    if (!toast) return
    const t = setTimeout(() => clear(), 8000)
    return () => clearTimeout(t)
  }, [toast, clear])

  if (!toast) return null
  // Sfef725: when `message` is set (e.g. the incus-admin recover completion) it
  // is shown verbatim; otherwise the self-update "<version> に更新しました" form.
  return (
    <div className={styles.toast} data-testid="update-complete-toast" role="status">
      <span className={styles.check}>✓</span>
      <span>{toast.message ?? `${toast.version} に更新しました`}</span>
      <button className={styles.close} onClick={() => clear()} aria-label="閉じる">
        ×
      </button>
    </div>
  )
}
