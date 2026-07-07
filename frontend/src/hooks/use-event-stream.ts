import { useEffect } from 'react'

import { type RemoteEvent, usePalmuxStore } from '../stores/palmux-store'
import { incusGroupApi, selfUpdateApi } from '../lib/api'
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
    let sawDrop = false
    const ws = new ReconnectingWebSocket({
      url: buildEventsURL(),
      binaryType: 'blob',
      onState: (s) => {
        if (s === 'open') {
          setStatus('connected')
          // On (re)connect, do a full reload so we never miss events.
          void reloadRepos()
          // S6ab0ed: self-update reconnect handshake. If an "Update all" was in
          // flight and the WS dropped (palmux restarted itself after the
          // home-manager switch), confirm the new version is live, then surface
          // the completion toast. The reconnect itself is handled by
          // ReconnectingWebSocket — we only observe the open-after-drop edge.
          if (sawDrop) {
            void maybeFinishSelfUpdate()
          }
          sawDrop = false
        } else if (s === 'connecting') {
          setStatus('connecting')
        } else {
          setStatus('disconnected')
          sawDrop = true
        }
      },
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return
        try {
          const msg = JSON.parse(ev.data) as RemoteEvent
          applyEvent(msg)
          // S012: also re-broadcast as a DOM CustomEvent so component-
          // local hooks (e.g. useGitStatusEvents) can subscribe to
          // narrow event types without round-tripping through Zustand.
          window.dispatchEvent(new CustomEvent('palmux:event', { detail: msg }))
        } catch {
          // ignore
        }
      },
    })
    ws.connect()
    return () => ws.close()
  }, [applyEvent, reloadRepos, setStatus])
}

// maybeFinishSelfUpdate runs on the first successful reconnect after a WS drop
// while an "Update all" is in flight. It polls /api/health for the new version
// (the restarted palmux reports it) and, when it differs from the baseline
// recorded at trigger time, fires the completion toast. If the version never
// changes within the timeout, it marks the update as failed (the home-manager
// generation rollback kept the old version) — AC-S6ab0ed-2-2 / 2-3.
async function maybeFinishSelfUpdate(): Promise<void> {
  if (!usePalmuxStore.getState().updateInProgress) return

  // Sfef725-2-3: the incus-admin click-recover restarts the user manager (which
  // restarts palmux), so it rides the SAME WS-drop → reconnect handshake. We
  // distinguish it by the incusGroupFixInProgress flag: instead of comparing
  // /health versions (the version is unchanged across a user-manager restart),
  // we wait for the server to come back and re-fetch the incus-admin group
  // state, then route the toast / failure accordingly.
  if (usePalmuxStore.getState().incusGroupFixInProgress) {
    await maybeFinishIncusGroupFix()
    return
  }

  // Optional E2E hook: shorten the handshake poll window so the failure
  // (rollback) path is testable without a 60s wait. Never set in production.
  const w = window as unknown as { __PALMUX_UPDATE_TIMEOUT_MS__?: number }
  await pollForNewVersion({
    baseline: usePalmuxStore.getState().updateBaselineVersion,
    timeoutMs: typeof w.__PALMUX_UPDATE_TIMEOUT_MS__ === 'number' ? w.__PALMUX_UPDATE_TIMEOUT_MS__ : undefined,
    fetchVersion: async () => (await selfUpdateApi.health()).version ?? '',
    onSuccess: (v) => {
      usePalmuxStore.setState({
        updateInProgress: false,
        updateFailed: false,
        updateToast: { version: v },
        serverInfo: { ...usePalmuxStore.getState().serverInfo, version: v },
      })
      void usePalmuxStore.getState().loadSelfUpdate()
      // Sfef725-3-2: a self-update may have added the incus-admin group; if the
      // restarted server reports a stale state, refresh it so the recover
      // surface (Story 2) appears and routes the user to the fix.
      void usePalmuxStore.getState().loadIncusGroup()
      // A version change means the server now serves a NEW frontend bundle, but
      // this reconnect handshake only re-attaches the WS — the browser is still
      // running the OLD bundle, so frontend-side changes in the update would not
      // take effect until a manual reload (they'd look "not fixed"). Reload to
      // load the new FE. Delayed so the "更新しました" toast is briefly visible;
      // terminal state is server-side (tmux/daemon) so it survives the reload.
      // Suppressible for E2E via window.__PALMUX_NO_RELOAD__ so the reconnect
      // test can still assert the toast without navigating away.
      const wr = window as unknown as { __PALMUX_NO_RELOAD__?: boolean }
      if (!wr.__PALMUX_NO_RELOAD__) {
        setTimeout(() => window.location.reload(), 1500)
      }
    },
    onFailure: () => usePalmuxStore.setState({ updateInProgress: false, updateFailed: true }),
  })
}

