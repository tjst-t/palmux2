// uploads-store — global, app-level upload queue for the Files tab.
//
// Lives in a Zustand store so uploads keep running while the user
// navigates away from the Files tab (or even to a different
// repository / branch). The worker is started lazily when the first
// item is enqueued and runs in the SPA's lifetime — when the user
// reloads the page, in-flight uploads are cancelled (a hotfix-scoped
// limitation; restoring across reloads would need server-side jobs).
//
// Concurrency: `MAX_CONCURRENT` files in flight at once. Each file
// is POSTed independently to /upload as multipart, with the relative
// path included in the form (so folder uploads preserve directory
// structure — auto-mkdir on the server side).

import { create } from 'zustand'

const MAX_CONCURRENT = 3
const COMPLETED_RETENTION_MS = 30_000 // keep "done" jobs around briefly so the UI can show summaries

export type UploadStatus = 'queued' | 'uploading' | 'done' | 'error'

export interface UploadItem {
  id: string
  /** Worktree-relative target path (incl. subdirs for folder uploads). */
  path: string
  size: number
  status: UploadStatus
  /** 0–1 progress for the in-flight item. */
  progress: number
  /** Server response error or thrown error message. */
  error?: string
}

export interface UploadJob {
  id: string
  repoId: string
  branchId: string
  /** Absolute Files-view directory the user was sitting in when they
   *  triggered the upload. Used to (a) join with each file's relative
   *  path on the server, and (b) decide whether to refresh a Files
   *  view that the user is currently looking at. */
  baseDir: string
  /** Human-readable label for the activity menu. */
  label: string
  items: UploadItem[]
  createdAt: number
  doneAt?: number
}

interface UploadsState {
  jobs: UploadJob[]
  /** Items currently in flight (across all jobs). */
  inFlight: number
  /** True once the worker loop is registered. Prevents double-spawn. */
  workerStarted: boolean
}

interface UploadsActions {
  enqueue: (
    repoId: string,
    branchId: string,
    baseDir: string,
    files: { file: File; relativePath: string }[],
  ) => string
  /** Drop a finished job from the list (user-initiated dismiss). */
  dismissJob: (jobId: string) => void
  /** Drop ALL finished/error jobs at once. */
  clearCompleted: () => void
  /** Cancel a queued/uploading item — currently a soft cancel: we mark
   *  it errored so the worker skips it. In-flight requests are not
   *  aborted (out of scope for the hotfix). */
  cancelItem: (jobId: string, itemId: string) => void
}

let nextId = 1
const newId = () => `up-${Date.now()}-${nextId++}`

function joinPath(base: string, rel: string): string {
  const b = base.replace(/^\/+|\/+$/g, '')
  const r = rel.replace(/^\/+/, '')
  if (!b) return r
  return `${b}/${r}`
}

export const useUploadsStore = create<UploadsState & UploadsActions>((set, get) => ({
  jobs: [],
  inFlight: 0,
  workerStarted: false,

  enqueue: (repoId, branchId, baseDir, files) => {
    if (files.length === 0) return ''
    const jobId = newId()
    const items: UploadItem[] = files.map((f) => ({
      id: newId(),
      path: joinPath(baseDir, f.relativePath),
      size: f.file.size,
      status: 'queued',
      progress: 0,
    }))
    files.forEach((f, i) => fileByItemId.set(items[i].id, f.file))
    const label =
      files.length === 1
        ? files[0].relativePath
        : `${files.length} items → ${baseDir || '/'}`
    set((s) => ({
      jobs: [
        ...s.jobs,
        {
          id: jobId,
          repoId,
          branchId,
          baseDir,
          label,
          items,
          createdAt: Date.now(),
        },
      ],
    }))
    if (!get().workerStarted) {
      set({ workerStarted: true })
      startWorker()
    }
    pump()
    return jobId
  },

  dismissJob: (jobId) =>
    set((s) => ({ jobs: s.jobs.filter((j) => j.id !== jobId) })),

  clearCompleted: () =>
    set((s) => ({
      jobs: s.jobs.filter(
        (j) => !j.items.every((i) => i.status === 'done' || i.status === 'error'),
      ),
    })),

  cancelItem: (jobId, itemId) =>
    set((s) => ({
      jobs: s.jobs.map((j) => {
        if (j.id !== jobId) return j
        return {
          ...j,
          items: j.items.map((it) =>
            it.id === itemId && it.status === 'queued'
              ? { ...it, status: 'error', error: 'cancelled' }
              : it,
          ),
        }
      }),
    })),
}))

