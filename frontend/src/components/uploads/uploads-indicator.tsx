// hotfix: header indicator for the cross-tab uploads queue. Shows a
// compact "↑ N" chip while uploads are in flight; clicking it opens a
// dropdown with per-file status. Stays mounted in the header so users
// can monitor uploads no matter which tab/branch they navigate to.

import { useEffect, useMemo, useRef, useState } from 'react'

import {
  useUploadsStore,
  type UploadItem,
} from '../../stores/uploads-store'

import styles from './uploads-indicator.module.css'

function fmtBytes(n: number): string {
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}K`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}M`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)}G`
}

export function UploadsIndicator() {
  const jobs = useUploadsStore((s) => s.jobs)
  const dismissJob = useUploadsStore((s) => s.dismissJob)
  const clearCompleted = useUploadsStore((s) => s.clearCompleted)
  // Compute aggregates locally — passing a derived-object selector to
  // useUploadsStore would return a new ref on every store update and
  // re-render us in a loop (zustand uses Object.is for equality).
  const agg = useMemo(() => {
    let total = 0
    let uploaded = 0
    let inFlight = 0
    let done = 0
    let errored = 0
    for (const j of jobs) {
      for (const i of j.items) {
        total += i.size
        if (i.status === 'done') {
          uploaded += i.size
          done++
        } else if (i.status === 'uploading') {
          uploaded += Math.round(i.size * i.progress)
          inFlight++
        } else if (i.status === 'error') {
          errored++
        }
      }
    }
    return { total, uploaded, inFlight, done, error: errored }
  }, [jobs])
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)

  // Close the dropdown when clicking outside.
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current) return
      if (!wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  if (jobs.length === 0) return null

  const totalItems = jobs.reduce((n, j) => n + j.items.length, 0)
  const activeOrPending = agg.inFlight + (totalItems - agg.done - agg.error)
  const pct =
    agg.total > 0 ? Math.round((agg.uploaded / agg.total) * 100) : 0
  const allDone = activeOrPending === 0

  return (
    <div className={styles.wrap} ref={wrapRef}>
      <button
        type="button"
        className={styles.chip}
        data-testid="uploads-indicator"
        data-state={allDone ? 'done' : 'active'}
        onClick={() => setOpen((o) => !o)}
        title={
          allDone
            ? `${agg.done} uploaded${agg.error > 0 ? `, ${agg.error} failed` : ''}`
            : `Uploading ${activeOrPending} of ${totalItems} (${pct}%)`
        }
      >
        <span className={styles.arrow}>↑</span>
        {!allDone ? (
          <span className={styles.count}>{activeOrPending}</span>
        ) : agg.error > 0 ? (
          <span className={styles.countError}>!{agg.error}</span>
        ) : (
          <span className={styles.countDone}>✓</span>
        )}
      </button>
      {open && (
        <div className={styles.dropdown} role="dialog" aria-label="Uploads">
          <div className={styles.dropdownHeader}>
            <span>Uploads</span>
            <button
              type="button"
              className={styles.linkBtn}
              onClick={() => clearCompleted()}
            >
              Clear finished
            </button>
          </div>
          <ul className={styles.jobList}>
            {jobs.map((j) => {
              const done = j.items.filter((i) => i.status === 'done').length
              const failed = j.items.filter((i) => i.status === 'error').length
              const finishedAll =
                done + failed === j.items.length && j.items.length > 0
              return (
                <li key={j.id} className={styles.job}>
                  <div className={styles.jobHeader}>
                    <span className={styles.jobLabel} title={j.label}>
                      {j.label}
                    </span>
                    <span className={styles.jobMeta}>
                      {done}/{j.items.length}
                      {failed > 0 && ` (${failed} failed)`}
                    </span>
                    {finishedAll && (
                      <button
                        type="button"
                        className={styles.linkBtn}
                        onClick={() => dismissJob(j.id)}
                        title="Dismiss"
                      >
                        ×
                      </button>
                    )}
                  </div>
                  <ul className={styles.itemList}>
                    {j.items.slice(0, 6).map((it) => (
                      <ItemRow key={it.id} item={it} />
                    ))}
                    {j.items.length > 6 && (
                      <li className={styles.more}>
                        … and {j.items.length - 6} more
                      </li>
                    )}
                  </ul>
                </li>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}

function ItemRow({ item }: { item: UploadItem }) {
  const status = item.status
  return (
    <li className={styles.item} data-status={status}>
      <span className={styles.itemPath} title={item.path}>
        {item.path}
      </span>
      <span className={styles.itemStatus}>
        {status === 'queued' && 'queued'}
        {status === 'uploading' && `${Math.round(item.progress * 100)}%`}
        {status === 'done' && '✓'}
        {status === 'error' && (item.error ?? 'error')}
      </span>
      <span className={styles.itemSize}>{fmtBytes(item.size)}</span>
    </li>
  )
}
