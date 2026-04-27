import { useCallback, useEffect, useReducer, useRef, useState } from 'react'

import { ReconnectingWebSocket } from '../../lib/ws'

import { initialState, reduce } from './agent-state'
import type { AgentState } from './agent-state'

interface SendFn {
  userMessage: (content: string) => void
  interrupt: () => void
  permissionRespond: (
    permissionId: string,
    decision: 'allow' | 'deny',
    scope: 'once' | 'session' | 'always',
    updatedInput?: unknown,
    reason?: string,
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
  const [state, dispatch] = useReducer(reduce, initialState)
  const [connState, setConnState] = useState<ConnState>('connecting')
  const wsRef = useRef<ReconnectingWebSocket | null>(null)

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
    dispatch({ kind: 'reset' })
    ws.connect()
    return () => {
      ws.close(1000, 'unmount')
      wsRef.current = null
    }
  }, [repoId, branchId])

  const sendMessage = useCallback((content: string) => {
    if (!content) return
    wsRef.current?.send(JSON.stringify({ type: 'user.message', payload: { content } }))
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