// maybeFinishIncusGroupFix completes the incus-admin click-recover handshake: it
// polls /api/incus-group until the server reports the group is no longer stale
// (state OK) → success toast, or the timeout elapses → failure flag. Reuses the
// pollForNewVersion mechanics with the group STATE as the changing signal.
async function maybeFinishIncusGroupFix(): Promise<void> {
  const w = window as unknown as { __PALMUX_UPDATE_TIMEOUT_MS__?: number }
  await pollForNewVersion({
    // The "baseline" here is the pre-restart state ("stale"); success is any
    // reading that is NOT stale (ok = group applied). We encode the state as the
    // version string and let the differ detect the transition out of "stale".
    baseline: 'stale',
    timeoutMs: typeof w.__PALMUX_UPDATE_TIMEOUT_MS__ === 'number' ? w.__PALMUX_UPDATE_TIMEOUT_MS__ : undefined,
    fetchVersion: async () => {
      const st = await incusGroupApi.get()
      // Report the state so a transition stale→ok registers as a "change".
      usePalmuxStore.setState({ incusGroup: st })
      return st.state
    },
    onSuccess: (state) => {
      // OK = the group is now applied. A transition to not-member/n/a means the
      // restart ran but the group still isn't active on the process — surface a
      // distinct message instead of silently resetting (the recover panel will
      // re-render with the new state's guidance).
      usePalmuxStore.setState({
        incusGroupFixInProgress: false,
        updateInProgress: false,
        updateFailed: false,
        updateToast:
          state === 'ok'
            ? { version: '', message: 'incus-admin を適用しました。incus-container が使えます。' }
            : { version: '', message: 'サーバを再起動しましたが incus-admin はまだ反映されていません。下の案内を確認してください。' },
      })
    },
    onFailure: () =>
      usePalmuxStore.setState({ incusGroupFixInProgress: false, updateInProgress: false, updateFailed: true }),
  })
}

// pollForNewVersion is the pure, testable core of the self-update reconnect
// handshake. It polls fetchVersion() until it returns a non-empty version that
// differs from the baseline (→ onSuccess), or the deadline elapses (→ onFailure).
// Extracted so the decision logic can be unit-tested without a real WS.
//
// Robust-baseline rule: if `baseline` is null/empty (the bootstrap /health fetch
// failed, so we never recorded the pre-update version), we MUST NOT treat the
// first non-empty version as success — a rolled-back update would report the
// SAME old version and falsely show "更新しました". In that case we adopt the
// first successfully-fetched version as the effective baseline and only succeed
// if a LATER poll returns a different one. This keeps the rollback (failure)
// path reachable even without a recorded baseline.
export async function pollForNewVersion(opts: {
  baseline: string | null
  fetchVersion: () => Promise<string>
  onSuccess: (version: string) => void
  onFailure: () => void
  timeoutMs?: number
  intervalMs?: number
}): Promise<void> {
  const timeoutMs = opts.timeoutMs ?? 60_000
  const intervalMs = opts.intervalMs ?? 2000
  const deadline = Date.now() + timeoutMs
  let effectiveBaseline = opts.baseline && opts.baseline.length > 0 ? opts.baseline : null
  for (;;) {
    try {
      const v = await opts.fetchVersion()
      if (v) {
        if (effectiveBaseline === null) {
          // No recorded baseline yet — adopt this first reading and keep polling
          // for a CHANGE (so a rollback to the same version is not a false win).
          effectiveBaseline = v
        } else if (v !== effectiveBaseline) {
          opts.onSuccess(v)
          return
        }
      }
    } catch {
      // health not reachable yet (still restarting); keep polling.
    }
    if (Date.now() >= deadline) {
      opts.onFailure()
      return
    }
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}