// Side-channel map of item.id → File. We can't put `File` directly
// into the store (Zustand expects serialisable shapes for devtools &
// it would defeat React's re-render minimisation). The map only
// holds files until they're uploaded, then we delete the entry.
const fileByItemId = new Map<string, File>()

function startWorker() {
  // Periodic dust cleaner — purges fully-completed jobs after a
  // retention window so the UI doesn't accumulate stale entries.
  if (typeof window === 'undefined') return
  window.setInterval(() => {
    const now = Date.now()
    useUploadsStore.setState((s) => ({
      jobs: s.jobs.filter((j) => {
        const allDone = j.items.every((i) => i.status === 'done' || i.status === 'error')
        if (!allDone) return true
        const finishedAt = j.doneAt ?? j.createdAt
        return now - finishedAt < COMPLETED_RETENTION_MS
      }),
    }))
  }, 5000)
}

function pump() {
  const state = useUploadsStore.getState()
  if (state.inFlight >= MAX_CONCURRENT) return
  // Find next queued item across all jobs.
  for (const job of state.jobs) {
    for (const item of job.items) {
      if (item.status !== 'queued') continue
      const file = fileByItemId.get(item.id)
      if (!file) {
        // Orphaned (shouldn't happen) — mark errored so we don't loop.
        markItem(job.id, item.id, { status: 'error', error: 'file ref lost' })
        continue
      }
      uploadOne(job, item, file)
      // Try to fill any remaining slots.
      if (useUploadsStore.getState().inFlight >= MAX_CONCURRENT) return
    }
  }
}

async function uploadOne(job: UploadJob, item: UploadItem, file: File) {
  bumpInFlight(1)
  markItem(job.id, item.id, { status: 'uploading', progress: 0 })

  const xhr = new XMLHttpRequest()
  const url = `/api/repos/${encodeURIComponent(job.repoId)}/branches/${encodeURIComponent(job.branchId)}/files/upload`
  const fd = new FormData()
  fd.append('path', item.path)
  fd.append('overwrite', '1')
  fd.append('file', file, file.name)

  await new Promise<void>((resolve) => {
    xhr.open('POST', url, true)
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return
      const p = e.loaded / e.total
      markItem(job.id, item.id, { progress: p })
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        markItem(job.id, item.id, { status: 'done', progress: 1 })
      } else {
        let msg = `HTTP ${xhr.status}`
        try {
          const body = JSON.parse(xhr.responseText) as { error?: string }
          if (body?.error) msg = body.error
        } catch {
          // ignore
        }
        markItem(job.id, item.id, { status: 'error', error: msg })
      }
      resolve()
    }
    xhr.onerror = () => {
      markItem(job.id, item.id, { status: 'error', error: 'network error' })
      resolve()
    }
    xhr.send(fd)
  })

  fileByItemId.delete(item.id)
  bumpInFlight(-1)
  // Mark job's doneAt when all items finished.
  useUploadsStore.setState((s) => ({
    jobs: s.jobs.map((j) => {
      if (j.id !== job.id) return j
      const allFinished = j.items.every(
        (i) => i.status === 'done' || i.status === 'error',
      )
      return allFinished && !j.doneAt ? { ...j, doneAt: Date.now() } : j
    }),
  }))
  pump()
}

function bumpInFlight(delta: number) {
  useUploadsStore.setState((s) => ({ inFlight: Math.max(0, s.inFlight + delta) }))
}

function markItem(jobId: string, itemId: string, patch: Partial<UploadItem>) {
  useUploadsStore.setState((s) => ({
    jobs: s.jobs.map((j) => {
      if (j.id !== jobId) return j
      return {
        ...j,
        items: j.items.map((i) => (i.id === itemId ? { ...i, ...patch } : i)),
      }
    }),
  }))
}

// Aggregate / derived computations are intentionally NOT exposed as
// store selectors — Zustand v5 uses `Object.is` equality, so a
// selector returning a fresh `{...}` each call would re-render
// subscribers on every store update (regardless of whether the
// derived values changed). Consumers compute aggregates locally via
// `useMemo` over `jobs`.
