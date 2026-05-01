// Shared types for the Files-tab viewer dispatcher (S010).
//
// Each viewer (Markdown / Monaco / Image / Drawio / TooLarge) takes the
// same minimal contract so `dispatcher.ts` can pick one purely from the
// file metadata + body. Keeping the props narrow means new viewers can
// be added later (PDF, Mermaid live render, etc.) without touching the
// FilesView caller — register the routing in `dispatcher.ts` alone.

import type { FileBody } from '../types'

export interface ViewerProps {
  /** Per-branch Files-API base, e.g. `/api/repos/<r>/branches/<b>/files`.
   *  Image/Drawio viewers prefer fetching the raw bytes via this base
   *  so the cache layer is shared with the rest of the tab. */
  apiBase: string
  /** Worktree-relative path of the open file, e.g. `cmd/foo/main.go`. */
  path: string
  /** Resolved file body. `null` while loading. */
  body: FileBody | null
  /** 1-based line to scroll to (Monaco / line-aware viewers only). */
  lineNum?: number
}
