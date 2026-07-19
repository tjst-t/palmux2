import { useEffect } from 'react'
import { Navigate, Route, Routes, useParams, useSearchParams } from 'react-router-dom'

import { CommandPalette } from './components/command-palette/command-palette'
import { ConfirmDialogRenderer } from './components/context-menu/confirm-dialog'
import { ContextMenuRenderer } from './components/context-menu/context-menu'
import { PromptDialogRenderer } from './components/context-menu/prompt-dialog'
import { SelectDialogRenderer } from './components/context-menu/select-dialog'
import { HomeRedirect } from './components/redirect'
import { MainLayout } from './components/main-layout'
import { useEventStream } from './hooks/use-event-stream'
import { useVisualViewport } from './hooks/use-visual-viewport'
import { useAgentRegistryStore } from './stores/agent-registry-store'
import { usePalmuxStore } from './stores/palmux-store'
import { TestHarness } from './tabs/claude-agent/test-harness'
import { BrowserFullscreen } from './tabs/browser/browser-view'

// S1f75ec-2: Redirect /claude-tui → /claude (canonical URL).
// The WS endpoint paths (/api/.../tabs/claude-tui/attach) are NOT changed;
// only the page URL is redirected.
function ClaudeTuiRedirect() {
  const { repoId, branchId } = useParams()
  return (
    <Navigate
      to={`/${encodeURIComponent(repoId ?? '')}/${encodeURIComponent(branchId ?? '')}/claude`}
      replace
    />
  )
}

// S62374c-2: standalone full-window browser view.
// Renders BrowserFullscreen when the tabId is 'browser' AND ?view=fullscreen.
// Otherwise falls through to the normal MainLayout.
function MainLayoutOrBrowserFullscreen() {
  const { repoId, branchId, tabId } = useParams()
  const [searchParams] = useSearchParams()
  if (tabId === 'browser' && searchParams.get('view') === 'fullscreen' && repoId && branchId) {
    return <BrowserFullscreen repoId={repoId} branchId={branchId} />
  }
  return <MainLayout />
}

function App() {
  const bootstrap = usePalmuxStore((s) => s.bootstrap)
  const error = usePalmuxStore((s) => s.error)
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)
  const fetchAgents = useAgentRegistryStore((s) => s.fetchAgents)

  useEffect(() => {
    void bootstrap()
  }, [bootstrap])

  // S2b5691-2: load the agent registry once at boot, in parallel with the
  // main bootstrap. fetchAgents never throws (falls back to claude-only on
  // error), so this can't fail App startup.
  useEffect(() => {
    void fetchAgents()
  }, [fetchAgents])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  useEventStream()
  useVisualViewport()

  if (error) {
    return (
      <div style={{ padding: 24, color: 'var(--color-error)' }}>
        <p>Error talking to Palmux server: {error}</p>
        <p style={{ color: 'var(--color-fg-muted)' }}>
          Open <code>?</code> via <code>/auth?token=…</code> if you started the server with{' '}
          <code>--token</code>.
        </p>
      </div>
    )
  }

  return (
    <>
      <Routes>
        <Route path="/" element={<HomeOrLayout />} />
        {/* S017: hidden test harness route. Drives the virtualised
            ConversationList + Read-preview surface from synthetic data
            so E2E doesn't need a live claude CLI. */}
        <Route path="/__test/claude" element={<TestHarness />} />
        {/* S1f75ec-2: /claude-tui is no longer a visible URL. Redirect to
            canonical /claude. The backend still exposes WS at
            /tabs/claude-tui/attach but the page URL is /claude. */}
        <Route path="/:repoId/:branchId/claude-tui" element={<ClaudeTuiRedirect />} />
        <Route path="/:repoId/:branchId/claude-tui/*" element={<ClaudeTuiRedirect />} />
        <Route path="/:repoId/:branchId/:tabId/*" element={<MainLayoutOrBrowserFullscreen />} />
      </Routes>
      <ContextMenuRenderer />
      <ConfirmDialogRenderer />
      <PromptDialogRenderer />
      <SelectDialogRenderer />
      <CommandPalette />
    </>
  )
}

function HomeOrLayout() {
  const bootstrapped = usePalmuxStore((s) => s.bootstrapped)
  return (
    <>
      <HomeRedirect />
      {bootstrapped && <MainLayout />}
    </>
  )
}

export default App
