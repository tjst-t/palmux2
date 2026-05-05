// Zustand store for per-worktree network namespace isolation (S034).
// Manages listeners, port mappings, and netns availability state.

import { create } from 'zustand'
import { api } from '../lib/api'

// ─── Types (mirror Go structs) ────────────────────────────────────────────────

export interface Listener {
  port: number
  processName?: string
  pid?: number
  exposed: boolean
  hostPort?: number
}

export interface PortMapping {
  hostPort: number
  internalPort: number
  createdAt: string
  publicUrl?: string
}

// ─── Store ────────────────────────────────────────────────────────────────────

interface NetnsState {
  /** worktreeId (= branchId) → listener list from WS events */
  listeners: Record<string, Listener[]>
  /** worktreeId → port mappings */
  ports: Record<string, PortMapping[]>
  /** True while the network modal is open */
  modalOpen: boolean
  /** branchId for which the modal is open */
  modalBranchId: string | null
  /** repoId for which the modal is open */
  modalRepoId: string | null
  /** Whether slirp4netns is available (from server health or settings) */
  slirpAvailable: boolean
  /** Whether Caddy is available */
  caddyAvailable: boolean
  /** Isolation restart confirm dialog state */
  restartConfirmOpen: boolean
  restartConfirmBranchId: string | null

  // Actions
  openModal: (repoId: string, branchId: string) => void
  closeModal: () => void
  openRestartConfirm: (branchId: string) => void
  closeRestartConfirm: () => void

  /** Called from WS event handler for netns.listenersChanged */
  setListeners: (branchId: string, listeners: Listener[]) => void

  /** Fetch listeners from REST (fallback / initial load) */
  fetchListeners: (repoId: string, branchId: string) => Promise<void>

  /** Fetch port mappings */
  fetchPorts: (repoId: string, branchId: string) => Promise<void>

  /** Expose a port — POST /ports/expose */
  exposePort: (repoId: string, branchId: string, internalPort: number, hostPort?: number) => Promise<PortMapping>

  /** Un-expose a port — DELETE /ports/{hostPort} */
  unexposePort: (repoId: string, branchId: string, hostPort: number) => Promise<void>

  setSlirpAvailable: (v: boolean) => void
  setCaddyAvailable: (v: boolean) => void
}

export const useNetnsStore = create<NetnsState>((set, get) => ({
  listeners: {},
  ports: {},
  modalOpen: false,
  modalBranchId: null,
  modalRepoId: null,
  slirpAvailable: true, // optimistic default; corrected on first API call
  caddyAvailable: false,
  restartConfirmOpen: false,
  restartConfirmBranchId: null,

  openModal(repoId, branchId) {
    set({ modalOpen: true, modalBranchId: branchId, modalRepoId: repoId })
    // Eagerly fetch both listeners and ports.
    void get().fetchListeners(repoId, branchId)
    void get().fetchPorts(repoId, branchId)
  },

  closeModal() {
    set({ modalOpen: false, modalBranchId: null, modalRepoId: null })
  },

  openRestartConfirm(branchId) {
    set({ restartConfirmOpen: true, restartConfirmBranchId: branchId })
  },

  closeRestartConfirm() {
    set({ restartConfirmOpen: false, restartConfirmBranchId: null })
  },

  setListeners(branchId, listeners) {
    set((state) => ({
      listeners: { ...state.listeners, [branchId]: listeners },
    }))
  },

  async fetchListeners(repoId, branchId) {
    try {
      const listeners = await api.get<Listener[]>(
        `/api/repos/${repoId}/branches/${branchId}/listeners`
      )
      set((state) => ({
        listeners: { ...state.listeners, [branchId]: listeners ?? [] },
      }))
    } catch {
      // Non-isolated worktree — ignore 404
    }
  },

  async fetchPorts(repoId, branchId) {
    try {
      const ports = await api.get<PortMapping[]>(
        `/api/repos/${repoId}/branches/${branchId}/ports`
      )
      set((state) => ({
        ports: { ...state.ports, [branchId]: ports ?? [] },
      }))
    } catch {
      // Non-isolated worktree — ignore
    }
  },

  async exposePort(repoId, branchId, internalPort, hostPort) {
    const pm = await api.post<PortMapping>(
      `/api/repos/${repoId}/branches/${branchId}/ports/expose`,
      { internalPort, hostPort }
    )
    set((state) => ({
      ports: {
        ...state.ports,
        [branchId]: [...(state.ports[branchId] ?? []), pm],
      },
    }))
    return pm
  },

  async unexposePort(repoId, branchId, hostPort) {
    await api.delete(`/api/repos/${repoId}/branches/${branchId}/ports/${hostPort}`)
    set((state) => ({
      ports: {
        ...state.ports,
        [branchId]: (state.ports[branchId] ?? []).filter((p) => p.hostPort !== hostPort),
      },
    }))
  },

  setSlirpAvailable(v) {
    set({ slirpAvailable: v })
  },

  setCaddyAvailable(v) {
    set({ caddyAvailable: v })
  },
}))
