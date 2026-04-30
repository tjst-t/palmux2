import { useCallback, useEffect, useReducer, useRef, useState } from 'react'

import { ReconnectingWebSocket } from '../../lib/ws'

import { initialState, reduce } from './agent-state'
import type { AgentState } from './agent-state'

// In-memory snapshot cache keyed by `${repoId}/${branchId}`. Survives tab
// switches inside the SPA so navigating back to a Claude tab shows the
// previous turns immediately, while the WS reconnects in the background
// and refreshes from the server's authoritative snapshot.
const stateCache = new Map<string, AgentState>()
const cacheKey = (r: string, b: string) => `${r}/${b}`

interface SendFn {
  /** Send a user message. addDirs is the absolute filesystem paths the
   *  CLI should be granted tool access to via `--add-dir <path>` (S006).
   *  Empty / omitted → no change in CLI scope. The backend validates
   *  every path against the worktree boundary before forwarding. */
  userMessage: (content: string, addDirs?: string[]) => void
  interrupt: () => void
  permissionRespond: (
    permissionId: string,
    decision: 'allow' | 'deny',
    scope: 'once' | 'session' | 'always',
    updatedInput?: unknown,
    reason?: string,
  ) => void
  /** Submit the user's selected option(s) for an AskUserQuestion. The
   *  outer array holds one entry per question; the inner array carries
   *  the chosen option labels (multiple iff the question is
   *  multiSelect:true). The backend wakes the blocked tool's
   *  permission_prompt and ships the answers to the CLI. */
  askRespond: (permissionId: string, answers: string[][]) => void
  /** Submit the user's response to an ExitPlanMode permission. On
   *  approve, the backend wakes the CLI with behavior:"allow", optionally
   *  swaps the permission mode to opts.targetMode (default "auto"), and
   *  passes opts.editedPlan through as updatedInput.plan when non-empty.
   *  On reject, the CLI gets behavior:"deny" with a "User chose to keep
   *  planning" message and the agent stays in plan mode. */
  planRespond: (
    permissionId: string,
    decision: 'approve' | 'reject',
    opts?: { targetMode?: string; editedPlan?: string },
  ) => void
  setModel: (model: string) => void
  setEffort: (effort: string) => void
  setPermissionMode: (mode: string) => void
  sessionClear: () => void
  sessionResume: (sessionId: string) => void
  sessionFork: (baseSessionId: string) => void
}

type ConnState = 'connecting' | 'open' | 'closed' | 'closing'

interface UseAgentResult {
  state: AgentState
  connState: ConnState
  send: SendFn
}

export function useAgent(repoId: string, branchId: string): UseAgentResult {
  // Lazy initialiser hits the snapshot cache so the first paint shows the
  // previous turns instead of the empty-state placeholder.
  const [state, dispatch] = useReducer(reduce, undefined, () =>
    stateCache.get(cacheKey(repoId, branchId)) ?? initialState,
  )
  const [connState, setConnState] = useState<ConnState>('connecting')
  const wsRef = useRef<ReconnectingWebSocket | null>(null)

  // Mirror every state change into the cache so the next mount has fresh
  // data. Skipping `ready: false` (initial state) avoids overwriting a
  // good cache with a transient blank slate during branch switch.
  useEffect(() => {
    if (state.ready) {
      stateCache.set(cacheKey(repoId, branchId), state)
    }
  }, [repoId, branchId, state])

  useEffect(() => {
    if (!repoId || !branchId) return
    const url = buildWSUrl(repoId, branchId)
    const ws = new ReconnectingWebSocket({
      url,
      binaryType: 'arraybuffer',
      onState: (s) => setConnState(s),
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const parsed = JSON.parse(ev.data) as { type: string; ts: string; payload?: unknown }
          dispatch({ kind: 'event', ev: parsed })
        } catch {
          // ignore
        }
      },
    })
    wsRef.current = ws
    // On (re)mount or branch switch, restore from cache when available so
    // the UI has content to render immediately. The WS will follow up with
    // a session.init that supersedes whatever we restored.
    const cached = stateCache.get(cacheKey(repoId, branchId))
    if (cached) {
      dispatch({ kind: 'restore', state: cached })
    } else {
      dispatch({ kind: 'reset' })
    }
    ws.connect()
    return () => {
      ws.close(1000, 'unmount')
      wsRef.current = null
    }
  }, [repoId, branchId])

  const sendMessage = useCallback((content: string, addDirs?: string[]) => {
    if (!content) return
    const payload: { content: string; addDirs?: string[] } = { content }
    if (addDirs && addDirs.length > 0) payload.addDirs = addDirs
    wsRef.current?.send(JSON.stringify({ type: 'user.message', payload }))
  }, [])

  const interrupt = useCallback(() => {
    wsRef.current?.send(JSON.stringify({ type: 'interrupt' }))
  }, [])

  const permissionRespond = useCallback<SendFn['permissionRespond']>((permissionId, decision, scope, updatedInput, reason) => {
    wsRef.current?.send(
      JSON.stringify({
        type: 'permission.respond',
        payload: { permissionId, decision, scope, updatedInput, reason },
      }),
    )
  }, [])

  const askRespond = useCallback<SendFn['askRespond']>((permissionId, answers) => {
    wsRef.current?.send(
      JSON.stringify({
        type: 'ask.respond',
        payload: { permissionId, answers },
      }),
    )
  }, [])

  const planRespond = useCallback<SendFn['planRespond']>((permissionId, decision, opts) => {
    wsRef.current?.send(
      JSON.stringify({
        type: 'plan.respond',
        payload: {
          permissionId,
          decision,
          targetMode: opts?.targetMode ?? '',
          editedPlan: opts?.editedPlan ?? '',
        },
      }),
    )
  }, [])

  const setModel = useCallback((model: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'model.set', payload: { model } }))
  }, [])

  const setEffort = useCallback((effort: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'effort.set', payload: { effort } }))
  }, [])

  const setPermissionMode = useCallback((mode: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'permission_mode.set', payload: { mode } }))
  }, [])

  const sessionClear = useCallback(() => {
    wsRef.current?.send(JSON.stringify({ type: 'session.clear' }))
  }, [])

  const sessionResume = useCallback((sessionId: string) => {
    wsRef.current?.send(
      JSON.stringify({ type: 'session.resume', payload: { sessionId } }),
    )
  }, [])

  const sessionFork = useCallback((baseSessionId: string) => {
    wsRef.current?.send(
      JSON.stringify({ type: 'session.fork', payload: { baseSessionId } }),
    )
  }, [])

  return {
    state,
    connState,
    send: {
      userMessage: sendMessage,
      interrupt,
      permissionRespond,
      askRespond,
      planRespond,
      setModel,
      setEffort,
      setPermissionMode,
      sessionClear,
      sessionResume,
      sessionFork,
    },
  }
}

function buildWSUrl(repoId: string, branchId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/claude/agent`
  return `${proto}//${window.location.host}${path}`
}
