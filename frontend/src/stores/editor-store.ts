// editor-store — S011 in-memory state for the Files-tab editor.
//
// The Files tab maintains *per-file* editor state (mode, dirty buffer,
// last-known ETag) keyed by `{repoId, branchId, path}`. Keeping it in
// a Zustand store rather than `FilesView` local state means:
//
//   - Tab switches inside the same branch (e.g. user jumps from Files to
//     Claude and back) don't lose unsaved work.
//   - The Files tree can subscribe to "is this entry dirty?" without
//     prop-drilling.
//   - The unsaved-leave confirm flow can read the global dirty count
//     to gate `beforeunload`.
//
// We deliberately keep the dirty buffer in memory only — DESIGN_PRINCIPLES
// "下書きは積極保持 / drafts kept aggressively" allows persisting later,
// but for S011 the scope is "edits survive tab switches"; full
// `localStorage` persistence is on the Backlog (see ROADMAP.md).

import { create } from 'zustand'

export type EditorMode = 'view' | 'edit'

export interface EditorEntryState {
  mode: EditorMode
  /** Last-known server ETag for `If-Match` on the next PUT. */
  etag?: string
  /** Current buffer if the user has unsaved changes. */
  draft?: string
  /** Original content from the server (used to detect "user undid all
   *  edits and is back at clean"). */
  pristine?: string
  /** Most recent save error (network / 412 / etc.) — surfaced inline. */
  saveError?: string
  /** Pending conflict that needs user decision (set when PUT got 412). */
  conflict?: {
    /** ETag the server reports as current — what we have to send back to overwrite. */
    serverEtag: string
    /** The user's draft we tried to save. */
    localContent: string
  }
}

export interface EditorStoreState {
  /** Map keyed by `{repoId}/{branchId}/{path}`. */
  entries: Record<string, EditorEntryState>

  // Actions ────────────────────────────────────────────────────────────
  setMode: (key: string, mode: EditorMode) => void
  setEtag: (key: string, etag: string | undefined) => void
  setPristine: (key: string, pristine: string, etag: string | undefined) => void
  setDraft: (key: string, draft: string) => void
  /** Mark this file clean — drops the draft, clears save errors. */
  clearDraft: (key: string) => void
  setSaveError: (key: string, err: string | undefined) => void
  setConflict: (key: string, conflict: EditorEntryState['conflict']) => void
  /** Drop the entry entirely (file closed without dirty state, etc.). */
  forget: (key: string) => void
}

export function makeEditorKey(repoId: string, branchId: string, path: string): string {
  return `${repoId}/${branchId}/${path}`
}

/** Path component of an editor key — used by the FileList to decide if
 *  a row should show a dirty badge. */
export function pathFromKey(key: string): string {
  const idx = key.indexOf('/')
  if (idx < 0) return ''
  const idx2 = key.indexOf('/', idx + 1)
  if (idx2 < 0) return ''
  return key.slice(idx2 + 1)
}

export const useEditorStore = create<EditorStoreState>()((set) => ({
  entries: {},

  setMode: (key, mode) =>
    set((state) => ({
      entries: {
        ...state.entries,
        [key]: { ...(state.entries[key] ?? { mode: 'view' }), mode },
      },
    })),

  setEtag: (key, etag) =>
    set((state) => ({
      entries: {
        ...state.entries,
        [key]: { ...(state.entries[key] ?? { mode: 'view' }), etag },
      },
    })),

  setPristine: (key, pristine, etag) =>
    set((state) => ({
      entries: {
        ...state.entries,
        [key]: {
          ...(state.entries[key] ?? { mode: 'view' }),
          pristine,
          etag,
        },
      },
    })),

  setDraft: (key, draft) =>
    set((state) => {
      const cur = state.entries[key] ?? { mode: 'view' as EditorMode }
      // If draft equals pristine, the file is no longer dirty — drop
      // the draft so dirty-checks return false.
      if (cur.pristine != null && draft === cur.pristine) {
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { draft: _drop, ...rest } = cur
        return { entries: { ...state.entries, [key]: rest } }
      }
      return {
        entries: { ...state.entries, [key]: { ...cur, draft } },
      }
    }),

  clearDraft: (key) =>
    set((state) => {
      const cur = state.entries[key]
      if (!cur) return {}
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { draft: _d, saveError: _e, conflict: _c, ...rest } = cur
      return { entries: { ...state.entries, [key]: rest } }
    }),

  setSaveError: (key, err) =>
    set((state) => ({
      entries: {
        ...state.entries,
        [key]: { ...(state.entries[key] ?? { mode: 'view' }), saveError: err },
      },
    })),

  setConflict: (key, conflict) =>
    set((state) => ({
      entries: {
        ...state.entries,
        [key]: { ...(state.entries[key] ?? { mode: 'view' }), conflict },
      },
    })),

  forget: (key) =>
    set((state) => {
      if (!(key in state.entries)) return {}
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { [key]: _drop, ...rest } = state.entries
      return { entries: rest }
    }),
}))

/** True if any entry in the store has an unsaved draft. Used by the
 *  beforeunload guard. */
export function hasAnyDirty(state: EditorStoreState): boolean {
  for (const k of Object.keys(state.entries)) {
    const e = state.entries[k]
    if (e.draft != null && e.pristine != null && e.draft !== e.pristine) return true
  }
  return false
}

/** True if the entry at `key` has unsaved changes. */
export function isDirty(state: EditorStoreState, key: string): boolean {
  const e = state.entries[key]
  if (!e) return false
  return e.draft != null && e.pristine != null && e.draft !== e.pristine
}
