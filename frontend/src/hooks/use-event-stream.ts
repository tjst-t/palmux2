import { useEffect } from 'react'

import { type RemoteEvent, usePalmuxStore } from '../stores/palmux-store'
import { ReconnectingWebSocket } from '../lib/ws'

function buildEventsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/api/events`
}

// Subscribes to /api/events for the lifetime of the component. Domain events
// trigger a /api/repos refresh; the store handles the actual diffing.
export function useEventStream() {
  const applyEvent = usePalmuxStore((s) => s.applyEvent)
  const reloadRepos = usePalmuxStore((s) => s.reloadRepos)
  const setStatus = usePalmuxStore((s) => s.setConnectionStatus)

  useEffect(() => {
    const ws = new ReconnectingWebSocket({
      url: buildEventsURL(),
      binaryType: 'blob',
      onState: (s) => {
        if (s === 'open') {
          setStatus('connected')
          // On (re)connect, do a full reload so we never miss events.
          void reloadRepos()
        } else if (s === 'connecting') {
          setStatus('connecting')
        } else {
          setStatus('disconnected')
        }
      },
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const msg = JSON.parse(ev.data) as RemoteEvent
          applyEvent(msg)
        } catch {
          // ignore
        }
      },
    })
    ws.connect()
    return () => ws.close()
  }, [applyEvent, reloadRepos, setStatus])
}
